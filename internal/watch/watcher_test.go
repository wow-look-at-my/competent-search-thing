package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
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

func TestWatcherCreateFileAndSymlink(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "target/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	file := filepath.Join(root, "fresh.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	f.send(fsnotify.Create, file)

	link := filepath.Join(root, "dirlink")
	require.NoError(t, os.Symlink(filepath.Join(root, "target"), link))
	f.send(fsnotify.Create, link)

	waitFor(t, func() bool { return hasPath(m, file) && hasPath(m, link) }, "file and symlink indexed")
	for _, r := range m.Query("dirlink", 0) {
		if r.Path == link {
			require.False(t, r.IsDir, "a symlink to a directory is indexed as a non-directory, like the walker")
		}
	}
	require.False(t, f.has(link), "symlinks are never watched or descended")

	// A Create whose path vanished before the flush is skipped.
	ghost := filepath.Join(root, "ghost.txt")
	f.send(fsnotify.Create, ghost)
	settle(t, m, f, root)
	require.False(t, hasPath(m, ghost))
}

func TestWatcherCreateDirScansSubtree(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, []string{"skipdir"})
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	badDir := filepath.Join(root, "tree", "unreadable")
	w.readDir = func(dir string) ([]os.DirEntry, error) {
		if dir == badDir {
			return nil, fs.ErrPermission
		}
		return os.ReadDir(dir)
	}
	startWatcher(t, w)

	// Build the subtree on disk WITHOUT events (the fake stays silent),
	// then report only the topmost Create -- as after a coalesced
	// mkdir -p burst.
	paths := mkTree(t, root,
		"tree/", "tree/one.txt", "tree/nested/", "tree/nested/two.txt",
		"tree/skipdir/", "tree/skipdir/hidden.txt",
		"tree/unreadable/", "tree/unreadable/three.txt")
	f.send(fsnotify.Create, paths["tree/"])

	waitFor(t, func() bool {
		return hasPath(m, paths["tree/one.txt"]) && hasPath(m, paths["tree/nested/two.txt"])
	}, "subtree scan indexes nested content")
	require.True(t, f.has(paths["tree/"]))
	require.True(t, f.has(paths["tree/nested/"]))
	require.True(t, f.has(paths["tree/unreadable/"]), "watch lands before the failed read")
	require.False(t, f.has(paths["tree/skipdir/"]), "excluded subdir not watched")
	require.False(t, hasPath(m, paths["tree/skipdir/"]))
	require.False(t, hasPath(m, paths["tree/skipdir/hidden.txt"]))
	require.True(t, hasPath(m, paths["tree/unreadable/"]), "the unreadable dir entry itself is indexed")
	require.False(t, hasPath(m, paths["tree/unreadable/three.txt"]), "unreadable contents skipped, like the walker")
}

func TestWatcherRemoveDirDropsWatchesAndTombstones(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "gone/", "gone/deep/", "gone/deep/f.txt", "keep/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 4 }, "root, gone, gone/deep, keep")

	// Simulate the kernel having auto-dropped the deleted dirs' watches
	// already: the notifier then errors on Remove and the loop must
	// shrug that off.
	require.NoError(t, os.RemoveAll(paths["gone/"]))
	f.unwatch(paths["gone/"])
	f.unwatch(paths["gone/deep/"])
	f.send(fsnotify.Remove, paths["gone/"])

	waitFor(t, func() bool { return !hasPath(m, paths["gone/deep/f.txt"]) }, "subtree tombstoned")
	require.False(t, hasPath(m, paths["gone/"]))
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 2 }, "root and keep remain watched")
	require.True(t, f.has(paths["keep/"]))
	settle(t, m, f, root) // the loop survived ErrNonExistentWatch
}

func TestWatcherRenameOldNameRemoves(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "old-name.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	require.True(t, hasPath(m, paths["old-name.txt"]))
	// Renegotiated for reconcile-by-lstat: the rename must actually
	// happen on disk. The ordered-batch watcher tombstoned on the
	// Rename op alone; reconcile trusts only the disk, so the old
	// name's lstat has to fail for the entry to go (a Rename event for
	// a path that still exists would -- correctly -- keep it).
	require.NoError(t, os.Rename(paths["old-name.txt"], filepath.Join(root, "new-name.txt")))
	f.send(fsnotify.Rename, paths["old-name.txt"])
	waitFor(t, func() bool { return !hasPath(m, paths["old-name.txt"]) }, "rename-from tombstones the old name")
}

func TestWatcherWriteAndChmodIgnored(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	// Write/Chmod never change names: even for a file the index does
	// not know, they must not create an entry.
	unknown := filepath.Join(root, "not-indexed.txt")
	require.NoError(t, os.WriteFile(unknown, nil, 0o644))
	f.send(fsnotify.Write, unknown)
	f.send(fsnotify.Chmod, unknown)
	settle(t, m, f, root)
	require.False(t, hasPath(m, unknown))
}

