package index

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// mustAdd adds an entry and fails the test on error.
func mustAdd(t *testing.T, s *Store, parentDir, name string, isDir bool) int32 {
	t.Helper()
	id, err := s.AddEntry(parentDir, name, isDir)
	require.NoError(t, err)
	return id
}

// livePaths returns the set of live entry paths.
func livePaths(s *Store) map[string]bool {
	out := make(map[string]bool)
	s.ForEachLive(func(id int32) bool {
		out[s.EntryPath(id)] = true
		return true
	})
	return out
}

// buildSampleTree adds /root/{docs/{a.txt,b.txt}, src/{main.go}, top.txt}
// with docs and src as directory entries under /root.
func buildSampleTree(t *testing.T, s *Store) {
	t.Helper()
	mustAdd(t, s, "/root", "docs", true)
	mustAdd(t, s, "/root", "src", true)
	mustAdd(t, s, "/root", "top.txt", false)
	mustAdd(t, s, "/root/docs", "a.txt", false)
	mustAdd(t, s, "/root/docs", "b.txt", false)
	mustAdd(t, s, "/root/src", "main.go", false)
}

func TestAddEntryAndPaths(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)

	require.Equal(t, 6, s.Len())
	require.Equal(t, 6, s.LiveCount())
	require.Equal(t, map[string]bool{
		"/root/docs":        true,
		"/root/src":         true,
		"/root/top.txt":     true,
		"/root/docs/a.txt":  true,
		"/root/docs/b.txt":  true,
		"/root/src/main.go": true,
	}, livePaths(s))

	id, err := s.AddEntry("/root/docs", "a.txt", false)
	require.NoError(t, err)
	require.Equal(t, "a.txt", s.Name(id))
	require.Equal(t, "/root/docs", s.ParentDir(id))
	require.Equal(t, "/root/docs/a.txt", s.EntryPath(id))
	require.False(t, s.IsDir(id))

	dirID, err := s.AddEntry("/root", "docs", true)
	require.NoError(t, err)
	require.True(t, s.IsDir(dirID))

	// Root-of-filesystem parent keeps a single separator in paths.
	rid := mustAdd(t, s, "/", "rootfile", false)
	require.Equal(t, "/rootfile", s.EntryPath(rid))
}

func TestAddEntryValidation(t *testing.T) {
	s := NewStore()
	cases := []struct {
		label     string
		parentDir string
		name      string
	}{
		{"empty name", "/root", ""},
		{"dot name", "/root", "."},
		{"dotdot name", "/root", ".."},
		{"nul byte in name", "/root", "bad\x00name"},
		{"separator in name", "/root", "bad/name"},
		{"relative parent", "relative/dir", "ok.txt"},
		{"empty parent", "", "ok.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			id, err := s.AddEntry(tc.parentDir, tc.name, false)
			require.Error(t, err)
			require.Equal(t, int32(-1), id)
		})
	}
	require.Equal(t, 0, s.Len())
}

func TestAddEntryDedupAndResurrect(t *testing.T) {
	s := NewStore()
	first := mustAdd(t, s, "/x", "thing", false)
	again := mustAdd(t, s, "/x", "thing", false)
	require.Equal(t, first, again)
	require.Equal(t, 1, s.Len())

	require.Equal(t, 1, s.RemoveByPath("/x/thing"))
	require.Equal(t, 0, s.LiveCount())
	require.Equal(t, 1, s.Len())

	// Re-adding resurrects the tombstoned entry in place, and the
	// entry kind can change (file replaced by a directory).
	back := mustAdd(t, s, "/x", "thing", true)
	require.Equal(t, first, back)
	require.Equal(t, 1, s.LiveCount())
	require.True(t, s.IsDir(back))
	// The resurrected directory can take children.
	mustAdd(t, s, "/x/thing", "inner.txt", false)
	require.Equal(t, 2, s.LiveCount())
}

func TestRemoveByPathFile(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)

	require.Equal(t, 1, s.RemoveByPath("/root/docs/a.txt"))
	require.Equal(t, 5, s.LiveCount())
	require.False(t, livePaths(s)["/root/docs/a.txt"])

	// Removing again is a no-op.
	require.Equal(t, 0, s.RemoveByPath("/root/docs/a.txt"))
	// Unknown paths are a no-op.
	require.Equal(t, 0, s.RemoveByPath("/nowhere/at/all"))
	require.Equal(t, 5, s.LiveCount())
}

