package watch

// Env-gated measurement harness for the CURRENT (pre-redesign) watch
// layer, the sibling of internal/index's gated bench-phase harness:
// nothing in this file runs by default -- BenchmarkWatchMeasure skips
// unless COMPETENT_SEARCH_WATCH_MEASURE=1 -- so CI and the normal gate
// are unaffected. It produces the baseline numbers for the
// watcher-scale work: watch registration wall/CPU cost and the
// kernel-side price of the one-watch-per-directory model, event-storm
// throughput and index convergence, idle cost, and shutdown latency.
//
// The harness is a BENCHMARK, not a test, for the same reason the
// index package's huge-store harness is (internal/index
// measure_test.go): go-toolchain's test phase enforces a 30s per-test
// budget, and a full run here (minutes of wall time, 10s of it
// deliberate idle) does not fit -- there is no flag to raise that
// budget. The benchmark phase has no such cap and is not
// coverage-instrumented, so the wall times are honest; compare only
// runs captured the same way. The work runs ONCE per invocation
// (b.N is ignored, the gated-bench precedent): the body far exceeds
// any benchtime at b.N=1, so the framework never ramps up.
//
// Knobs (all optional; defaults in the consts below):
//
//	COMPETENT_SEARCH_WATCH_MEASURE_DIRS   synthetic tree size (directories)
//	COMPETENT_SEARCH_WATCH_MEASURE_STORM  storm create+delete op count
//	COMPETENT_SEARCH_WATCH_MEASURE_ROOT   pre-created tree root (default b.TempDir)
//	COMPETENT_SEARCH_WATCH_MEASURE_OUT    report path (writes <out>.json + <out>.txt)
//
// A default run needs ~30k inotify watches and minutes of wall time
// (10s of it deliberate idle), so pass a generous go test -timeout and
// leave /proc/sys/fs/inotify/max_user_watches headroom. An exhausted
// watch budget or an overflowed event queue is REPORTED (dropped
// watches, overflows, degraded flag), never an error: degradation is
// exactly what the harness exists to observe.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// Environment gates and knobs.
const (
	measureEnv      = "COMPETENT_SEARCH_WATCH_MEASURE"       // gate: the harness runs only when set
	measureDirsEnv  = "COMPETENT_SEARCH_WATCH_MEASURE_DIRS"  // synthetic tree directory count
	measureStormEnv = "COMPETENT_SEARCH_WATCH_MEASURE_STORM" // storm create+delete op count
	measureRootEnv  = "COMPETENT_SEARCH_WATCH_MEASURE_ROOT"  // pre-created tree root (else b.TempDir)
	measureOutEnv   = "COMPETENT_SEARCH_WATCH_MEASURE_OUT"   // report file path (<out>.json + <out>.txt)
)

// Synthetic workload shape: buildSynthTree fills the tree breadth-first
// with treeFanout children per directory -- the default 30000 dirs sit
// at depth 1..3 under the root (32 + 1024 + 28944) -- and spreads 2x
// that many empty files evenly over the leaf directories, 2-3 files per
// leaf at the default shape. The storm spreads its create+delete ops
// over stormTargetDirs distinct watched directories.
const (
	defaultTreeDirs   = 30_000
	defaultStormFiles = 20_000
	treeFanout        = 32
	stormTargetDirs   = 200
)

// Stabilization windows. Registration is considered finished once the
// watched+dropped counters have not moved for registerQuiet; the index
// has converged once LiveCount has not moved for convergeQuiet. Both
// are generous on purpose (slow disks), polled every stabilizePoll, and
// there is deliberately NO overall internal timeout -- go test's
// -timeout is the backstop.
const (
	stabilizePoll = 50 * time.Millisecond
	registerQuiet = 2 * time.Second
	convergeQuiet = 3 * time.Second
	idleWindow    = 10 * time.Second
)

// inotifyWatchCost estimates the kernel-side memory of one inotify
// watch. The kernel's own accounting formula is INOTIFY_WATCH_COST =
// sizeof(struct inotify_inode_mark) + 2*sizeof(struct inode)
// (fs/notify/inotify/inotify_user.c), measured 1284 B/watch on x86-64.
// Struct sizes drift by kernel version and config, so the derived bytes
// are an estimate for the report, not exact accounting.
const inotifyWatchCost = 1284

// envInt reads an integer knob from the environment, falling back to
// def when the variable is unset, malformed, or non-positive.
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// rusageSelf returns the process's cumulative user and system CPU time
// in seconds. syscall.Getrusage(RUSAGE_SELF) exists on both linux and
// darwin; the deltas are process-wide, so background goroutines (GC,
// the stabilization polls) are included.
func rusageSelf(tb testing.TB) (user, sys float64) {
	var ru syscall.Rusage
	require.Nil(tb, syscall.Getrusage(syscall.RUSAGE_SELF, &ru))
	return tvSeconds(ru.Utime), tvSeconds(ru.Stime)
}

