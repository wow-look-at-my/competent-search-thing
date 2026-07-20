package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

// measureReport is the structured output of the run, written as JSON
// (plus a rendered text sibling) when $COMPETENT_SEARCH_WATCH_MEASURE_OUT
// is set, and always t.Log-ed. Field names are the JSON keys.
type measureReport struct {
	Label     string
	GoVersion string
	NumCPU    int
	GOOS      string

	// Environment (linux /proc reads; zero values elsewhere).
	KernelVersion  string
	MaxUserWatches int

	// Phase 1: synthetic tree build.
	TreeDirs         int
	TreeFiles        int
	TreeBuildSeconds float64

	// Phase 2: index build over the tree.
	IndexEntries      int
	IndexBuildSeconds float64

	// Phase 3: watch registration (Start -> stats stable).
	RegisterSeconds    float64
	RegisterCPUUserSec float64
	RegisterCPUSysSec  float64
	WatchedDirs        int
	DroppedWatches     int
	Degraded           bool

	// Phase 4: kernel-side accounting (linux; zeros elsewhere).
	EstimatedKernelBytes int64
	InotifyFDs           int
	InotifyWatchDescs    int

	// Phase 5: event storm and convergence.
	StormFiles          int
	StormDirs           int
	StormSeconds        float64
	ConvergeSeconds     float64
	StormCPUUserSec     float64
	StormCPUSysSec      float64
	StormOverflowsDelta int
	StormDroppedDelta   int
	DegradedAfterStorm  bool
	LiveCountBefore     int
	LiveCountAfter      int

	// Phase 6: idle cost with the watcher live.
	IdleSeconds    float64
	IdleCPUUserSec float64
	IdleCPUSysSec  float64

	// Phase 7: teardown.
	StopSeconds float64

	Notes []string `json:",omitempty"`
}

// text renders the human-readable form of the report.
func (r measureReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s ==\n", r.Label)
	fmt.Fprintf(&b, "%s, GOMAXPROCS %d, %s", r.GoVersion, r.NumCPU, r.GOOS)
	if r.KernelVersion != "" {
		fmt.Fprintf(&b, ", kernel %s", r.KernelVersion)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "tree:     %d dirs + %d files in %.2fs\n", r.TreeDirs, r.TreeFiles, r.TreeBuildSeconds)
	fmt.Fprintf(&b, "index:    %d entries in %.2fs\n", r.IndexEntries, r.IndexBuildSeconds)
	fmt.Fprintf(&b, "register: %d watched, %d dropped, degraded %v; %.2fs wall, cpu %.2fs user + %.2fs sys\n",
		r.WatchedDirs, r.DroppedWatches, r.Degraded, r.RegisterSeconds, r.RegisterCPUUserSec, r.RegisterCPUSysSec)
	fmt.Fprintf(&b, "kernel:   max_user_watches %d; est %.1f MB (%d watches x %d B); fdinfo sees %d inotify fds, %d wds\n",
		r.MaxUserWatches, mb(uint64(r.EstimatedKernelBytes)), r.WatchedDirs, inotifyWatchCost, r.InotifyFDs, r.InotifyWatchDescs)
	fmt.Fprintf(&b, "storm:    %d files created+deleted across %d dirs in %.2fs; converged %.2fs after last op; cpu %.2fs user + %.2fs sys\n",
		r.StormFiles, r.StormDirs, r.StormSeconds, r.ConvergeSeconds, r.StormCPUUserSec, r.StormCPUSysSec)
	fmt.Fprintf(&b, "          stats delta: overflows +%d, dropped +%d, degraded %v; live %d -> %d\n",
		r.StormOverflowsDelta, r.StormDroppedDelta, r.DegradedAfterStorm, r.LiveCountBefore, r.LiveCountAfter)
	fmt.Fprintf(&b, "idle:     %.2fs cost cpu %.3fs user + %.3fs sys\n", r.IdleSeconds, r.IdleCPUUserSec, r.IdleCPUSysSec)
	fmt.Fprintf(&b, "stop:     %.3fs\n", r.StopSeconds)
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

// writeMeasureReport logs the text form and, when
// $COMPETENT_SEARCH_WATCH_MEASURE_OUT is set, writes the JSON to
// <out>.json plus the text to <out>.txt.
func writeMeasureReport(tb testing.TB, rep measureReport) {
	text := rep.text()
	tb.Log("\n" + text)
	out := os.Getenv(measureOutEnv)
	if out == "" {
		return
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	require.Nil(tb, err)
	require.Nil(tb, os.WriteFile(out+".json", data, 0o644))
	require.Nil(tb, os.WriteFile(out+".txt", []byte(text), 0o644))
	tb.Logf("measure: wrote %s.json and %s.txt", out, out)
}

// buildSynthTree creates exactly dirs directories under root,
// breadth-first with treeFanout children per directory, then spreads
// 2*dirs empty files evenly across the LEAF directories (the ones that
// received no children). Returns the created directories in creation
// order (root excluded) and the number of files written. The tree
// locations must not already exist: a collision fails loudly rather
// than silently skewing the measurement, so a reused measureRootEnv
// directory has to start out empty.
func buildSynthTree(tb testing.TB, root string, dirs int) ([]string, int) {
	all := make([]string, 1, dirs+1)
	all[0] = root
	lastParent := 0
	for i := 0; len(all)-1 < dirs; i++ {
		lastParent = i
		for f := 0; f < treeFanout && len(all)-1 < dirs; f++ {
			d := filepath.Join(all[i], fmt.Sprintf("d%06d", len(all)-1))
			require.Nil(tb, os.Mkdir(d, 0o755))
			all = append(all, d)
		}
	}
	leaves := all[lastParent+1:]
	total := 2 * dirs
	base, rem := total/len(leaves), total%len(leaves)
	files := 0
	for i, leaf := range leaves {
		n := base
		if i < rem {
			n++
		}
		for j := 0; j < n; j++ {
			require.Nil(tb, os.WriteFile(filepath.Join(leaf, fmt.Sprintf("f%06d", files)), nil, 0o644))
			files++
		}
	}
	return all[1:], files
}

// stormTargets picks up to want directories spread evenly across the
// created tree (every stride-th dir in creation order), so the storm
// hits many distinct watches instead of hammering one directory.
func stormTargets(dirs []string, want int) []string {
	if want > len(dirs) {
		want = len(dirs)
	}
	stride := len(dirs) / want
	out := make([]string, 0, want)
	for i := 0; i < want; i++ {
		out = append(out, dirs[i*stride])
	}
	return out
}