func TestRemoveByPathDirSubtree(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)
	mustAdd(t, s, "/root/docs", "deep", true)
	mustAdd(t, s, "/root/docs/deep", "nested.txt", false)

	// docs + a.txt + b.txt + deep + nested.txt = 5 tombstones.
	require.Equal(t, 5, s.RemoveByPath("/root/docs"))
	require.Equal(t, 3, s.LiveCount())
	want := map[string]bool{
		"/root/src":         true,
		"/root/top.txt":     true,
		"/root/src/main.go": true,
	}
	require.Equal(t, want, livePaths(s))

	// A sibling whose name shares the prefix must NOT be caught by the
	// subtree scan ("/root/docs" vs "/root/docsX").
	s2 := NewStore()
	mustAdd(t, s2, "/root", "docs", true)
	mustAdd(t, s2, "/root", "docsX", true)
	mustAdd(t, s2, "/root/docsX", "keep.txt", false)
	require.Equal(t, 1, s2.RemoveByPath("/root/docs"))
	require.True(t, livePaths(s2)["/root/docsX/keep.txt"])
}

func TestOriginalCasePreserved(t *testing.T) {
	s := NewStore()
	id := mustAdd(t, s, "/Docs", "ReadMe.MD", false)
	require.Equal(t, "ReadMe.MD", s.Name(id))
	require.Equal(t, "/Docs/ReadMe.MD", s.EntryPath(id))

	res := s.Query("readme", 10)
	require.Len(t, res, 1)
	require.Equal(t, "ReadMe.MD", res[0].Name)
	require.Equal(t, "/Docs/ReadMe.MD", res[0].Path)
}

func TestUnicodeNames(t *testing.T) {
	// The store keeps names in original case only; queries carrying
	// non-ASCII runes fold per rune at scan time (fold.go). U+1E9E
	// (capital sharp s) folds to U+00DF, U+0130 (capital dotted I) to
	// plain "i" -- the byte-length-shifting folds. The ASCII-query
	// side of the U+0130/U+212A semantics is pinned in
	// TestFoldSemanticsPins. All non-ASCII text stays escaped in this
	// source file by convention.
	s := NewStore()
	ist := mustAdd(t, s, "/u", "\u0130stanbul.txt", false)
	street := mustAdd(t, s, "/u", "Stra\u1e9eE", false)
	eclair := mustAdd(t, s, "/u", "\u00c9clair.txt", false)
	mustAdd(t, s, "/u", "plain.txt", false)

	cases := []struct {
		query string
		want  int32
	}{
		{"\u0130stanbul", ist},
		{"\u0130STANBUL", ist},
		{"stra\u00dfe", street},
		{"Stra\u1e9eE", street},
		{"\u00e9clair", eclair},
		{"\u00c9CLAIR", eclair},
	}
	for _, tc := range cases {
		res := s.Query(tc.query, 10)
		require.Len(t, res, 1, "query %q", tc.query)
		require.Equal(t, s.EntryPath(tc.want), res[0].Path, "query %q", tc.query)
		require.Equal(t, s.Name(tc.want), res[0].Name, "query %q", tc.query)
	}

	// Original casing round-trips.
	require.Equal(t, "/u/\u0130stanbul.txt", s.EntryPath(ist))
	require.Equal(t, "Stra\u1e9eE", s.Name(street))

	// ASCII queries still fold-match mixed-case ASCII names that sit
	// next to unicode ones in the blob.
	res := s.Query("plain", 10)
	require.Len(t, res, 1)
	require.Equal(t, "plain.txt", res[0].Name)
}

