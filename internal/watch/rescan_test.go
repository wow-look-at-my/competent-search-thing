package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

func TestRescannerRequestRebuildsAndSyncsWatcher(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	r := NewRescanner(m, w, RescanOptions{MinGap: time.Millisecond})
	startWatcher(t, w)
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	// Out-of-band changes the watcher never hears about (the fake
	// notifier stays silent): a requested rescan must pick them up and
	// watch the new directory.
	paths := mkTree(t, root, "offband/", "offband/found.txt")
	r.Request()
	waitFor(t, func() bool { return hasPath(m, paths["offband/found.txt"]) }, "rescan indexes out-of-band files")
	waitFor(t, func() bool { return f.has(paths["offband/"]) }, "rescan resyncs the watch set")
	s := r.Stats()
	require.GreaterOrEqual(t, s.Completed, 1)
	require.Equal(t, 0, s.Failed)
}

func TestRescannerMinGapSpacesRequests(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	r := NewRescanner(m, nil, RescanOptions{MinGap: 2 * time.Second})
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	r.Request()
	waitFor(t, func() bool { return r.Stats().Completed == 1 }, "first request rescans immediately")
	r.Request()
	require.Never(t, func() bool { return r.Stats().Completed >= 2 },
		500*time.Millisecond, 20*time.Millisecond,
		"a back-to-back request waits out MinGap")
	waitFor(t, func() bool { return r.Stats().Completed == 2 }, "the spaced rescan still runs")
}

func TestRescannerIntervalTicker(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	r := NewRescanner(m, nil, RescanOptions{Interval: 40 * time.Millisecond, MinGap: time.Millisecond})
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	p := filepath.Join(root, "ticked.txt")
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, p) }, "periodic rescan picks up new files without any watcher")
	require.GreaterOrEqual(t, r.Stats().Completed, 1)
}

func TestRescannerCoalescesQueuedRequests(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	r := NewRescanner(m, nil, RescanOptions{MinGap: time.Millisecond})
	for i := 0; i < 5; i++ {
		r.Request() // 1-slot queue: five asks collapse into one
	}
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)
	waitFor(t, func() bool { return r.Stats().Completed == 1 }, "queued requests run once")
	require.Never(t, func() bool { return r.Stats().Completed > 1 },
		400*time.Millisecond, 20*time.Millisecond, "no follow-up rescan without a new request")
}

func TestRescannerFailureKeepsOldIndex(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 50)
	require.NoError(t, m.Add("/pre", "keepme.txt", false))
	r := NewRescanner(m, nil, RescanOptions{MinGap: time.Millisecond})
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	r.Request()
	waitFor(t, func() bool { return r.Stats().Failed == 1 }, "a bad exclude pattern fails the rescan")
	require.True(t, hasPath(m, "/pre/keepme.txt"), "previous store kept on failure")
	require.Equal(t, 0, r.Stats().Completed)
}

func TestRescannerStopCutsMinGapWait(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	r := NewRescanner(m, nil, RescanOptions{MinGap: time.Hour})
	require.NoError(t, r.Start())

	r.Request()
	waitFor(t, func() bool { return r.Stats().Completed == 1 }, "first rescan")
	r.Request() // the loop now sits in the MinGap wait
	time.Sleep(50 * time.Millisecond)
	r.Stop() // must interrupt the wait, not sleep the hour out
	require.Equal(t, 1, r.Stats().Completed)
}

func TestRescannerLifecycle(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	r := NewRescanner(m, nil, RescanOptions{})
	require.Equal(t, defaultMinGap, r.opt.MinGap, "zero MinGap selects the default")
	require.NoError(t, r.Start())
	require.Error(t, r.Start(), "double start")
	r.Stop()
	r.Stop() // idempotent
	require.Error(t, r.Start(), "start after stop")

	fresh := NewRescanner(m, nil, RescanOptions{})
	fresh.Stop() // stop before start is a no-op
	require.False(t, fresh.Stats().Running)
}

func TestOverflowDegradationReconcilesEndToEnd(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	r := NewRescanner(m, w, RescanOptions{MinGap: time.Millisecond})
	startWatcher(t, w)
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	// The index goes stale invisibly (no events for these), then the
	// kernel reports an overflow: degraded -> reconcile request ->
	// fresh store swap -> resynced watches.
	paths := mkTree(t, root, "lost/", "lost/during-overflow.txt")
	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool { return w.Degraded() }, "overflow degrades the watcher")
	waitFor(t, func() bool { return hasPath(m, paths["lost/during-overflow.txt"]) }, "reconcile rescan recovers lost changes")
	waitFor(t, func() bool { return f.has(paths["lost/"]) }, "watch set resynced after the reconcile")
	require.Equal(t, 1, w.Stats().Overflows)
	require.GreaterOrEqual(t, r.Stats().Completed, 1)
}
