package index

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The traversal itself (queue, exclude semantics, root normalization,
// symlink and cancellation behavior) is gated in go-fs-tree-fast. What
// stays here is the seam between that walker and the Store: the entries
// it hands over must land in the store exactly once, with the right
// paths, and the presized children slices must survive.

// makeDiskTree creates nDirs subdirectories of filesPerDir files each
// under a fresh temp root and returns the root plus the expected entry
// count (subdir entries + file entries).
func makeDiskTree(t *testing.T, nDirs, filesPerDir int) (string, int) {
	t.Helper()
	root := t.TempDir()
	for d := 0; d < nDirs; d++ {
		sub := filepath.Join(root, fmt.Sprintf("sub_%03d", d))
		require.NoError(t, os.Mkdir(sub, 0o755))
		for f := 0; f < filesPerDir; f++ {
			writeFile(t, filepath.Join(sub, fmt.Sprintf("f_%03d.txt", f)))
		}
	}
	return root, nDirs + nDirs*filesPerDir
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
}

func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		require.NoError(t, os.MkdirAll(filepath.Join(root, r), 0o755))
	}
}

// walkInto runs Walk on a fresh store with default context.
func walkInto(t *testing.T, roots, excludes []string) (*Store, WalkStats) {
	t.Helper()
	st := NewStore()
	stats, err := Walk(context.Background(), st, roots, excludes, nil)
	require.NoError(t, err)
	return st, stats
}

func TestWalkBasic(t *testing.T) {
	root, want := makeDiskTree(t, 40, 60)
	var mu sync.Mutex
	var calls []int
	var doneSeen bool
	st := NewStore()
	stats, err := Walk(context.Background(), st, []string{root}, nil, func(indexed int, done bool) {
		mu.Lock()
		defer mu.Unlock()
		require.False(t, doneSeen, "no progress after the done callback")
		calls = append(calls, indexed)
		doneSeen = done
	})
	require.NoError(t, err)

	require.Equal(t, want, stats.Indexed)
	require.Equal(t, 41, stats.Dirs) // root plus its subdirs
	require.Equal(t, 0, stats.Errors)
	require.Equal(t, 0, stats.SkippedRoots)
	require.Equal(t, want, st.LiveCount())

	require.True(t, doneSeen, "final done=true callback")
	require.Equal(t, want, calls[len(calls)-1])
	for i := 1; i < len(calls); i++ {
		require.LessOrEqual(t, calls[i-1], calls[i], "progress counts are monotonic")
	}

	// Spot-check path reconstruction against the real filesystem: the
	// store rebuilds a path from the dir table plus the name blob, so a
	// misfiled entry shows up as a path that does not exist.
	found := 0
	st.ForEachLive(func(id int32) bool {
		p := st.EntryPath(id)
		_, statErr := os.Lstat(p)
		require.NoError(t, statErr, "indexed path %q must exist", p)
		found++
		return found < 25
	})
}

func TestWalkExcludes(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "node_modules/pkg", ".git", "src/sub", "docs", "x/private")
	writeFile(t, filepath.Join(root, "node_modules/pkg/a.js"))
	writeFile(t, filepath.Join(root, "node_modules/c.js"))
	writeFile(t, filepath.Join(root, ".git/config"))
	writeFile(t, filepath.Join(root, "src/main.go"))
	writeFile(t, filepath.Join(root, "src/main.tmp"))
	writeFile(t, filepath.Join(root, "src/sub/keep.txt"))
	writeFile(t, filepath.Join(root, "docs/readme.md"))
	writeFile(t, filepath.Join(root, "x/private/secret.txt"))
	writeFile(t, filepath.Join(root, "x/other.txt"))

	excludes := []string{"node_modules", ".git", "*.tmp", filepath.Join(root, "*", "private")}
	st, stats := walkInto(t, []string{root}, excludes)

	got := livePaths(st)
	want := map[string]bool{
		filepath.Join(root, "src"):              true,
		filepath.Join(root, "src/main.go"):      true,
		filepath.Join(root, "src/sub"):          true,
		filepath.Join(root, "src/sub/keep.txt"): true,
		filepath.Join(root, "docs"):             true,
		filepath.Join(root, "docs/readme.md"):   true,
		filepath.Join(root, "x"):                true,
		filepath.Join(root, "x/other.txt"):      true,
	}
	require.Equal(t, want, got)
	require.Equal(t, len(want), stats.Indexed)
	// Pruned dirs are never read: root, src, src/sub, docs, x.
	require.Equal(t, 5, stats.Dirs)
}