func TestWatcherExcludedEventsDropped(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, []string{"node_modules", "*.tmp"})
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	paths := mkTree(t, root, "node_modules/", "node_modules/pkg.js", "junk.tmp")
	f.send(fsnotify.Create, paths["node_modules/"])
	f.send(fsnotify.Create, paths["junk.tmp"])
	settle(t, m, f, root)
	require.False(t, hasPath(m, paths["node_modules/"]))
	require.False(t, hasPath(m, paths["junk.tmp"]))
	require.False(t, f.has(paths["node_modules/"]), "excluded dir gained no watch")
	require.Equal(t, 1, w.Stats().WatchedDirs, "still only the root")
}

// TestWatcherBatchOrderConverges pins the convergence contract for
// interleaved bursts. Renegotiated for reconcile-by-lstat: the old
// ordered-batch model reached these outcomes through op-application
// order (Remove tombstones, then Create resurrects, and vice versa);
// under reconcile the two events collapse into ONE dirty path and the
// op order plays no role BY DESIGN -- lstat at flush time decides. The
// scenarios are kept verbatim and the assertions are final-state only.
func TestWatcherBatchOrderConverges(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "victim.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	// delete-then-create ends LIVE: the file is on disk at flush time.
	f.send(fsnotify.Remove, paths["victim.txt"])
	f.send(fsnotify.Create, paths["victim.txt"])
	settle(t, m, f, root)
	require.True(t, hasPath(m, paths["victim.txt"]))

	// create-then-delete ends DELETED: the path is gone at flush time.
	flash := filepath.Join(root, "flash.txt")
	require.NoError(t, os.WriteFile(flash, nil, 0o644))
	f.send(fsnotify.Create, flash)
	require.NoError(t, os.Remove(flash))
	f.send(fsnotify.Remove, flash)
	settle(t, m, f, root)
	require.False(t, hasPath(m, flash))
}

// TestWatcherMergedLifecycleConverges drives the whole point of the
// dirty-path model: opposing events on ONE path pend as ONE dirty
// entry (the debouncer dedups), and the single flush settles it to the
// on-disk truth.
func TestWatcherMergedLifecycleConverges(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)

	// create+delete of the same path within one debounce window: the
	// merged dirty path reconciles to absent, without a panic.
	flash := filepath.Join(root, "flash.txt")
	require.NoError(t, os.WriteFile(flash, nil, 0o644))
	f.send(fsnotify.Create, flash)
	require.NoError(t, os.Remove(flash))
	f.send(fsnotify.Remove, flash)
	settle(t, m, f, root)
	require.False(t, hasPath(m, flash))

	// delete+recreate-AS-A-DIRECTORY of a live file path, again within
	// one window: the merged dirty path must end as a live dir with
	// its subtree indexed (the entry's dir bit flips in place).
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.WriteFile(victim, nil, 0o644))
	f.send(fsnotify.Create, victim)
	settle(t, m, f, root)
	require.True(t, hasPath(m, victim))

	require.NoError(t, os.Remove(victim))
	f.send(fsnotify.Remove, victim)
	require.NoError(t, os.Mkdir(victim, 0o755))
	inner := filepath.Join(victim, "inner.txt")
	require.NoError(t, os.WriteFile(inner, nil, 0o644))
	f.send(fsnotify.Create, victim)
	settle(t, m, f, root)

	require.True(t, hasPath(m, victim))
	require.True(t, hasPath(m, inner), "recreated-as-dir subtree indexed")
	for _, r := range m.Query("victim", 0) {
		if r.Path == victim {
			require.True(t, r.IsDir, "the surviving entry carries the dir bit")
		}
	}
	require.True(t, f.has(victim), "the recreated dir is watched")
}

// TestWatcherRecreatedDirRegainsWatch pins the refreshWatch half of
// the reconcile contract: deleting a directory and recreating the SAME
// path within one debounce window merges into one dirty path, so no
// Remove is ever applied -- yet the kernel watch died with the old
// inode. The dirty-dir reconcile must re-issue the notifier Add.
func TestWatcherRecreatedDirRegainsWatch(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "d/", "d/old.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 2 }, "root and d watched initially")

	require.NoError(t, os.RemoveAll(paths["d/"]))
	f.unwatch(paths["d/"]) // the kernel dropped the watch with the inode
	f.send(fsnotify.Remove, paths["d/"])
	require.NoError(t, os.Mkdir(paths["d/"], 0o755))
	fresh := filepath.Join(paths["d/"], "fresh.txt")
	require.NoError(t, os.WriteFile(fresh, nil, 0o644))
	f.send(fsnotify.Create, paths["d/"])
	settle(t, m, f, root)

	require.True(t, f.has(paths["d/"]), "reconcile re-armed the watch on the recreated dir")
	require.True(t, hasPath(m, fresh), "new content indexed")
	require.False(t, hasPath(m, paths["d/old.txt"]), "stale child diffed away via ChildrenOf")
	require.Equal(t, 2, w.Stats().WatchedDirs)
}