func TestBlobInvariants(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)
	mustAdd(t, s, "/u", "\u0130stanbul.txt", false)

	n := s.Len()
	require.Len(t, s.nameOff, n+1)
	require.Equal(t, uint32(0), s.nameOff[0])
	require.Equal(t, uint32(len(s.names)), s.nameOff[n])

	for i := 0; i < n; i++ {
		id := int32(i)
		require.Equal(t, nameSep, s.names[s.nameOff[id+1]-1], "entry %d separator", i)
		require.NotContains(t, string(s.nameBytes(id)), string(nameSep))
		require.Equal(t, s.Name(id), string(s.nameBytes(id)))
		require.NotEmpty(t, s.Name(id))
	}
}

// liveDirPaths returns the live directory entry paths in id order (the
// ForEachLive-based reference for the LiveDirsPage tests).
func liveDirPaths(s *Store) []string {
	var out []string
	s.ForEachLive(func(id int32) bool {
		if s.IsDir(id) {
			out = append(out, s.EntryPath(id))
		}
		return true
	})
	return out
}

// pageAllLiveDirs drains LiveDirsPage from id 0 with the given page
// size and returns every collected path in page order.
func pageAllLiveDirs(t *testing.T, s *Store, max int) []string {
	t.Helper()
	var all []string
	calls := 0
	for start := int32(0); start != -1; {
		var page []string
		page, start = s.LiveDirsPage(start, max)
		all = append(all, page...)
		calls++
		require.LessOrEqual(t, calls, s.Len()+1, "paging must terminate")
	}
	return all
}

func TestLiveDirsPage(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		s := NewStore()
		dirs, next := s.LiveDirsPage(0, 10)
		require.Nil(t, dirs)
		require.Equal(t, int32(-1), next)
	})

	t.Run("single page covers all", func(t *testing.T) {
		s := NewStore()
		buildSampleTree(t, s)
		dirs, next := s.LiveDirsPage(0, 100)
		require.Equal(t, []string{"/root/docs", "/root/src"}, dirs)
		require.Equal(t, int32(-1), next)
	})

	t.Run("page size 1 equals the full live-dir set", func(t *testing.T) {
		s := NewStore()
		buildSampleTree(t, s)
		mustAdd(t, s, "/root/src", "pkg", true)
		require.Equal(t, liveDirPaths(s), pageAllLiveDirs(t, s, 1))
	})

	t.Run("tombstoned dirs are skipped", func(t *testing.T) {
		s := NewStore()
		buildSampleTree(t, s)
		s.RemoveByPath("/root/docs")
		dirs, next := s.LiveDirsPage(0, 0)
		require.Equal(t, []string{"/root/src"}, dirs)
		require.Equal(t, int32(-1), next)
	})

	t.Run("resurrected dir reappears", func(t *testing.T) {
		s := NewStore()
		buildSampleTree(t, s)
		s.RemoveByPath("/root/docs")
		mustAdd(t, s, "/root", "docs", true)
		dirs, _ := s.LiveDirsPage(0, 0)
		require.Equal(t, []string{"/root/docs", "/root/src"}, dirs,
			"resurrection keeps the original id, so id order is unchanged")
	})

	t.Run("next is -1 exactly at exhaustion", func(t *testing.T) {
		s := NewStore()
		mustAdd(t, s, "/r", "a", true)      // id 0
		mustAdd(t, s, "/r", "b.txt", false) // id 1
		mustAdd(t, s, "/r", "c", true)      // id 2

		// A page that fills before the end reports the next id to
		// examine, even though that id is not a live dir.
		dirs, next := s.LiveDirsPage(0, 1)
		require.Equal(t, []string{"/r/a"}, dirs)
		require.Equal(t, int32(1), next)

		// A page that fills ON the final id has examined the whole id
		// space: next is -1, not Len().
		dirs, next = s.LiveDirsPage(next, 1)
		require.Equal(t, []string{"/r/c"}, dirs)
		require.Equal(t, int32(-1), next)

		// Starting at or past Len() is exhausted immediately.
		dirs, next = s.LiveDirsPage(int32(s.Len()), 1)
		require.Nil(t, dirs)
		require.Equal(t, int32(-1), next)
	})

	t.Run("non-positive max selects the default", func(t *testing.T) {
		s := NewStore()
		var want []string
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("sub%d", i)
			mustAdd(t, s, "/d", name, true)
			want = append(want, "/d/"+name)
		}
		for _, max := range []int{0, -7} {
			dirs, next := s.LiveDirsPage(0, max)
			require.Equal(t, want, dirs, "max %d", max)
			require.Equal(t, int32(-1), next, "max %d", max)
		}
	})

	t.Run("negative start is treated as 0", func(t *testing.T) {
		s := NewStore()
		buildSampleTree(t, s)
		dirs, next := s.LiveDirsPage(-5, 0)
		require.Equal(t, []string{"/root/docs", "/root/src"}, dirs)
		require.Equal(t, int32(-1), next)
	})
}

