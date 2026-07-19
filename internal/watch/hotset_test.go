package watch

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func TestResolveBudget(t *testing.T) {
	fake := func(n int) func() int { return func() int { return n } }
	cases := []struct {
		name       string
		maxWatches int
		readMax    func() int
		want       int
	}{
		{"explicit positive wins", 7, fake(1 << 20), 7},
		{"negative is unlimited", -1, fake(1 << 20), math.MaxInt},
		{"auto: half the kernel limit", 0, fake(100000), 50000},
		{"auto: capped at 65536", 0, fake(1 << 20), 65536},
		{"auto: floored at 1024", 0, fake(1000), 1024},
		{"auto: read failure is unlimited", 0, fake(0), math.MaxInt},
		{"auto: negative read is unlimited", 0, fake(-5), math.MaxInt},
		{"auto: nil reader is unlimited (non-linux shape)", 0, nil, math.MaxInt},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, resolveBudget(tc.maxWatches, tc.readMax), tc.name)
	}
}

func TestReadInotifyMaxWatchesSmoke(t *testing.T) {
	v := readInotifyMaxWatches()
	if runtime.GOOS == "linux" {
		require.Greater(t, v, 0, "linux exposes the limit under /proc")
	} else {
		require.Zero(t, v, "non-linux reports unknown")
	}
}

func TestHotSetAutoBudgetViaSeam(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	w := newBudgetWatcher(t, m, newFakeNotifier(), 0)
	w.readMaxWatches = func() int { return 4000 }
	startWatcherRegistered(t, w)
	require.Equal(t, 2000, w.Stats().Budget, "auto budget consults the seam")

	w2 := newBudgetWatcher(t, m, newFakeNotifier(), 0)
	w2.readMaxWatches = func() int { return 0 }
	startWatcherRegistered(t, w2)
	require.Equal(t, math.MaxInt, w2.Stats().Budget, "unreadable limit means watch-everything")
}

func TestHotSetBudgetNeverExceededAndColdAddsSilent(t *testing.T) {
	root := t.TempDir()
	var layout []string
	for i := 0; i < 6; i++ {
		layout = append(layout, fmt.Sprintf("d%d/", i))
	}
	mkTree(t, root, layout...)
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 3)
	startWatcherRegistered(t, w)

	s := w.Stats()
	require.Equal(t, 3, s.Budget)
	require.Equal(t, 3, s.WatchedDirs, "the fill stops exactly at the budget")
	require.Equal(t, 7, s.IndexedDirs, "the desired set (root + 6 dirs) is still counted in full")
	require.Equal(t, 3, f.addAttempts(), "beyond-budget dirs never cost a syscall")
	require.Zero(t, s.DroppedWatches, "cold dirs are not drops")
	require.Zero(t, s.Evictions, "a fill never evicts")
	require.False(t, s.Degraded, "a budgeted hot set is not degradation")
	require.True(t, f.has(root), "the root is always watched")

	// Event-driven churn promotes new dirs by evicting cold ones; the
	// watched count never exceeds the budget.
	for i := 0; i < 3; i++ {
		d := filepath.Join(root, fmt.Sprintf("late%d", i))
		require.NoError(t, os.Mkdir(d, 0o755))
		f.send(fsnotify.Create, d)
	}
	settle(t, m, f, root)
	s = w.Stats()
	require.Equal(t, 3, s.WatchedDirs, "promotion swaps watches, never grows past the budget")
	require.Positive(t, s.Evictions, "at budget, promotions evict")
	require.Zero(t, s.DroppedWatches)
	require.False(t, s.Degraded, "evictions are not degradation")
}

func TestHotSetLRUEvictionHonorsTouches(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "a/", "b/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 3) // root (pinned) + 2 evictable slots
	startWatcherRegistered(t, w)
	require.True(t, f.has(paths["a/"]) && f.has(paths["b/"]), "both dirs fit the budget")

	// Touch b through file activity inside it: reconcile touches the
	// parent of every dirty path, so b becomes the hottest entry and a
	// the eviction candidate.
	inB := filepath.Join(paths["b/"], "touch.txt")
	require.NoError(t, os.WriteFile(inB, nil, 0o644))
	f.send(fsnotify.Create, inB)
	waitFor(t, func() bool { return hasPath(m, inB) }, "the touch event lands")

	// A new hot dir at budget evicts the least-recently-touched: a.
	c := filepath.Join(root, "c")
	require.NoError(t, os.Mkdir(c, 0o755))
	f.send(fsnotify.Create, c)
	waitFor(t, func() bool { return f.has(c) }, "the new dir is promoted into the hot set")

	require.False(t, f.has(paths["a/"]), "the untouched dir was evicted")
	require.True(t, f.has(paths["b/"]), "the touched dir survived")
	require.True(t, f.has(root))
	s := w.Stats()
	require.Equal(t, 3, s.WatchedDirs)
	require.Equal(t, 1, s.Evictions)
	require.False(t, s.Degraded)
}

