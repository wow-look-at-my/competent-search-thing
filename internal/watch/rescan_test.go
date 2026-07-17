package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestRescanSyncWatchesTracksVanishedAndNewDirs(t *testing.T) {
	root := t.TempDir()
	tree := mkTree(t, root, "stays/", "goes/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	r := NewRescanner(m, w, RescanOptions{MinGap: time.Millisecond})
	startWatcher(t, w)
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)
	waitFor(t, func() bool { return f.has(tree["stays/"]) && f.has(tree["goes/"]) },
		"both indexed directories are watched initially")

	// Change what the manager's roots yield ON DISK while the fake
	// notifier stays silent: goes/ vanishes, born/ appears. Only the
	// rescan's rebuild + syncWatches can reconcile the watch set.
	require.NoError(t, os.RemoveAll(tree["goes/"]))
	born := filepath.Join(root, "born")
	require.NoError(t, os.Mkdir(born, 0o755))

	r.Request()
	waitFor(t, func() bool { return f.has(born) }, "the resync registers the appeared directory")
	waitFor(t, func() bool { return !f.has(tree["goes/"]) }, "the resync drops the vanished directory's watch")
	require.True(t, f.has(tree["stays/"]), "surviving directories keep their watch")
	require.True(t, f.has(root), "the root keeps its watch")
	require.False(t, w.Degraded())
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

// TestRescannerStopCancelsInFlightRescan is the fast-quit contract for
// the rebuild half: Stop during an in-flight build must cancel it and
// return promptly (the build seam would otherwise hold the rescan for
// 30s), log the cancellation -- never a completion or a failure -- and
// leave the previous store serving queries.
func TestRescannerStopCancelsInFlightRescan(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/pre", "keepme.txt", false))
	r := NewRescanner(m, nil, RescanOptions{MinGap: time.Millisecond})

	inFlight := make(chan struct{})
	r.build = func(ctx context.Context) (int, time.Duration, error) {
		close(inFlight)
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(30 * time.Second):
			return 0, 0, errors.New("cancellation never arrived")
		}
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	require.NoError(t, r.Start())
	r.Request()
	<-inFlight

	start := time.Now()
	r.Stop()
	elapsed := time.Since(start)

	require.Less(t, elapsed, 5*time.Second, "Stop must cancel the in-flight rescan, not wait it out")
	require.Contains(t, buf.String(), "watch: rescan cancelled", "cancellation is logged as such")
	require.NotContains(t, buf.String(), "rescan complete")
	require.NotContains(t, buf.String(), "rescan failed")
	require.True(t, hasPath(m, "/pre/keepme.txt"), "the previous store still answers queries")
	s := r.Stats()
	require.Equal(t, 0, s.Completed, "a cancelled rescan never counts as completed")
	require.Equal(t, 1, s.Failed, "it counts on the errored side (previous index kept)")
	require.False(t, s.Running)
}

// TestRescannerStopDropsQueuedRequest covers the queued half: a request
// that arrived while a rescan was in flight sits in the 1-slot channel;
// Stop must drop it, not run it.
func TestRescannerStopDropsQueuedRequest(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	r := NewRescanner(m, nil, RescanOptions{MinGap: time.Millisecond})

	var builds atomic.Int32
	inFlight := make(chan struct{})
	r.build = func(ctx context.Context) (int, time.Duration, error) {
		if builds.Add(1) == 1 {
			close(inFlight)
		}
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}

	require.NoError(t, r.Start())
	r.Request()
	<-inFlight
	r.Request() // queued behind the in-flight rescan

	r.Stop() // Stop returns only after the loop exited: builds is final

	require.Equal(t, int32(1), builds.Load(), "the queued request is dropped, never started")
}

// TestRescannerStopCancelsWatchResync is the motivating bug end to end:
// after a successful rebuild the rescan loop resyncs the watch set,
// which on a huge index means minutes of notifier calls, and Stop used
// to wait ALL of them out (the 18.6M-file Ctrl+C hang). Here 200
// out-of-band directories at 20ms per fake watch add would hold Stop
// for ~4s if cancellation did not interrupt the resync between
// directories.
func TestRescannerStopCancelsWatchResync(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	f.addDelay = 20 * time.Millisecond
	w := newTestWatcher(t, m, f)
	r := NewRescanner(m, w, RescanOptions{MinGap: time.Millisecond})
	startWatcher(t, w)
	require.NoError(t, r.Start())
	t.Cleanup(r.Stop)

	var layout []string
	for i := 0; i < 200; i++ {
		layout = append(layout, fmt.Sprintf("late%03d/", i))
	}
	mkTree(t, root, layout...)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	r.Request()
	waitFor(t, func() bool { return w.Stats().WatchedDirs > 1 }, "the post-rebuild resync is in flight")

	start := time.Now()
	r.Stop()
	elapsed := time.Since(start)

	require.Less(t, elapsed, 2*time.Second, "Stop interrupts the watch resync between directories")
	require.Less(t, w.Stats().WatchedDirs, 201, "the resync did not run to completion")
	require.Contains(t, buf.String(), "watch: rescan cancelled", "an aborted resync is not logged as a completion")
	require.NotContains(t, buf.String(), "rescan complete")
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