func TestChildrenOf(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)

	t.Run("unknown dir", func(t *testing.T) {
		require.Nil(t, s.ChildrenOf("/nowhere"))
		require.Nil(t, s.ChildrenOf("relative/dir"))
	})

	t.Run("root children mix files and dirs", func(t *testing.T) {
		require.ElementsMatch(t, []ChildInfo{
			{Name: "docs", IsDir: true},
			{Name: "src", IsDir: true},
			{Name: "top.txt", IsDir: false},
		}, s.ChildrenOf("/root"))
	})

	t.Run("nested dir children", func(t *testing.T) {
		require.ElementsMatch(t, []ChildInfo{
			{Name: "a.txt"},
			{Name: "b.txt"},
		}, s.ChildrenOf("/root/docs"))
	})

	t.Run("unclean path is cleaned", func(t *testing.T) {
		require.ElementsMatch(t, []ChildInfo{
			{Name: "a.txt"},
			{Name: "b.txt"},
		}, s.ChildrenOf("/root/docs/"))
	})

	t.Run("interned dir with no entries", func(t *testing.T) {
		// src/main.go's parent has children; a fresh empty dir entry
		// interns its own path but has no children yet.
		mustAdd(t, s, "/root", "empty", true)
		require.Nil(t, s.ChildrenOf("/root/empty"))
	})
}

func TestChildrenOfTombstonesAndResurrection(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)

	// A tombstoned child disappears from its parent's listing.
	s.RemoveByPath("/root/docs/a.txt")
	require.ElementsMatch(t, []ChildInfo{{Name: "b.txt"}}, s.ChildrenOf("/root/docs"))

	// A whole-subtree tombstone empties the removed dir AND drops the
	// dir from its own parent's listing.
	s.RemoveByPath("/root/docs")
	require.Nil(t, s.ChildrenOf("/root/docs"), "no live children left")
	require.ElementsMatch(t, []ChildInfo{
		{Name: "src", IsDir: true},
		{Name: "top.txt", IsDir: false},
	}, s.ChildrenOf("/root"))

	// Resurrecting the dir and one child brings both listings back.
	mustAdd(t, s, "/root", "docs", true)
	mustAdd(t, s, "/root/docs", "a.txt", false)
	require.ElementsMatch(t, []ChildInfo{{Name: "a.txt"}}, s.ChildrenOf("/root/docs"))
	require.ElementsMatch(t, []ChildInfo{
		{Name: "docs", IsDir: true},
		{Name: "src", IsDir: true},
		{Name: "top.txt", IsDir: false},
	}, s.ChildrenOf("/root"))
}

func TestChildrenOfUnicodeNames(t *testing.T) {
	// Names round-trip in original case; non-ASCII text stays escaped
	// in this source file by convention (see TestUnicodeNames).
	s := NewStore()
	mustAdd(t, s, "/u", "\u0130stanbul", true)
	mustAdd(t, s, "/u", "Stra\u1e9eE.txt", false)
	require.ElementsMatch(t, []ChildInfo{
		{Name: "\u0130stanbul", IsDir: true},
		{Name: "Stra\u1e9eE.txt", IsDir: false},
	}, s.ChildrenOf("/u"))
}

func TestForEachLive(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)
	s.RemoveByPath("/root/top.txt")

	var seen []string
	s.ForEachLive(func(id int32) bool {
		seen = append(seen, s.Name(id))
		return true
	})
	require.Len(t, seen, 5)
	require.NotContains(t, seen, "top.txt")

	// Early stop.
	calls := 0
	s.ForEachLive(func(id int32) bool {
		calls++
		return calls < 2
	})
	require.Equal(t, 2, calls)
}
