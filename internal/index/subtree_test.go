package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// RemoveByPath walks DOWN through the children graph instead of
// scanning the whole dir table, and seeds that walk from `entryless`
// (dir ids interned without an entry of their own). These tests pin
// both halves: the walk's result against the old full scan, and the
// smallness of entryless that keeps the walk cheap.

// refRemoveByPath is the pre-rewrite implementation: tombstone the
// entry itself, then scan EVERY interned directory and tombstone the
// children of any at or below path.
func refRemoveByPath(s *Store, path string) int {
	path = filepath.Clean(path)
	removed := 0
	if pid, ok := s.dirIndex[filepath.Dir(path)]; ok {
		if id := refFindChild(s, pid, filepath.Base(path)); id >= 0 {
			removed += s.tombstone(id)
		}
	}
	if _, ok := s.dirIndex[path]; ok {
		for did, dirPath := range s.dirs {
			if !isWithin(dirPath, path) {
				continue
			}
			for _, id := range s.children[uint32(did)] {
				removed += s.tombstone(id)
			}
		}
	}
	return removed
}

// liveSet lists the live entry paths, so two stores can be compared by
// content rather than by internal layout.
func liveSet(s *Store) map[string]bool {
	out := map[string]bool{}
	s.ForEachLive(func(id int32) bool {
		out[s.EntryPath(id)] = true
		return true
	})
	return out
}

// buildTree fills a store with a small nested tree through AddEntry.
func buildTree(t *testing.T, s *Store) {
	t.Helper()
	add := func(dir, name string, isDir bool) {
		t.Helper()
		_, err := s.AddEntry(dir, name, isDir)
		require.NoError(t, err)
	}
	add("/r", "a", true)
	add("/r", "keep.txt", false)
	add("/r/a", "b", true)
	add("/r/a", "f1.txt", false)
	add("/r/a/b", "c", true)
	add("/r/a/b", "f2.txt", false)
	add("/r/a/b/c", "deep.txt", false)
	add("/r", "sibling", true)
	add("/r/sibling", "s.txt", false)
}

// TestRemoveByPathMatchesFullScan compares the subtree walk against the
// old whole-dir-table scan for every removable path in the tree, on
// independent stores built the same way.
func TestRemoveByPathMatchesFullScan(t *testing.T) {
	targets := []string{
		"/r/a", "/r/a/b", "/r/a/b/c", "/r/a/b/c/deep.txt",
		"/r/keep.txt", "/r/sibling", "/r", "/r/absent", "/nowhere",
	}
	for _, target := range targets {
		got, want := NewStore(), NewStore()
		buildTree(t, got)
		buildTree(t, want)

		nGot := got.RemoveByPath(target)
		nWant := refRemoveByPath(want, target)
		require.Equal(t, nWant, nGot, "removed count for %q", target)
		require.Equal(t, liveSet(want), liveSet(got), "live set after removing %q", target)
	}
}

// TestRemoveByPathReachesUnentriedSubtree is the case the entryless
// seeding exists for: a file event interned its parent directory
// before that directory's own entry was created, so the parent is NOT
// reachable through the children graph. Its contents must still be
// tombstoned.
func TestRemoveByPathReachesUnentriedSubtree(t *testing.T) {
	s := NewStore()
	_, err := s.AddEntry("/r", "a", true)
	require.NoError(t, err)
	// No entry for "b" under /r/a -- only /r/a/b interned as a parent,
	// exactly what AddEntry does for a file event in a new directory.
	_, err = s.AddEntry("/r/a/b", "orphan.txt", false)
	require.NoError(t, err)
	require.Contains(t, liveSet(s), "/r/a/b/orphan.txt")
	require.Contains(t, s.entryless, s.dirIndex["/r/a/b"],
		"a parent interned without its own entry must be marked")

	require.Positive(t, s.RemoveByPath("/r/a"))
	require.NotContains(t, liveSet(s), "/r/a/b/orphan.txt",
		"the unentried subtree must still be tombstoned")
}

// TestInternDirClearsEntrylessMark pins the self-healing half: once the
// directory gains its own entry it is reachable through children, so
// the mark must go away rather than accumulating.
func TestInternDirClearsEntrylessMark(t *testing.T) {
	s := NewStore()
	_, err := s.AddEntry("/r/a/b", "f.txt", false)
	require.NoError(t, err)
	id := s.dirIndex["/r/a/b"]
	require.Contains(t, s.entryless, id)

	_, err = s.AddEntry("/r/a", "b", true) // the directory's own entry
	require.NoError(t, err)
	require.NotContains(t, s.entryless, id,
		"a directory that gained its entry is reachable and must be unmarked")
}

// TestWalkLeavesOnlyRootsEntryless is the invariant the whole removal
// rewrite rests on: tombstoneSubtree scans entryless linearly, so it is
// only cheap while that set stays tiny. After a real walk it must hold
// exactly the roots.
func TestWalkLeavesOnlyRootsEntryless(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "a/b", "a/b/c", "d"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, d, "f.txt"), nil, 0o644))
	}
	s := NewStore()
	_, err := Walk(context.Background(), s, []string{root}, nil, nil)
	require.NoError(t, err)
	require.Greater(t, len(s.dirs), 4, "the walk should have interned the tree")

	require.Len(t, s.entryless, 1, "only the walk root lacks an entry")
	require.Contains(t, s.entryless, s.dirIndex[filepath.Clean(root)])
}
