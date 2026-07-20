package watch

// The deferred-start (StartDeferred/Release) tests: the pre-build
// registration mode the app's startup ordering rides -- the backend is
// armed BEFORE the initial index walk, events collect while held, and
// Release runs the registration pass plus the held replay against the
// freshly built index. The scripted fakeNotifier keeps every
// interleaving deterministic; the shared helpers live in
// helpers_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// TestDeferredMidBuildEventsReachFinalIndex is the core ordering gate:
// changes that land while the initial build walks -- after the walker
// already passed their directories, so the walk itself cannot see them
// -- are held by the deferred watcher and applied once it is released
// against the swapped-in store. Final index state == on-disk truth.
func TestDeferredMidBuildEventsReachFinalIndex(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "sub/", "old.txt")
	m := index.NewManager([]string{root}, nil, 500)

	// The app's ordering: the watcher is armed BEFORE the first walk.
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	require.NoError(t, w.StartDeferred())
	t.Cleanup(w.Stop)

	// "The initial build": walks the pre-change disk state.
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, hasPath(m, paths["old.txt"]))

	// Mid-build changes the walk missed: one deletion, one creation in
	// the root, one creation under an indexed subdirectory. Their
	// events arrive while held.
	created := filepath.Join(root, "during-build.txt")
	nested := filepath.Join(paths["sub/"], "nested-during-build.txt")
	require.NoError(t, os.Remove(paths["old.txt"]))
	require.NoError(t, os.WriteFile(created, nil, 0o644))
	require.NoError(t, os.WriteFile(nested, nil, 0o644))
	f.send(fsnotify.Remove, paths["old.txt"])
	f.send(fsnotify.Create, created)
	f.send(fsnotify.Create, nested)
	waitFor(t, func() bool { return len(f.events) == 0 }, "the hold loop drains the notifier")

	// Held means HELD: nothing is applied before Release, well past the
	// fastOptions debounce thresholds.
	require.Never(t, func() bool {
		return hasPath(m, created) || hasPath(m, nested) || !hasPath(m, paths["old.txt"])
	}, 300*time.Millisecond, 20*time.Millisecond, "no event may apply while the watcher is held")

	// The app wires the trio before releasing; a clean hold must not
	// ask for any sweep.
	sweeps := make(chan struct{}, 4)
	w.setSweepRequester(func() { sweeps <- struct{}{} })
	w.Release()
	<-w.InitialRegistration()

	waitFor(t, func() bool { return hasPath(m, created) }, "a mid-build creation applies at release")
	waitFor(t, func() bool { return hasPath(m, nested) }, "a mid-build nested creation applies at release")
	waitFor(t, func() bool { return !hasPath(m, paths["old.txt"]) }, "a mid-build deletion applies at release")
	select {
	case <-sweeps:
		t.Fatal("a loss-free hold must not request a sweep")
	default:
	}
	require.False(t, w.Degraded(), "a loss-free hold never degrades")

	// The watcher is fully live after release: ordinary events apply.
	settle(t, m, f, root)
}

// TestDeferredStartWatchesRootsImmediately pins the per-directory
// model's pre-build coverage: the configured roots are watched from
// StartDeferred (they exist before any index does), while everything
// else waits for the release-time fill.
func TestDeferredStartWatchesRootsImmediately(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "docs/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	require.NoError(t, w.StartDeferred())
	t.Cleanup(w.Stop)

	require.True(t, f.has(root), "the root is watched before any build/release")
	require.False(t, f.has(paths["docs/"]), "non-root dirs wait for the release-time fill")
	require.Equal(t, 1, w.Stats().WatchedDirs)

	w.Release()
	<-w.InitialRegistration()
	waitFor(t, func() bool { return f.has(paths["docs/"]) }, "the fill runs at release")
}

// TestDeferredHoldCapLossDegradesAndSweeps pins the bounded hold: new
// paths beyond holdCap are dropped, the loss degrades the watcher like
// an overflow, and one reconcile sweep is requested at release so the
// dropped changes still converge.
func TestDeferredHoldCapLossDegradesAndSweeps(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	w.holdCap = 2 // the test knob; StartDeferred keeps explicit values
	require.NoError(t, w.StartDeferred())
	t.Cleanup(w.Stop)

	kept1 := filepath.Join(root, "kept-1.txt")
	kept2 := filepath.Join(root, "kept-2.txt")
	dropped := filepath.Join(root, "dropped.txt")
	for _, p := range []string{kept1, kept2, dropped} {
		require.NoError(t, os.WriteFile(p, nil, 0o644))
	}
	f.send(fsnotify.Create, kept1)
	f.send(fsnotify.Create, kept2)
	f.send(fsnotify.Create, kept2) // re-marking a pending path is never a loss
	f.send(fsnotify.Create, dropped)
	// All four sends must be HELD before releasing (a straggler picked
	// up by the released loop would apply normally instead of counting
	// as hold loss); once the channel is drained, the loop finishes the
	// in-flight holdAdd before it can observe the release.
	waitFor(t, func() bool { return len(f.events) == 0 }, "the hold loop drains the notifier")

	sweeps := make(chan struct{}, 4)
	w.setSweepRequester(func() { sweeps <- struct{}{} })
	w.Release()
	<-w.InitialRegistration()

	waitFor(t, func() bool { return hasPath(m, kept1) && hasPath(m, kept2) },
		"paths within the cap apply at release")
	select {
	case <-sweeps:
	case <-time.After(20 * time.Second):
		t.Fatal("hold-cap loss must request a reconcile sweep at release")
	}
	waitFor(t, func() bool { return w.Stats().Overflows == 1 }, "the loss counts as an overflow")
	require.True(t, w.Degraded(), "dropped hold paths degrade the watcher")
}

// TestDeferredKernelOverflowDuringHoldSweepsAtRelease: an event-queue
// overflow that fires while held -- before any sweeper exists to ask
// -- is re-kicked at release, once the requesters are wired.
func TestDeferredKernelOverflowDuringHoldSweepsAtRelease(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	require.NoError(t, w.StartDeferred())
	t.Cleanup(w.Stop)

	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool { return w.Stats().Overflows == 1 }, "the overflow is counted while held")

	sweeps := make(chan struct{}, 4)
	w.setSweepRequester(func() { sweeps <- struct{}{} })
	w.Release()
	<-w.InitialRegistration()
	select {
	case <-sweeps:
	case <-time.After(20 * time.Second):
		t.Fatal("an overflow during the hold must be swept once a sweeper is wired")
	}
}

// TestDeferredStopWithoutRelease: Stop tears a still-held watcher down
// cleanly -- the loop exits, InitialRegistration unblocks, and nothing
// held is applied (a cancelled build's sweeps/rescans never run, and
// the partial store was discarded anyway).
func TestDeferredStopWithoutRelease(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	require.NoError(t, w.StartDeferred())

	held := filepath.Join(root, "held.txt")
	require.NoError(t, os.WriteFile(held, nil, 0o644))
	f.send(fsnotify.Create, held)

	w.Stop()
	select {
	case <-w.InitialRegistration():
	case <-time.After(20 * time.Second):
		t.Fatal("Stop must unblock InitialRegistration waiters on a held watcher")
	}
	require.False(t, hasPath(m, held), "a stopped hold applies nothing")
	w.Release() // after Stop: a harmless no-op
	w.Stop()    // idempotent
}

// TestReleaseIsNoOpOnPlainStart: Release on a watcher started with
// plain Start does nothing (nil release channel), and normal event
// application is unaffected.
func TestReleaseIsNoOpOnPlainStart(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcherRegistered(t, w)

	w.Release()
	settle(t, m, f, root)
}