// tvSeconds converts a syscall.Timeval to seconds. The field widths
// differ by GOOS/GOARCH (int32 vs int64), so convert, never assert.
func tvSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1e6
}

// waitStable polls read every stabilizePoll until the value has not
// changed for quiet, then returns the final value and the time of the
// LAST observed change (phase wall times run to that instant, so the
// quiet window itself is excluded from them). Deliberately no internal
// timeout: on a slow disk the poll just keeps waiting.
func waitStable(read func() int, quiet time.Duration) (final int, lastChange time.Time) {
	last := read()
	lastChange = time.Now()
	for {
		time.Sleep(stabilizePoll)
		if v := read(); v != last {
			last = v
			lastChange = time.Now()
		}
		if time.Since(lastChange) >= quiet {
			return last, lastChange
		}
	}
}

// procInt reads a single integer from a /proc file. Zero on non-linux
// (no /proc) and on any read or parse failure: the report degrades to
// zeros instead of failing the measurement.
func procInt(path string) int {
	if runtime.GOOS != "linux" {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

// kernelVersion returns the running kernel's release string -- the
// uname -r value, read from /proc/sys/kernel/osrelease. Empty on
// non-linux.
func kernelVersion() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// inotifyKernelCounts reports the kernel's own view of this process's
// inotify state via /proc/self/fdinfo: an inotify fd's fdinfo file
// carries one "inotify wd:" line per watch descriptor, so fds is the
// number of inotify instances seen holding watches and wds the total
// watch count -- the cross-check of Stats.WatchedDirs against what the
// kernel actually holds. Zeros on non-linux or any read failure.
func inotifyKernelCounts() (fds, wds int) {
	if runtime.GOOS != "linux" {
		return 0, 0
	}
	entries, err := os.ReadDir("/proc/self/fdinfo")
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", e.Name()))
		if err != nil {
			continue // fd closed between ReadDir and the read
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "inotify wd:") {
				n++
			}
		}
		if n > 0 {
			fds++
			wds += n
		}
	}
	return fds, wds
}

