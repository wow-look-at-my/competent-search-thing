package index

// Env-gated measurement harness. Nothing in this file runs by default:
// every test/benchmark skips unless its env var is set, so CI and the
// normal gate are unaffected. The harness backs the PR-body efficiency
// numbers (bytes/entry, GC behavior, whole-filesystem walk reality,
// multi-million-entry query latency).
//
// Phase split (deliberate): go-toolchain's TEST phase is
// coverage-instrumented, so wall-clock timings taken inside tests are
// skewed -- the tests here therefore carry only MEMORY and GC numbers
// (unaffected by instrumentation) and label their elapsed times as
// instrumented. Timing-critical measurements live in the benchmark
// phase (bench_test.go: BenchmarkWalkRootMeasure, BenchmarkSearchHuge),
// which go-toolchain runs separately without coverage.
//
// The test phase and the benchmark phase are separate processes, so a
// gated run that exercises both builds the huge synthetic store twice
// (once per process); within a process it is built once and shared.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// Environment gates and knobs.
const (
	measureEnv     = "COMPETENT_SEARCH_MEASURE"      // whole-/ walk measurements
	measureHugeEnv = "COMPETENT_SEARCH_MEASURE_HUGE" // huge synth store measurements
	measureOutEnv  = "COMPETENT_SEARCH_MEASURE_OUT"  // report file path (JSON; .txt sibling)
)

// measureExcludes composes the exclude list for a whole-filesystem walk
// exactly like Manager.BuildFromDisk does: the configured excludes (the
// branch's defaults: .git, node_modules, .cache, /proc, /sys, /dev,
// /run, /tmp, /var/tmp, lost+found) plus the mount-derived skip list
// for roots, appended as full-path patterns.
func measureExcludes(roots []string) (excludes, skips []string) {
	base := config.Default().Excludes
	skips = SystemMountSkips(roots)
	excludes = append(append([]string{}, base...), skips...)
	return excludes, skips
}

// timedGCs runs n forced garbage collections and returns each one's
// wall time in milliseconds.
func timedGCs(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		start := time.Now()
		runtime.GC()
		out[i] = float64(time.Since(start).Nanoseconds()) / 1e6
	}
	return out
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

// measureReport is the structured output of one measurement, written as
// JSON (plus a rendered text sibling) to $COMPETENT_SEARCH_MEASURE_OUT
// when set, and always t.Log-ed. Field names are the JSON keys.
type measureReport struct {
	Label         string
	GoVersion     string
	NumCPU        int
	Entries       int
	LiveEntries   int
	Dirs          int
	BuildSeconds  float64 // coverage-instrumented (test phase); see Notes
	EntriesPerSec float64
	Walk          *WalkStats `json:",omitempty"`
	MountSkips    []string   `json:",omitempty"`
	Excludes      []string   `json:",omitempty"`
	Footprint     Footprint
	BytesPerEntry float64

	// Heap evidence (runtime.ReadMemStats around build and release).
	HeapAllocBeforeMB   float64
	HeapAllocAfterMB    float64
	HeapInuseAfterMB    float64
	HeapAllocReleasedMB float64
	GCDuringBuild       uint32

	// Forced-GC wall times, store live vs after release (ms each).
	LiveGCMS     []float64
	BaselineGCMS []float64

	Notes []string `json:",omitempty"`
}

