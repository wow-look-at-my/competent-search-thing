package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

func TestWatcherInitialWatchSet(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "docs/", "docs/inner/", "src/", "a.txt", "node_modules/")
	m := buildManager(t, root, []string{"node_modules"})
	// Simulate drift: an excluded directory that is somehow live in the
	// index must STILL never be watched.
	require.NoError(t, m.Add(root, "node_modules", true))

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	waitFor(t, func() bool { return w.Stats().WatchedDirs == 4 }, "root + docs + docs/inner + src")
	require.True(t, f.has(root), "the root itself is watched")
	require.True(t, f.has(paths["docs/"]))
	require.True(t, f.has(paths["docs/inner/"]))
	require.True(t, f.has(paths["src/"]))
	require.False(t, f.has(paths["node_modules/"]), "excluded dirs are never watched")
	require.False(t, w.Degraded())
}

func TestWatcherExcludedRootIsNotWatched(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "skipme")
	require.NoError(t, os.Mkdir(root, 0o755))
	paths := mkTree(t, root, "sub/", "sub/f.txt")
	m := buildManager(t, root, []string{"skipme"})
	// The walker checks only CHILDREN against excludes, so sub is live.
	require.True(t, hasPath(m, paths["sub/"]))

	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 1 }, "only sub gets a watch")
	require.False(t, f.has(root), "a root matching its own exclude list is not watched")
	require.True(t, f.has(paths["sub/"]))
}

func TestWatcherWatchExcludedDirIndexedSweptNeverWatched(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "hot/", "churn/", "churn/sub/")
	m := buildManager(t, root, nil)

	watchEx, err := index.NewExcluder([]string{"churn"})
	require.NoError(t, err)
	opt := fastOptions()
	opt.WatchEx = watchEx
	f := newFakeNotifier()
	w := New(m, m.Roots(), nil, opt)
	w.newNotifier = func() (notifier, error) { return f, nil }
	startWatcherRegistered(t, w)

	// The fill: the excluded dir AND its subtree get no watches and no
	// desired-set slot, while the index keeps all of them.
	require.True(t, hasPath(m, paths["churn/"]), "watch-excluded dirs stay indexed")
	require.True(t, hasPath(m, paths["churn/sub/"]))
	require.True(t, f.has(root))
	require.True(t, f.has(paths["hot/"]))
	require.False(t, f.has(paths["churn/"]), "watch-excluded dirs are never watched at the fill")
	require.False(t, f.has(paths["churn/sub/"]), "a watch-exclude match covers its whole subtree")
	st := w.Stats()
	require.Equal(t, 2, st.WatchedDirs, "root + hot only")
	require.Equal(t, 2, st.IndexedDirs, "watch-excluded dirs are not part of the desired watch set")
	require.Zero(t, st.DroppedWatches, "withheld watches are never counted as drops")

	// Events inside the subtree (a wideCoverage backend would deliver
	// them) still reconcile into the index -- but promote no watch.
	inChurn := filepath.Join(paths["churn/"], "seen.txt")
	require.NoError(t, os.WriteFile(inChurn, nil, 0o644))
	f.send(fsnotify.Create, inChurn)
	waitFor(t, func() bool { return hasPath(m, inChurn) }, "events under a watch-excluded dir still index")
	f.send(fsnotify.Create, paths["churn/"]) // a dirty excluded DIR reconciles without a refreshWatch
	settle(t, m, f, root)
	require.False(t, f.has(paths["churn/"]), "reconcile never arms a watch on a watch-excluded dir")

	// The sweep tier: changes nobody watched or reported converge, and
	// sweep promotion respects the exclusion too.
	s := newTestSweeper(t, m, w, SweepOptions{})
	startSweeper(t, s)
	swept := filepath.Join(paths["churn/sub/"], "swept.txt")
	require.NoError(t, os.WriteFile(swept, nil, 0o644))
	s.Request()
	waitFor(t, func() bool { return hasPath(m, swept) }, "sweeps converge watch-excluded subtrees")
	require.False(t, f.has(paths["churn/"]), "sweep promotion skips watch-excluded dirs")
	require.False(t, f.has(paths["churn/sub/"]))
}

func TestWatcherDroppedWatchesDegrade(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "limit-a/", "limit-b/", "ok/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	f.addErr = func(path string) error {
		if strings.HasPrefix(filepath.Base(path), "limit-") {
			return errors.New("inotify: no space left on device")
		}
		return nil
	}
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	waitFor(t, func() bool {
		s := w.Stats()
		return s.DroppedWatches == 2 && s.WatchedDirs == 2
	}, "two drops (limit-a, limit-b), two live watches (root, ok)")
	require.True(t, w.Degraded())
	require.False(t, f.has(paths["limit-a/"]))
	require.True(t, f.has(paths["ok/"]))

	// The loop keeps applying events after degradation.
	settle(t, m, f, root)
}