// TestWatcherRefreshFailureDropsDeadWatch covers refreshWatch's
// failure path: when the re-issued Add for a recreated directory is
// refused, the old bookkeeping entry provably points at a dead watch
// and must be dropped (and counted), not kept as a false claim.
func TestWatcherRefreshFailureDropsDeadWatch(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "d/")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	calls := 0
	f.addErr = func(path string) error { // runs under the fake's lock
		if path != paths["d/"] {
			return nil
		}
		calls++
		if calls > 1 {
			return errors.New("inotify: no space left on device")
		}
		return nil
	}
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 2 }, "root and d watched")

	// d goes away and comes back within one window (one merged dirty
	// path); the refresh Add is scripted to fail.
	require.NoError(t, os.RemoveAll(paths["d/"]))
	f.unwatch(paths["d/"])
	f.send(fsnotify.Remove, paths["d/"])
	require.NoError(t, os.Mkdir(paths["d/"], 0o755))
	f.send(fsnotify.Create, paths["d/"])
	settle(t, m, f, root)

	require.False(t, f.has(paths["d/"]))
	waitFor(t, func() bool {
		s := w.Stats()
		return s.WatchedDirs == 1 && s.DroppedWatches == 1 && s.Degraded
	}, "the dead watch leaves the bookkeeping and the drop is counted")
}

// TestWatcherParentDirtyRemovesMissingChildren covers deletions the
// watcher never hears about directly: children vanish with no events
// of their own (lost to an overflow, or merged away by a batching
// backend), and only the PARENT is dirtied. The ChildrenOf diff must
// tombstone them anyway.
func TestWatcherParentDirtyRemovesMissingChildren(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "p/", "p/gone.txt", "p/gonedir/", "p/gonedir/deep.txt", "p/stays.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 3 }, "root, p, gonedir watched")

	require.NoError(t, os.Remove(paths["p/gone.txt"]))
	require.NoError(t, os.RemoveAll(paths["p/gonedir/"]))
	f.unwatch(paths["p/gonedir/"])
	f.send(fsnotify.Create, paths["p/"]) // ANY op only marks p dirty
	settle(t, m, f, root)

	require.False(t, hasPath(m, paths["p/gone.txt"]), "missing file child diffed away")
	require.False(t, hasPath(m, paths["p/gonedir/"]), "missing dir child diffed away")
	require.False(t, hasPath(m, paths["p/gonedir/deep.txt"]), "missing dir child's subtree tombstoned")
	require.True(t, hasPath(m, paths["p/stays.txt"]), "surviving child untouched")
	require.True(t, hasPath(m, paths["p/"]))
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 2 }, "the vanished dir's watch is dropped")
	require.False(t, f.has(paths["p/gonedir/"]))
}

// TestWatcherTypeFlipViaParentReconcile flips both kinds under one
// dirty parent: a file becomes a directory (with content) and a
// directory becomes a file, neither with events of its own. The diff
// must tombstone-and-re-add each flipped child with the new kind --
// including the old dir's subtree and watches.
func TestWatcherTypeFlipViaParentReconcile(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "p/", "p/wasfile", "p/wasdir/", "p/wasdir/child.txt")
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newTestWatcher(t, m, f)
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 3 }, "root, p, wasdir watched")

	require.NoError(t, os.Remove(paths["p/wasfile"]))
	require.NoError(t, os.Mkdir(paths["p/wasfile"], 0o755))
	nested := filepath.Join(paths["p/wasfile"], "nested.txt")
	require.NoError(t, os.WriteFile(nested, nil, 0o644))
	require.NoError(t, os.RemoveAll(paths["p/wasdir/"]))
	f.unwatch(paths["p/wasdir/"])
	require.NoError(t, os.WriteFile(paths["p/wasdir/"], []byte("now a file"), 0o644))
	f.send(fsnotify.Remove, paths["p/"]) // op is advisory; p still exists
	settle(t, m, f, root)

	// file -> dir: dir bit set, new subtree scanned, watch added.
	for _, r := range m.Query("wasfile", 0) {
		if r.Path == paths["p/wasfile"] {
			require.True(t, r.IsDir, "file->dir flip sets the entry's dir bit")
		}
	}
	require.True(t, hasPath(m, nested), "flipped-to-dir subtree scanned in")
	require.True(t, f.has(paths["p/wasfile"]), "flipped-to-dir gains a watch")

	// dir -> file: subtree tombstoned, watch dropped, dir bit cleared.
	require.False(t, hasPath(m, paths["p/wasdir/child.txt"]), "dir->file flip tombstones the old subtree")
	require.True(t, hasPath(m, paths["p/wasdir/"]), "the flipped path itself stays indexed")
	for _, r := range m.Query("wasdir", 0) {
		if r.Path == paths["p/wasdir/"] {
			require.False(t, r.IsDir, "dir->file flip clears the entry's dir bit")
		}
	}
	require.False(t, f.has(paths["p/wasdir/"]), "flipped-to-file loses its watch")
	require.Equal(t, 3, w.Stats().WatchedDirs, "root, p, wasfile")
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
