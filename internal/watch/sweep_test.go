package watch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// backdate pushes a path's mtime well behind any sweep watermark.
func backdate(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
}

func TestSweeperIntervalFires(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "d/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier() // stays silent: only sweeps can find the changes
	w := newTestWatcher(t, m, f)
	startWatcherRegistered(t, w)

	s := newTestSweeper(t, m, w, SweepOptions{Interval: 40 * time.Millisecond})
	startSweeper(t, s)

	inRoot := filepath.Join(root, "swept-root.txt")
	require.NoError(t, os.WriteFile(inRoot, nil, 0o644))
	inDir := filepath.Join(paths["d/"], "swept-nested.txt")
	require.NoError(t, os.WriteFile(inDir, nil, 0o644))

	waitFor(t, func() bool { return hasPath(m, inRoot) }, "the periodic sweep finds a root-level file")
	waitFor(t, func() bool { return hasPath(m, inDir) }, "the periodic sweep finds a nested file")
	require.GreaterOrEqual(t, s.Stats().Completed, 1)
}

func TestSweeperRequestCoalesces(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	s := newTestSweeper(t, m, w, SweepOptions{})
	for i := 0; i < 5; i++ {
		s.Request() // 1-slot queue: five asks collapse into one
	}
	startSweeper(t, s)
	waitFor(t, func() bool { return s.Stats().Completed == 1 }, "queued requests run once")
	require.Never(t, func() bool { return s.Stats().Completed > 1 },
		400*time.Millisecond, 20*time.Millisecond, "no follow-up pass without a new request")
}

func TestSweeperMinGapSpacesRequests(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	s := newTestSweeper(t, m, w, SweepOptions{MinGap: 2 * time.Second})
	startSweeper(t, s)

	s.Request()
	waitFor(t, func() bool { return s.Stats().Completed == 1 }, "first request sweeps immediately")
	s.Request()
	require.Never(t, func() bool { return s.Stats().Completed >= 2 },
		500*time.Millisecond, 20*time.Millisecond, "a back-to-back request waits out MinGap")
	waitFor(t, func() bool { return s.Stats().Completed == 2 }, "the spaced pass still runs")
}

func TestSweeperStopCutsPassMidPage(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "a/", "b/", "c/", "d/")
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	mark := time.Unix(1000, 0)
	// One lstat per second: the 5-dir pass (root + 4) would take ~4s.
	s := newTestSweeper(t, m, w, SweepOptions{StatsPerSec: 1, InitialWatermark: mark})
	require.NoError(t, s.Start())

	s.Request()
	waitFor(t, func() bool { return s.Stats().Running }, "a pass is in flight")
	start := time.Now()
	s.Stop() // must abort inside the throttle sleep / between dirs
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*time.Second, "Stop cuts the pass, not waits it out")
	st := s.Stats()
	require.Equal(t, 1, st.Cancelled)
	require.Zero(t, st.Completed)
	require.False(t, st.Running)
	// The watermark is untouched by a cancelled pass, so the next pass
	// would redo the whole window. (Safe to read: the loop has exited.)
	require.True(t, s.watermark.Equal(mark), "a partial pass never advances the watermark")
}

func TestSweeperStopCutsMinGapWait(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	s := newTestSweeper(t, m, w, SweepOptions{MinGap: time.Hour})
	require.NoError(t, s.Start())
	s.Request()
	waitFor(t, func() bool { return s.Stats().Completed == 1 }, "first pass")
	s.Request() // the loop now sits in the MinGap wait
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	s.Stop() // must interrupt the wait, not sleep the hour out
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, 1, s.Stats().Completed)
}

func TestSweeperLifecycle(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	w := newTestWatcher(t, m, newFakeNotifier())

	require.Panics(t, func() { NewSweeper(m, nil, SweepOptions{}) }, "a Sweeper without its reconcile engine is a programming error")

	s := NewSweeper(m, w, SweepOptions{})
	require.Equal(t, defaultSweepInterval, s.opt.Interval, "zero Interval selects the 20m default")
	require.Equal(t, defaultSweepMinGap, s.opt.MinGap, "zero MinGap selects the 1m default")
	require.Equal(t, defaultSweepStatsPerSec, s.opt.StatsPerSec, "zero StatsPerSec selects the default")
	require.NotNil(t, s.opt.mounts, "the production mount lister is wired by default")
	require.NoError(t, s.Start())
	require.Error(t, s.Start(), "double start")
	s.Stop()
	s.Stop() // idempotent
	require.Error(t, s.Start(), "start after stop")

	fresh := NewSweeper(m, w, SweepOptions{})
	fresh.Stop() // stop before start is a no-op
	require.False(t, fresh.Stats().Running)
}