func TestWatcherOverflowRequestsSweepWhenWired(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	rescans := make(chan struct{}, 8)
	w.setRescanRequester(func() { rescans <- struct{}{} })
	sweeps := make(chan struct{}, 8)
	w.setSweepRequester(func() { sweeps <- struct{}{} })
	startWatcher(t, w)

	f.errs <- fsnotify.ErrEventOverflow
	f.errs <- fmt.Errorf("wrapped: %w", fsnotify.ErrEventOverflow)
	waitFor(t, func() bool { return w.Stats().Overflows == 2 }, "both overflows counted (wrapped included)")
	require.True(t, w.Degraded())
	<-sweeps
	<-sweeps
	select {
	case <-rescans:
		t.Fatal("with a sweeper wired, overflow must never fall back to a full rescan")
	default:
	}
	require.Contains(t, buf.String(), "watch: event queue overflow, events lost (degraded); requesting reconcile sweep")

	// Non-overflow errors are logged and the loop keeps running.
	f.errs <- errors.New("some transient watcher error")
	settle(t, m, f, root)
	require.Equal(t, 2, w.Stats().Overflows)
}

func TestWatcherOverflowFallsBackToRescanRequest(t *testing.T) {
	// No sweeper wired: standalone watcher+rescanner setups keep the
	// old overflow -> rescan behavior.
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	requested := make(chan struct{}, 8)
	w.setRescanRequester(func() { requested <- struct{}{} })
	startWatcher(t, w)

	f.errs <- fsnotify.ErrEventOverflow
	f.errs <- fmt.Errorf("wrapped: %w", fsnotify.ErrEventOverflow)
	waitFor(t, func() bool { return w.Stats().Overflows == 2 }, "both overflows counted (wrapped included)")
	require.True(t, w.Degraded())
	<-requested
	<-requested
}

func TestWatcherLifecycle(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	// Stop before Start is a no-op; Start after Stop fails.
	pre := newTestWatcher(t, m, newFakeNotifier())
	pre.Stop()
	pre.Stop()
	require.Error(t, pre.Start(), "start after stop")

	w := newTestWatcher(t, m, newFakeNotifier())
	require.NoError(t, w.Start())
	require.Error(t, w.Start(), "double start")
	w.Stop()
	w.Stop() // idempotent
	select {
	case <-w.lc.done:
	default:
		t.Fatal("run loop still alive after Stop returned")
	}

	// Notifier construction failure surfaces from Start; Stop still
	// works afterwards.
	bad := newTestWatcher(t, m, nil)
	bad.newNotifier = func() (notifier, error) { return nil, errors.New("inotify instances exhausted") }
	require.Error(t, bad.Start())
	bad.Stop()
}

func TestWatcherStopInterruptsInitialAdds(t *testing.T) {
	root := t.TempDir()
	var layout []string
	for i := 0; i < 60; i++ {
		layout = append(layout, fmt.Sprintf("dir%02d/", i))
	}
	mkTree(t, root, layout...)
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	f.addDelay = 10 * time.Millisecond // 61 dirs: >600ms if left alone
	w := newTestWatcher(t, m, f)
	require.NoError(t, w.Start())

	time.Sleep(30 * time.Millisecond) // let a few adds land
	w.Stop()
	require.Less(t, w.Stats().WatchedDirs, 61, "Stop interrupted the registration pass")
}

func TestSyncWatchesReconciles(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "stays/", "vanishes/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)

	// Before Start, syncWatches is a guarded no-op.
	w.syncWatches(context.Background())
	require.Equal(t, 0, w.Stats().WatchedDirs)

	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 3 }, "initial watches")

	// Out-of-band drift: one dir vanishes, one appears. After a rebuild
	// swaps the fresh store in, syncWatches reconciles the watch set.
	require.NoError(t, os.RemoveAll(paths["vanishes/"]))
	fresh := filepath.Join(root, "appears")
	require.NoError(t, os.Mkdir(fresh, 0o755))
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)

	w.syncWatches(context.Background())
	require.True(t, f.has(fresh), "new live dir gains a watch")
	require.False(t, f.has(paths["vanishes/"]), "vanished dir loses its watch")
	require.True(t, f.has(paths["stays/"]))
	require.Equal(t, 3, w.Stats().WatchedDirs)

	// After Stop it degrades to a no-op again.
	w.Stop()
	w.syncWatches(context.Background())
}
