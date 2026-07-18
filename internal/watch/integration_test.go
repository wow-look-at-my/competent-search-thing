package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// startLive builds a Manager over root, walks it, and starts a Watcher
// on the REAL fsnotify implementation with fast debounce thresholds. It
// returns once the root watch is guaranteed live (roots are watched
// first), so tests can start mutating the tree without losing events.
func startLive(t *testing.T, root string, excludes []string) (*index.Manager, *Watcher) {
	t.Helper()
	m := buildManager(t, root, excludes)
	w := newTestWatcher(t, m, nil)
	// These tests exercise the REAL fsnotify backend specifically, so
	// pin it: the production default is backend auto-detection, which
	// would try fanotify first on capable machines and change the
	// per-directory watch counts asserted below.
	w.newNotifier = newFSNotifier
	startWatcher(t, w)
	waitFor(t, func() bool { return w.Stats().WatchedDirs >= 1 }, "root watch registered")
	return m, w
}

func TestIntegrationCreateAndDeleteFile(t *testing.T) {
	root := t.TempDir()
	m, _ := startLive(t, root, nil)

	p := filepath.Join(root, "created-live.txt")
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, p) }, "create picked up")

	require.NoError(t, os.Remove(p))
	waitFor(t, func() bool { return !hasPath(m, p) }, "delete picked up")
}

func TestIntegrationNestedDirCreation(t *testing.T) {
	root := t.TempDir()
	m, w := startLive(t, root, nil)

	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	leaf := filepath.Join(deep, "leaf.txt")
	require.NoError(t, os.WriteFile(leaf, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, leaf) }, "nested subtree scanned into the index")
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 4 }, "root, a, a/b, a/b/c all watched")

	// The freshly created deepest dir must have a live watch of its own.
	late := filepath.Join(deep, "late-arrival.txt")
	require.NoError(t, os.WriteFile(late, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, late) }, "new deepest dir delivers its own events")
}

func TestIntegrationDeleteSubtree(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "trunk/", "trunk/branch/", "trunk/branch/leaf.txt", "other.txt")
	m, w := startLive(t, root, nil)
	require.True(t, hasPath(m, paths["trunk/branch/leaf.txt"]))
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 3 }, "root, trunk, trunk/branch watched")

	require.NoError(t, os.RemoveAll(paths["trunk/"]))
	waitFor(t, func() bool { return !hasPath(m, paths["trunk/branch/leaf.txt"]) }, "subtree gone from the index")
	waitFor(t, func() bool { return w.Stats().WatchedDirs == 1 }, "subtree watches dropped")
	require.False(t, hasPath(m, paths["trunk/"]))
	require.True(t, hasPath(m, paths["other.txt"]))
}

func TestIntegrationRenameFile(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "before-rename.txt")
	m, _ := startLive(t, root, nil)

	after := filepath.Join(root, "after-rename.txt")
	require.NoError(t, os.Rename(paths["before-rename.txt"], after))
	waitFor(t, func() bool { return hasPath(m, after) && !hasPath(m, paths["before-rename.txt"]) },
		"rename lands as old-name removal plus new-name create")
}

func TestIntegrationRenameDir(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "olddir/", "olddir/inner/", "olddir/inner/deep.txt")
	m, _ := startLive(t, root, nil)

	newdir := filepath.Join(root, "newdir")
	require.NoError(t, os.Rename(paths["olddir/"], newdir))
	moved := filepath.Join(newdir, "inner", "deep.txt")
	waitFor(t, func() bool { return hasPath(m, moved) }, "renamed dir re-indexed under its new path")
	waitFor(t, func() bool { return !hasPath(m, paths["olddir/inner/deep.txt"]) }, "old paths tombstoned")

	// Watches must have moved along: events inside the renamed tree now
	// arrive under the NEW path.
	fresh := filepath.Join(newdir, "inner", "fresh.txt")
	require.NoError(t, os.WriteFile(fresh, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, fresh) }, "renamed subtree keeps delivering events")
}

func TestIntegrationExcludedDirStaysDark(t *testing.T) {
	root := t.TempDir()
	m, w := startLive(t, root, []string{"node_modules"})

	nm := filepath.Join(root, "node_modules")
	require.NoError(t, os.Mkdir(nm, 0o755))
	inside := filepath.Join(nm, "pkg.js")
	require.NoError(t, os.WriteFile(inside, nil, 0o644))

	// The settle marker travels the whole real pipeline, proving the
	// excluded events (sent earlier) were dropped rather than pending.
	settle(t, m, nil, root)
	require.False(t, hasPath(m, nm), "excluded dir never indexed")
	require.False(t, hasPath(m, inside), "contents of excluded dir never indexed")
	require.Equal(t, 1, w.Stats().WatchedDirs, "excluded dir never watched")
}

func TestIntegrationBurstIsCoalescedAndComplete(t *testing.T) {
	root := t.TempDir()
	m, _ := startLive(t, root, nil)
	base := m.LiveCount()

	const n = 400
	for i := 0; i < n; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("burst_%03d.txt", i)), nil, 0o644))
	}
	waitFor(t, func() bool { return m.LiveCount() == base+n },
		"every file of the burst lands after the debounced flushes")
	require.Len(t, m.Query("burst_399", 0), 1)
}