// BenchmarkWatchMeasure is the one env-gated measurement: synthetic
// tree -> index build -> watch registration -> kernel accounting ->
// event storm + convergence -> idle -> teardown, reported like the
// index harness. The work runs once; b.N is deliberately ignored (see
// the file comment). Skips unless COMPETENT_SEARCH_WATCH_MEASURE=1.
func BenchmarkWatchMeasure(b *testing.B) {
	if os.Getenv(measureEnv) == "" {
		b.Skip("set COMPETENT_SEARCH_WATCH_MEASURE=1 to run the watch-layer measurement")
	}
	dirs := envInt(measureDirsEnv, defaultTreeDirs)
	stormN := envInt(measureStormEnv, defaultStormFiles)

	// measureRootEnv lets the caller aim the tree at a specific
	// filesystem (they pre-create the directory and own its cleanup);
	// the default b.TempDir lands wherever TMPDIR points and is removed
	// automatically after the watcher has stopped.
	root := os.Getenv(measureRootEnv)
	if root == "" {
		root = b.TempDir()
	} else {
		fi, err := os.Stat(root)
		require.Nil(b, err)
		require.True(b, fi.IsDir())
	}

	rep := measureReport{
		Label:          fmt.Sprintf("watch layer baseline: %d dirs, %d storm ops (benchmark phase)", dirs, stormN),
		GoVersion:      runtime.Version(),
		NumCPU:         runtime.GOMAXPROCS(0),
		GOOS:           runtime.GOOS,
		KernelVersion:  kernelVersion(),
		MaxUserWatches: procInt("/proc/sys/fs/inotify/max_user_watches"),
	}

	// Phase 1: synthetic tree.
	start := time.Now()
	created, files := buildSynthTree(b, root, dirs)
	rep.TreeDirs = len(created)
	rep.TreeFiles = files
	rep.TreeBuildSeconds = time.Since(start).Seconds()

	// Phase 2: index build.
	mgr := index.NewManager([]string{root}, nil, 0)
	entries, buildDur, err := mgr.BuildFromDisk(context.Background(), nil)
	require.Nil(b, err)
	require.Greater(b, entries, 0)
	rep.IndexEntries = entries
	rep.IndexBuildSeconds = buildDur.Seconds()

	// Phase 3: watch registration -- the headline. Start, then wait for
	// the initial add pass to finish (watched+dropped stable for
	// registerQuiet). The wall number runs Start -> last observed
	// change; the CPU delta also spans the quiet window, which is idle
	// and adds ~nothing.
	w := New(mgr, []string{root}, nil, Options{})
	regUser0, regSys0 := rusageSelf(b)
	regStart := time.Now()
	require.Nil(b, w.Start())
	defer w.Stop() // safety net; the measured Stop below makes this a no-op
	_, regLast := waitStable(func() int {
		s := w.Stats()
		return s.WatchedDirs + s.DroppedWatches
	}, registerQuiet)
	regUser1, regSys1 := rusageSelf(b)
	st := w.Stats()
	rep.RegisterSeconds = regLast.Sub(regStart).Seconds()
	rep.RegisterCPUUserSec = regUser1 - regUser0
	rep.RegisterCPUSysSec = regSys1 - regSys0
	rep.WatchedDirs = st.WatchedDirs
	rep.DroppedWatches = st.DroppedWatches
	rep.Degraded = st.Degraded

	// Phase 4: kernel-side accounting.
	rep.EstimatedKernelBytes = int64(st.WatchedDirs) * inotifyWatchCost
	rep.InotifyFDs, rep.InotifyWatchDescs = inotifyKernelCounts()

	// Phase 5: event storm -- create then delete stormN files spread
	// over the target dirs, as fast as possible, then wait for the
	// index to converge (LiveCount stable for convergeQuiet). Mid-storm
	// flushes index creates for real (the files still exist) and the
	// delete wave tombstones them, so LiveCount should end where it
	// began -- unless the kernel queue overflowed, which the deltas
	// record (no Rescanner is wired here, so nothing reconciles a
	// loss).
	targets := stormTargets(created, stormTargetDirs)
	rep.StormDirs = len(targets)
	rep.StormFiles = stormN
	rep.LiveCountBefore = mgr.LiveCount()
	preStats := w.Stats()
	stormUser0, stormSys0 := rusageSelf(b)
	stormStart := time.Now()
	paths := make([]string, stormN)
	for i := range paths {
		paths[i] = filepath.Join(targets[i%len(targets)], fmt.Sprintf("s%06d", i))
		require.Nil(b, os.WriteFile(paths[i], nil, 0o644))
	}
	for _, p := range paths {
		require.Nil(b, os.Remove(p))
	}
	stormEnd := time.Now()
	rep.StormSeconds = stormEnd.Sub(stormStart).Seconds()
	_, convLast := waitStable(mgr.LiveCount, convergeQuiet)
	stormUser1, stormSys1 := rusageSelf(b)
	postStats := w.Stats()
	rep.ConvergeSeconds = convLast.Sub(stormEnd).Seconds()
	rep.StormCPUUserSec = stormUser1 - stormUser0
	rep.StormCPUSysSec = stormSys1 - stormSys0
	rep.StormOverflowsDelta = postStats.Overflows - preStats.Overflows
	rep.StormDroppedDelta = postStats.DroppedWatches - preStats.DroppedWatches
	rep.DegradedAfterStorm = postStats.Degraded
	rep.LiveCountAfter = mgr.LiveCount()

	// Phase 6: idle cost, watcher live and the tree quiet.
	idleUser0, idleSys0 := rusageSelf(b)
	idleStart := time.Now()
	time.Sleep(idleWindow)
	rep.IdleSeconds = time.Since(idleStart).Seconds()
	idleUser1, idleSys1 := rusageSelf(b)
	rep.IdleCPUUserSec = idleUser1 - idleUser0
	rep.IdleCPUSysSec = idleSys1 - idleSys0

	// Phase 7: teardown.
	stopStart := time.Now()
	w.Stop()
	rep.StopSeconds = time.Since(stopStart).Seconds()

	rep.Notes = []string{
		"wall times captured in go-toolchain's BENCHMARK phase (un-instrumented, same yardstick as internal/index's gated benches); compare only runs captured the same way",
		"RegisterSeconds/ConvergeSeconds run to the LAST observed change; the stabilization quiet windows (2s/3s) are excluded from the wall numbers but included in the CPU deltas",
		"CPU deltas are process-wide getrusage(RUSAGE_SELF): background GC and the stabilization polls are included",
		fmt.Sprintf("EstimatedKernelBytes = WatchedDirs * %d B (kernel INOTIFY_WATCH_COST, x86-64); InotifyWatchDescs is the kernel's own fdinfo count", inotifyWatchCost),
	}
	if runtime.GOOS != "linux" {
		rep.Notes = append(rep.Notes, "kernel-side numbers (kernel version, max_user_watches, fdinfo counts) are zero: /proc is linux-only")
	}
	writeMeasureReport(b, rep)
}