// TestSweeperWatermarkIncremental pins the watermark contract: a zero
// watermark re-lists every directory; a completed pass advances the
// watermark so an untouched tree re-lists nothing; and a touched
// directory is re-listed, sweeping away a silent child deletion.
func TestSweeperWatermarkIncremental(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "d1/", "d1/f1.txt", "d2/", "d2/f2.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier() // silent: the sweeps do all the work
	w := newTestWatcher(t, m, f)
	startWatcherRegistered(t, w)

	s := newTestSweeper(t, m, w, SweepOptions{}) // zero InitialWatermark: first pass re-lists all
	startSweeper(t, s)

	sweepOnce(t, s)
	st := s.Stats()
	require.Equal(t, 3, st.Swept, "root + d1 + d2 examined")
	require.Equal(t, 3, st.Relisted, "the zero watermark re-lists every directory")

	// Untouched tree: push every dir's mtime behind the watermark and
	// the next pass re-lists nothing.
	backdate(t, root)
	backdate(t, paths["d1/"])
	backdate(t, paths["d2/"])
	sweepOnce(t, s)
	st = s.Stats()
	require.Equal(t, 3, st.Swept)
	require.Zero(t, st.Relisted, "an untouched tree is only stat'd, never re-listed")

	// A silent deletion bumps d1's mtime: only d1 is re-listed, and
	// the vanished child is swept out of the index.
	require.NoError(t, os.Remove(paths["d1/f1.txt"]))
	sweepOnce(t, s)
	st = s.Stats()
	require.Equal(t, 1, st.Relisted, "only the touched directory is re-listed")
	require.False(t, hasPath(m, paths["d1/f1.txt"]), "the silent deletion converged")
	require.True(t, hasPath(m, paths["d2/f2.txt"]), "untouched content untouched")
}

// TestSweeperBackdatedMutationNeedsFullRelist pins the DOCUMENTED
// limitation: a mutation whose directory mtime is backdated behind the
// watermark (tar --preserve style) hides from the incremental pass --
// and converges on a full re-list (a fresh zero-watermark sweeper,
// standing in for the deep rescan).
func TestSweeperBackdatedMutationNeedsFullRelist(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "d/", "d/f.txt")
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	// The mutation, then the backdate that hides it.
	hidden := filepath.Join(paths["d/"], "hidden.txt")
	require.NoError(t, os.WriteFile(hidden, nil, 0o644))
	backdate(t, paths["d/"])

	inc := newTestSweeper(t, m, w, SweepOptions{InitialWatermark: time.Now()})
	startSweeper(t, inc)
	sweepOnce(t, inc)
	require.False(t, hasPath(m, hidden), "the incremental pass misses the backdated mutation (documented)")
	inc.Stop()

	full := newTestSweeper(t, m, w, SweepOptions{}) // zero watermark: full re-list
	startSweeper(t, full)
	sweepOnce(t, full)
	require.True(t, hasPath(m, hidden), "a full re-list converges the backdated mutation")
}

// TestSweeperMountDiffForceDirty drives the mount phase through a
// scripted mount lister: a mountpoint appearing under a root is
// reconciled despite an old mtime (mount-onto-existing-dir moves no
// mtime), and a disappearing one has its children swept away.
func TestSweeperMountDiffForceDirty(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "mnt/", "mnt/data.txt")
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	var mu sync.Mutex
	mounts := []string{}
	lister := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), mounts...)
	}
	setMounts := func(ms ...string) {
		mu.Lock()
		defer mu.Unlock()
		mounts = ms
	}

	// "Mount" appears: new content shows up under mnt while mnt's
	// mtime stays old -- only the mount diff can notice.
	mounted := filepath.Join(paths["mnt/"], "mounted.txt")
	require.NoError(t, os.WriteFile(mounted, nil, 0o644))
	backdate(t, paths["mnt/"])
	setMounts(paths["mnt/"])

	s := newTestSweeper(t, m, w, SweepOptions{InitialWatermark: time.Now(), mounts: lister})
	startSweeper(t, s)
	sweepOnce(t, s)
	require.True(t, hasPath(m, mounted), "an appearing mountpoint is force-reconciled despite its old mtime")

	// "Unmount": the mountpoint's content vanishes from disk, again
	// without an mtime the incremental pass would notice.
	require.NoError(t, os.Remove(mounted))
	require.NoError(t, os.Remove(paths["mnt/data.txt"]))
	backdate(t, paths["mnt/"])
	setMounts()

	sweepOnce(t, s)
	require.False(t, hasPath(m, mounted), "a vanishing mountpoint's children are swept away")
	require.False(t, hasPath(m, paths["mnt/data.txt"]))
	require.True(t, hasPath(m, paths["mnt/"]), "the mountpoint directory itself stays indexed")
}

// TestOverflowDegradationSweepReconcilesEndToEnd is the sweep-path
// port of the overflow recovery contract: overflow -> degraded ->
// sweep request -> the lost changes appear, with NO Rescanner in the
// picture at all.
func TestOverflowDegradationSweepReconcilesEndToEnd(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcherRegistered(t, w)
	s := newTestSweeper(t, m, w, SweepOptions{})
	startSweeper(t, s)

	// The index goes stale invisibly (no events for these), then the
	// kernel reports an overflow: degraded -> sweep request -> the
	// shallow passes recover the loss and promote the new dir.
	paths := mkTree(t, root, "lost/", "lost/during-overflow.txt")
	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool { return w.Degraded() }, "overflow degrades the watcher")
	waitFor(t, func() bool { return hasPath(m, paths["lost/during-overflow.txt"]) }, "the sweep recovers lost changes")
	waitFor(t, func() bool { return f.has(paths["lost/"]) }, "the discovered dir is promoted into the hot set")
	require.Equal(t, 1, w.Stats().Overflows)
	require.GreaterOrEqual(t, s.Stats().Completed, 1)
}