// TestWalkUnreadableDirInjected covers the readDirFn seam this package
// owns: an injected error must count as one Errors, keep the directory
// entry itself, and drop only its contents.
func TestWalkUnreadableDirInjected(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "good", "bad")
	writeFile(t, filepath.Join(root, "good/ok.txt"))
	writeFile(t, filepath.Join(root, "bad/hidden.txt"))

	orig := readDirFn
	readDirFn = func(name string) ([]os.DirEntry, error) {
		if name == filepath.Join(root, "bad") {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
		return orig(name)
	}
	defer func() { readDirFn = orig }()

	st, stats := walkInto(t, []string{root}, nil)
	require.Equal(t, 1, stats.Errors)
	got := livePaths(st)
	require.True(t, got[filepath.Join(root, "bad")], "the unreadable dir itself is still indexed")
	require.True(t, got[filepath.Join(root, "good/ok.txt")])
	require.False(t, got[filepath.Join(root, "bad/hidden.txt")], "contents of unreadable dir are skipped")
}

// TestWalkOverlappingAndDuplicateRoots matters at the store level: a
// root walked twice would append every entry twice, which no later
// duplicate check would catch.
func TestWalkOverlappingAndDuplicateRoots(t *testing.T) {
	root, want := makeDiskTree(t, 5, 4)
	nested := filepath.Join(root, "sub_000")
	st, stats := walkInto(t, []string{root, nested, root}, nil)
	require.Equal(t, 2, stats.SkippedRoots)
	require.Equal(t, want, stats.Indexed, "overlapping roots must not double-index")
	require.Equal(t, want, st.LiveCount())
}

func TestWalkSeparateRootsBothIndexed(t *testing.T) {
	rootA, wantA := makeDiskTree(t, 3, 5)
	rootB, wantB := makeDiskTree(t, 2, 7)
	st, stats := walkInto(t, []string{rootA, rootB}, nil)
	require.Equal(t, wantA+wantB, stats.Indexed)
	require.Equal(t, 0, stats.SkippedRoots)
	require.Equal(t, wantA+wantB, st.LiveCount())
}

func TestWalkNonexistentRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	st, stats := walkInto(t, []string{missing}, nil)
	require.Equal(t, 1, stats.Errors)
	require.Equal(t, 0, stats.Indexed)
	require.Equal(t, 0, st.Len())
}

func TestWalkCancelledContext(t *testing.T) {
	root, _ := makeDiskTree(t, 10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := NewStore()
	_, err := Walk(ctx, st, []string{root}, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestWalkBadExcludePattern(t *testing.T) {
	st := NewStore()
	_, err := Walk(context.Background(), st, []string{t.TempDir()}, []string{"["}, nil)
	require.Error(t, err)
	require.Equal(t, 0, st.Len())
}

func TestWalkNoRoots(t *testing.T) {
	st := NewStore()
	doneCount := -1
	stats, err := Walk(context.Background(), st, nil, nil, func(indexed int, done bool) {
		if done {
			doneCount = indexed
		}
	})
	require.NoError(t, err)
	require.Equal(t, WalkStats{}, stats)
	require.Equal(t, 0, doneCount, "done callback still fires with nothing walked")
}

// TestWalkChildrenPresized proves the growChildren presize: a fresh
// walk visits every directory exactly once, so each children slice
// must end at cap == len -- no append-ladder overshoot survives.
func TestWalkChildrenPresized(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "d1", "d2/d3")
	for i := 0; i < 9; i++ {
		writeFile(t, filepath.Join(root, "d1", fmt.Sprintf("f%d.txt", i)))
	}
	writeFile(t, filepath.Join(root, "d2/d3/one.txt"))
	writeFile(t, filepath.Join(root, "top.txt"))

	st, _ := walkInto(t, []string{root}, nil)
	require.NotEmpty(t, st.children)
	for pid, kids := range st.children {
		require.Equal(t, len(kids), cap(kids),
			"children of %q grew past their exact batch size", st.dirs[pid])
	}
}

// TestExcluderIsReExported keeps the re-export honest: internal/watch
// and internal/app build Excluders through this package and hand them
// back for event filtering, so the alias must stay a working type.
func TestExcluderIsReExported(t *testing.T) {
	ex, err := NewExcluder([]string{"node_modules", "/home/*/secret"})
	require.NoError(t, err)
	require.True(t, ex.MatchBase("node_modules"))
	require.True(t, ex.MatchFull("/home/bob/secret"))
	require.True(t, ex.HasFullPatterns())

	_, err = NewExcluder([]string{"["})
	require.Error(t, err)

	var nilEx *Excluder
	require.False(t, nilEx.Match("a", "/a"), "nil excluder matches nothing")
}