// text renders the human-readable form of the report.
func (r measureReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s ==\n", r.Label)
	fmt.Fprintf(&b, "%s, GOMAXPROCS %d\n", r.GoVersion, r.NumCPU)
	fmt.Fprintf(&b, "entries: %d (live %d), dirs %d\n", r.Entries, r.LiveEntries, r.Dirs)
	fmt.Fprintf(&b, "build: %.2fs (%.0f entries/s)\n", r.BuildSeconds, r.EntriesPerSec)
	if r.Walk != nil {
		fmt.Fprintf(&b, "walk: %d indexed, %d dirs read, %d dir read errors, %d roots skipped\n",
			r.Walk.Indexed, r.Walk.Dirs, r.Walk.Errors, r.Walk.SkippedRoots)
	}
	if len(r.MountSkips) > 0 {
		fmt.Fprintf(&b, "mount skips (%d): %s\n", len(r.MountSkips), strings.Join(r.MountSkips, ", "))
	}
	f := r.Footprint
	fmt.Fprintf(&b, "footprint: %.1f MB total, %.1f B/entry\n", mb(uint64(f.TotalBytes)), r.BytesPerEntry)
	per := func(v int64) float64 {
		if r.Entries == 0 {
			return 0
		}
		return float64(v) / float64(r.Entries)
	}
	row := func(name string, v int64) {
		fmt.Fprintf(&b, "  %-22s %10.1f MB  %6.2f B/e\n", name, mb(uint64(v)), per(v))
	}
	row("name blob (lower):", f.NameLowerBytes)
	row("name blob (orig):", f.NameOrigBytes)
	row("offset tables:", f.OffsetBytes)
	row("parent column:", f.ParentBytes)
	row("flag column:", f.FlagBytes)
	row("dir strings:", f.DirStringBytes)
	row("dir lower extra:", f.DirLowerExtraBytes)
	row("dir headers:", f.DirHeaderBytes)
	row("dirIndex (approx):", f.DirIndexApproxBytes)
	row("children (approx):", f.ChildrenApproxBytes)
	fmt.Fprintf(&b, "heap: %.1f MB alloc before -> %.1f MB after build (inuse %.1f MB); %.1f MB after release\n",
		r.HeapAllocBeforeMB, r.HeapAllocAfterMB, r.HeapInuseAfterMB, r.HeapAllocReleasedMB)
	fmt.Fprintf(&b, "GC cycles during build: %d\n", r.GCDuringBuild)
	fmt.Fprintf(&b, "forced GC, store live (ms):     %s\n", msList(r.LiveGCMS))
	fmt.Fprintf(&b, "forced GC, store released (ms): %s\n", msList(r.BaselineGCMS))
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

func msList(v []float64) string {
	parts := make([]string, len(v))
	for i, ms := range v {
		parts[i] = fmt.Sprintf("%.2f", ms)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// writeMeasureReport logs the text form and, when
// $COMPETENT_SEARCH_MEASURE_OUT is set, writes the JSON there plus the
// text to the same path with a .txt suffix. Runs that gate more than
// one measurement on the same OUT path overwrite it last-wins; the
// intended use is one gate env per run.
func writeMeasureReport(tb testing.TB, rep measureReport) {
	text := rep.text()
	tb.Log("\n" + text)
	out := os.Getenv(measureOutEnv)
	if out == "" {
		return
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	require.Nil(tb, err)
	require.Nil(tb, os.WriteFile(out, data, 0o644))
	require.Nil(tb, os.WriteFile(out+".txt", []byte(text), 0o644))
	tb.Logf("measure: wrote %s and %s.txt", out, out)
}

// walkRootMeasured walks "/" (default excludes + mount skips, composed
// like BuildFromDisk) into a fresh store and returns everything the
// report needs. The store stays live only inside this function -- the
// caller gets footprint and GC numbers, and the store becomes
// collectable on return, so the caller's baseline GCs measure a heap
// without it.
func walkRootMeasured(t *testing.T) measureReport {
	roots := []string{"/"}
	excludes, skips := measureExcludes(roots)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	st := NewStore()
	start := time.Now()
	stats, err := Walk(context.Background(), st, roots, excludes, nil)
	elapsed := time.Since(start).Seconds()
	require.Nil(t, err)
	require.Greater(t, stats.Indexed, 0)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	fp := st.Footprint()
	return measureReport{
		Label:         "whole-filesystem walk of / (test phase)",
		GoVersion:     runtime.Version(),
		NumCPU:        runtime.GOMAXPROCS(0),
		Entries:       fp.Entries,
		LiveEntries:   fp.LiveEntries,
		Dirs:          fp.Dirs,
		BuildSeconds:  elapsed,
		EntriesPerSec: float64(stats.Indexed) / elapsed,
		Walk:          &stats,
		MountSkips:    skips,
		Excludes:      excludes,
		Footprint:     fp,
		BytesPerEntry: fp.BytesPerEntry(),

		HeapAllocBeforeMB: mb(before.HeapAlloc),
		HeapAllocAfterMB:  mb(after.HeapAlloc),
		HeapInuseAfterMB:  mb(after.HeapInuse),
		GCDuringBuild:     after.NumGC - before.NumGC,
		LiveGCMS:          timedGCs(5),
		Notes: []string{
			"walk time is COVERAGE-INSTRUMENTED (go-toolchain test phase); see BenchmarkWalkRootMeasure for the un-instrumented walk",
			"memory, footprint and GC numbers are unaffected by coverage instrumentation",
		},
	}
}

// TestMeasureWholeFilesystemIndex builds the index for the whole
// container filesystem the same way BuildFromDisk would (default
// excludes + mount skips) and reports footprint, heap and forced-GC
// evidence. Skips unless COMPETENT_SEARCH_MEASURE=1.
func TestMeasureWholeFilesystemIndex(t *testing.T) {
	if os.Getenv(measureEnv) == "" {
		t.Skip("set COMPETENT_SEARCH_MEASURE=1 to run the whole-filesystem measurement")
	}
	rep := walkRootMeasured(t)
	runtime.GC()
	runtime.GC()
	var released runtime.MemStats
	runtime.ReadMemStats(&released)
	rep.HeapAllocReleasedMB = mb(released.HeapAlloc)
	rep.BaselineGCMS = timedGCs(5)
	writeMeasureReport(t, rep)
}

// Huge synthetic store: shared, lazily built, resettable. Guarded by a
// mutex (not sync.Once) so the measurement can release it for its
// baseline GC numbers and a later user in the same process would
// simply rebuild. Everything huge lives in the BENCHMARK phase:
// go-toolchain's test phase enforces a 30s per-test budget, and a
// 30M-entry build (coverage-instrumented there on top) does not fit.
const hugeStoreEntries = 30_000_000

var (
	hugeMu       sync.Mutex
	hugeSt       *Store
	hugeBuildSec float64

	// Heap evidence captured around the build, for the report.
	hugeHeapBeforeMB float64
	hugeHeapAfterMB  float64
	hugeHeapInuseMB  float64
	hugeGCsDuring    uint32
)

// hugeStore returns the shared huge synthetic store, building it on
// first use (seconds; announced through tb).
func hugeStore(tb testing.TB) *Store {
	hugeMu.Lock()
	defer hugeMu.Unlock()
	if hugeSt == nil {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		hugeSt = buildSynthStore(303, hugeStoreEntries)
		hugeBuildSec = time.Since(start).Seconds()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		hugeHeapBeforeMB = mb(before.HeapAlloc)
		hugeHeapAfterMB = mb(after.HeapAlloc)
		hugeHeapInuseMB = mb(after.HeapInuse)
		hugeGCsDuring = after.NumGC - before.NumGC
		tb.Logf("huge synth store: %d entries built in %.1fs", hugeStoreEntries, hugeBuildSec)
	}
	return hugeSt
}

// hugeStoreRelease drops the shared store (and the hit-count cache tied
// to it) so the footprint test can measure a baseline heap.
func hugeStoreRelease() {
	hugeMu.Lock()
	hugeSt = nil
	hugeMu.Unlock()
	hugeHitsMu.Lock()
	hugeHitsCache = map[string]int{}
	hugeHitsMu.Unlock()
}

// Reference hit counts against the huge store are full linear scans
// that cost seconds at this size, and the bench framework re-invokes
// each sub-benchmark while ramping b.N -- so they are cached per
// process.
var (
	hugeHitsMu    sync.Mutex
	hugeHitsCache = map[string]int{}
)

func hugeHitCount(st *Store, q string, pathMode bool) int {
	key := "n:" + q
	if pathMode {
		key = "p:" + q
	}
	hugeHitsMu.Lock()
	defer hugeHitsMu.Unlock()
	if n, ok := hugeHitsCache[key]; ok {
		return n
	}
	n := 0
	if pathMode {
		n = countPathMatches(st, q)
	} else {
		n = countMatches(st, q)
	}
	hugeHitsCache[key] = n
	return n
}

// hugeMeasured guards the one-shot huge-store measurement against the
// benchmark framework's b.N re-invocations.
var hugeMeasured bool

// hugeMeasureAndReport runs the one-shot huge-store measurement from
// the benchmark phase (see BenchmarkHugeStoreMeasure): the live phase
// (footprint, heap evidence, 5 timed forced GCs, then release of the
// shared store) runs inside hugeLivePhase so its frame -- the only
// remaining store reference -- is gone before the baseline GCs here
// see the emptied heap. After this returns the shared store is gone; a
// later hugeStore call would rebuild it, so the calling benchmark is
// declared after the other huge benches.
func hugeMeasureAndReport(tb testing.TB) {
	if hugeMeasured {
		return
	}
	hugeMeasured = true
	rep := hugeLivePhase(tb)
	runtime.GC()
	runtime.GC()
	var released runtime.MemStats
	runtime.ReadMemStats(&released)
	rep.HeapAllocReleasedMB = mb(released.HeapAlloc)
	rep.BaselineGCMS = timedGCs(5)
	writeMeasureReport(tb, rep)
}

// hugeLivePhase measures everything that needs the huge store live,
// then releases it. The returned report holds no reference to the
// store, so it is collectable the moment this function returns.
func hugeLivePhase(tb testing.TB) measureReport {
	st := hugeStore(tb)
	fp := st.Footprint()
	rep := measureReport{
		Label:         fmt.Sprintf("huge synthetic store (%d entries, benchmark phase)", hugeStoreEntries),
		GoVersion:     runtime.Version(),
		NumCPU:        runtime.GOMAXPROCS(0),
		Entries:       fp.Entries,
		LiveEntries:   fp.LiveEntries,
		Dirs:          fp.Dirs,
		BuildSeconds:  hugeBuildSec,
		EntriesPerSec: float64(fp.Entries) / hugeBuildSec,
		Footprint:     fp,
		BytesPerEntry: fp.BytesPerEntry(),

		HeapAllocBeforeMB: hugeHeapBeforeMB,
		HeapAllocAfterMB:  hugeHeapAfterMB,
		HeapInuseAfterMB:  hugeHeapInuseMB,
		GCDuringBuild:     hugeGCsDuring,
		LiveGCMS:          timedGCs(5),
		Notes: []string{
			"in-memory synthetic build (no disk IO), benchmark phase: un-instrumented wall times",
			"query latency for this store: BenchmarkSearchHuge (same process, same store)",
			"measured here rather than the test phase: the 30s per-test budget cannot fit a 30M-entry build",
		},
	}
	hugeStoreRelease()
	return rep
}
