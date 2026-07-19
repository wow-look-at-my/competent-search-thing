package watch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// TestTierEquivalence pins the user's hard invariant: every tier --
// full live watching, a budgeted hot set with evictions and cold
// misses, and sweep-only -- reaches the IDENTICAL final index state
// for the same mutation script, and that state equals the on-disk
// truth. Tiers differ only in latency, never in final state.
func TestTierEquivalence(t *testing.T) {
	var full, hot, sweep map[string]bool
	t.Run("FullWatch", func(t *testing.T) { full = runTierFullWatch(t) })
	t.Run("HotSetWithSweep", func(t *testing.T) { hot = runTierHotSet(t) })
	t.Run("SweepOnly", func(t *testing.T) { sweep = runTierSweepOnly(t) })
	require.NotEmpty(t, full)
	require.Equal(t, full, hot, "hot-set tier must converge to the full-watch state")
	require.Equal(t, full, sweep, "sweep-only tier must converge to the full-watch state")
}

// equivBase builds the pre-mutation tree every tier starts from.
func equivBase(t *testing.T, root string) {
	t.Helper()
	mkTree(t, root,
		"keep/", "keep/stay.txt",
		"gone.txt",
		"gonedir/", "gonedir/deep.txt",
		"ren/", "ren/inner.txt",
		"churn.txt",
		"flipfile",
		"flipdir/", "flipdir/child.txt",
	)
}

// applyMutationScript performs the shared mutation storm on a prepared
// tree and returns the dirty paths a notification backend would
// report, in order: creates, deletes, a rename, a coalesced
// mkdir -p burst (only the topmost dir reported), same-name
// delete+recreate churn, and both type flips.
func applyMutationScript(t *testing.T, root string) []string {
	t.Helper()
	var dirty []string
	touch := func(p string) { dirty = append(dirty, p) }

	// Creates.
	fresh := filepath.Join(root, "fresh.txt")
	require.NoError(t, os.WriteFile(fresh, nil, 0o644))
	touch(fresh)
	newdir := filepath.Join(root, "newdir")
	require.NoError(t, os.Mkdir(newdir, 0o755))
	touch(newdir)
	leaf := filepath.Join(newdir, "leaf.txt")
	require.NoError(t, os.WriteFile(leaf, nil, 0o644))
	touch(leaf)

	// Deletes; the subtree's children get no events of their own.
	require.NoError(t, os.Remove(filepath.Join(root, "gone.txt")))
	touch(filepath.Join(root, "gone.txt"))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "gonedir")))
	touch(filepath.Join(root, "gonedir"))

	// A rename: old name + new name dirty.
	ren2 := filepath.Join(root, "ren2")
	require.NoError(t, os.Rename(filepath.Join(root, "ren"), ren2))
	touch(filepath.Join(root, "ren"))
	touch(ren2)

	// Nested mkdir -p, reported only as the topmost create (a
	// coalesced burst).
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(deep, "deep.txt"), nil, 0o644))
	touch(filepath.Join(root, "a"))

	// Same-name delete+recreate churn.
	churn := filepath.Join(root, "churn.txt")
	require.NoError(t, os.Remove(churn))
	touch(churn)
	require.NoError(t, os.WriteFile(churn, []byte("reborn"), 0o644))
	touch(churn)

	// Type flips: file -> dir (with content) and dir -> file, each
	// dirtying only its own path.
	flipfile := filepath.Join(root, "flipfile")
	require.NoError(t, os.Remove(flipfile))
	require.NoError(t, os.Mkdir(flipfile, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(flipfile, "inner.txt"), nil, 0o644))
	touch(flipfile)
	flipdir := filepath.Join(root, "flipdir")
	require.NoError(t, os.RemoveAll(flipdir))
	require.NoError(t, os.WriteFile(flipdir, []byte("now a file"), 0o644))
	touch(flipdir)

	return dirty
}

// equivSkip filters the paths the state collectors ignore: the settle
// helper's marker files (their names embed timestamps, so they can
// never match across tiers).
func equivSkip(name string) bool { return strings.HasSuffix(name, ".settle") }

// indexState collects the index's live entries under root as a
// root-relative path -> isDir map, walking the store's own children
// links.
func indexState(t *testing.T, m *index.Manager, root string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	var walk func(dir string)
	walk = func(dir string) {
		for _, c := range m.ChildrenOf(dir) {
			if equivSkip(c.Name) {
				continue
			}
			p := filepath.Join(dir, c.Name)
			rel, err := filepath.Rel(root, p)
			require.NoError(t, err)
			out[rel] = c.IsDir
			if c.IsDir {
				walk(p)
			}
		}
	}
	walk(root)
	return out
}

// diskState collects the on-disk truth under root in the same shape
// (lstat semantics: symlinks are non-dirs and never followed, like the
// walker and the watcher).
func diskState(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if p == root || equivSkip(d.Name()) {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		require.NoError(t, rerr)
		out[rel] = d.IsDir()
		return nil
	})
	require.NoError(t, err)
	return out
}

// requireConverged asserts the index matches the on-disk truth and
// returns the (relative) state for cross-tier comparison.
func requireConverged(t *testing.T, m *index.Manager, root string) map[string]bool {
	t.Helper()
	want := diskState(t, root)
	got := indexState(t, m, root)
	require.Equal(t, want, got, "the final index must equal the on-disk truth")
	// The children walk above cannot see live entries whose parent was
	// tombstoned; the live count closes that hole (orphaned survivors
	// of a half-applied subtree removal would show up here).
	markers := len(m.Query(".settle", 0))
	require.Equal(t, len(got)+markers, m.LiveCount(), "no orphaned live entries outside the reachable tree")
	return got
}

// runTierFullWatch: unlimited budget, every event delivered.
func runTierFullWatch(t *testing.T) map[string]bool {
	root := t.TempDir()
	equivBase(t, root)
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, -1)
	startWatcherRegistered(t, w)

	for _, p := range applyMutationScript(t, root) {
		f.send(fsnotify.Create, p) // ops are advisory; the dirty path is what counts
	}
	settle(t, m, f, root)
	return requireConverged(t, m, root)
}

// runTierHotSet: a tiny budget (root + one slot); events are delivered
// ONLY for paths whose parent dir currently holds a watch, simulating
// cold misses, then one sweep pass converges the rest.
func runTierHotSet(t *testing.T) map[string]bool {
	root := t.TempDir()
	equivBase(t, root)
	m := buildManager(t, root, nil)
	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 2)
	startWatcherRegistered(t, w)

	for _, p := range applyMutationScript(t, root) {
		if f.has(filepath.Dir(p)) {
			f.send(fsnotify.Create, p)
		}
	}
	settle(t, m, f, root) // the root is pinned, so the marker always arrives

	s := newTestSweeper(t, m, w, SweepOptions{}) // zero watermark: full re-list
	startSweeper(t, s)
	sweepOnce(t, s)
	return requireConverged(t, m, root)
}

// runTierSweepOnly: the notifier accepts watches but never delivers a
// single event; one sweep pass alone converges the index.
func runTierSweepOnly(t *testing.T) map[string]bool {
	root := t.TempDir()
	equivBase(t, root)
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	applyMutationScript(t, root)

	s := newTestSweeper(t, m, w, SweepOptions{})
	startSweeper(t, s)
	sweepOnce(t, s)
	return requireConverged(t, m, root)
}