func TestHotSetRootsNeverEvicted(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "a/", "b/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 1) // the root eats the whole budget
	startWatcherRegistered(t, w)

	require.True(t, f.has(root), "the root is watched even when it fills the budget")
	require.Equal(t, 1, f.addAttempts(), "no syscalls for the unaffordable dirs")
	require.Equal(t, 1, w.Stats().WatchedDirs)

	// Promotion attempts cannot displace a pinned root: with nothing
	// evictable the candidate stays cold.
	w.promote(paths["a/"])
	require.False(t, f.has(paths["a/"]), "nothing evictable: the dir stays cold")
	require.True(t, f.has(root))
	s := w.Stats()
	require.Equal(t, 1, s.WatchedDirs)
	require.Zero(t, s.Evictions)
	require.False(t, s.Degraded)

	// A dirty ROOT refreshes its own (pinned) watch and stays watched.
	f.send(fsnotify.Create, root)
	settle(t, m, f, root)
	require.True(t, f.has(root))
	require.Equal(t, 1, w.Stats().WatchedDirs)
}

func TestHotSetPromoteEvictsColder(t *testing.T) {
	root := t.TempDir()
	paths := mkTree(t, root, "a/", "b/", "c/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 2) // root + exactly one evictable slot
	startWatcherRegistered(t, w)
	require.Equal(t, 2, w.Stats().WatchedDirs)
	require.True(t, f.has(paths["a/"]), "id order fills the single slot with a")

	w.promote(paths["b/"])
	require.True(t, f.has(paths["b/"]))
	require.False(t, f.has(paths["a/"]), "promote evicted the colder entry")

	w.promote(paths["c/"])
	require.True(t, f.has(paths["c/"]))
	require.False(t, f.has(paths["b/"]))

	w.promote(paths["c/"]) // promoting the already-hottest entry is a touch, not an eviction
	require.True(t, f.has(paths["c/"]))

	s := w.Stats()
	require.Equal(t, 2, s.WatchedDirs)
	require.Equal(t, 2, s.Evictions)
	require.True(t, f.has(root), "the root never left the set")
}

func TestHotSetPriorityFillPrefersHome(t *testing.T) {
	root := t.TempDir()
	var layout []string
	for i := 1; i <= 6; i++ {
		layout = append(layout, fmt.Sprintf("home/h%d/", i), fmt.Sprintf("other/o%d/", i))
	}
	paths := mkTree(t, root, layout...)
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	w := newBudgetWatcher(t, m, f, 5) // homeCap = 4: root + 3 home dirs, then 1 other
	home := filepath.Join(root, "home")
	w.homeDir = func() (string, error) { return home, nil }
	startWatcherRegistered(t, w)

	s := w.Stats()
	require.Equal(t, 5, s.WatchedDirs)
	require.Equal(t, 15, s.IndexedDirs, "root + 14 dirs")
	require.True(t, f.has(root), "the root always wins the first slot")

	homeWatched, otherWatched := 0, 0
	count := func(p string) {
		if !f.has(p) {
			return
		}
		if pathWithin(p, home) {
			homeWatched++
		} else {
			otherWatched++
		}
	}
	count(home)
	count(filepath.Join(root, "other"))
	for _, p := range paths {
		count(p)
	}
	require.Equal(t, 3, homeWatched, "home-subtree dirs fill up to 75% of the budget first")
	require.Equal(t, 1, otherWatched, "the remainder goes to everything else")

	// A failing homeDir seam degrades to no home preference, not a
	// crash: everything is "rest".
	w2 := newBudgetWatcher(t, m, newFakeNotifier(), 5)
	w2.homeDir = func() (string, error) { return "", os.ErrNotExist }
	startWatcherRegistered(t, w2)
	require.Equal(t, 5, w2.Stats().WatchedDirs)
}
