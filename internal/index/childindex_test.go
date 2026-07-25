package index

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// findChild switches from a linear scan to the cached name lookup at
// childIndexMin, and the cache describes exactly ONE directory. These
// tests drive both sides of that threshold and every way the cached
// directory can change underneath it.

// refFindChild is the pre-cache implementation: a plain first-match
// scan of the directory's children.
func refFindChild(s *Store, pid uint32, name string) int32 {
	for _, id := range s.children[pid] {
		if string(s.nameBytes(id)) == name {
			return id
		}
	}
	return -1
}

// buildDir adds n entries named file_<k> under dir and returns it.
func buildDir(t *testing.T, st *Store, dir string, n int) {
	t.Helper()
	for k := 0; k < n; k++ {
		_, err := st.AddEntry(dir, "file_"+itoa(k), false)
		require.NoError(t, err)
	}
}

// TestFindChildMatchesScanBothSidesOfThreshold sweeps directory sizes
// straddling childIndexMin and requires findChild to answer exactly
// what the scan would, for present and absent names alike.
func TestFindChildMatchesScanBothSidesOfThreshold(t *testing.T) {
	for _, n := range []int{0, 1, childIndexMin - 1, childIndexMin, childIndexMin + 1, 300} {
		st := NewStore()
		buildDir(t, st, "/d", n)
		pid := st.dirIndex["/d"]
		for k := 0; k < n+5; k++ {
			name := "file_" + itoa(k)
			require.Equal(t, refFindChild(st, pid, name), st.findChild(pid, name),
				"n=%d name=%q", n, name)
		}
		require.Equal(t, int32(-1), st.findChild(pid, "absent"), "n=%d", n)
	}
}

// TestFindChildAcrossDirectorySwitches thrashes the single-directory
// cache: two large directories queried alternately must each keep
// answering for themselves.
func TestFindChildAcrossDirectorySwitches(t *testing.T) {
	st := NewStore()
	n := childIndexMin + 20
	buildDir(t, st, "/a", n)
	buildDir(t, st, "/b", n)
	// Give /b a name /a does not have, and vice versa.
	_, err := st.AddEntry("/b", "only_b", false)
	require.NoError(t, err)
	_, err = st.AddEntry("/a", "only_a", false)
	require.NoError(t, err)

	pa, pb := st.dirIndex["/a"], st.dirIndex["/b"]
	for i := 0; i < 4; i++ {
		require.Equal(t, refFindChild(st, pa, "only_a"), st.findChild(pa, "only_a"))
		require.Equal(t, int32(-1), st.findChild(pa, "only_b"), "/a must not see /b's child")
		require.Equal(t, refFindChild(st, pb, "only_b"), st.findChild(pb, "only_b"))
		require.Equal(t, int32(-1), st.findChild(pb, "only_a"), "/b must not see /a's child")
		for k := 0; k < n; k += 13 {
			name := "file_" + itoa(k)
			require.Equal(t, refFindChild(st, pa, name), st.findChild(pa, name))
			require.Equal(t, refFindChild(st, pb, name), st.findChild(pb, name))
		}
	}
}

// TestFindChildSeesLaterAppends pins the incremental update: entries
// appended to the CACHED directory must become findable without a
// rebuild, and appends to other directories must not leak into it.
func TestFindChildSeesLaterAppends(t *testing.T) {
	st := NewStore()
	buildDir(t, st, "/d", childIndexMin+5)
	pid := st.dirIndex["/d"]
	require.Equal(t, int32(-1), st.findChild(pid, "later")) // populates the cache
	require.True(t, st.childIdxOK && st.childIdxID == pid, "cache should describe /d")

	id, err := st.AddEntry("/d", "later", false)
	require.NoError(t, err)
	require.Equal(t, id, st.findChild(pid, "later"), "append must be visible")
	require.Equal(t, refFindChild(st, pid, "later"), st.findChild(pid, "later"))

	// An append to a DIFFERENT directory must not appear in /d.
	_, err = st.AddEntry("/other", "elsewhere", false)
	require.NoError(t, err)
	require.Equal(t, int32(-1), st.findChild(pid, "elsewhere"))
}

// TestFindChildAfterRemoveAndResurrect covers the store's mutation
// semantics through the cache: tombstoning leaves the entry in
// children (findChild still finds it), and re-adding the same name
// must reuse that id rather than appending a duplicate.
func TestFindChildAfterRemoveAndResurrect(t *testing.T) {
	st := NewStore()
	buildDir(t, st, "/d", childIndexMin+5)
	pid := st.dirIndex["/d"]
	want := st.findChild(pid, "file_3")
	require.GreaterOrEqual(t, want, int32(0))

	before := st.Len()
	require.Equal(t, 1, st.RemoveByPath("/d/file_3"))
	require.Equal(t, want, st.findChild(pid, "file_3"),
		"a tombstoned entry stays in children and stays findable")

	got, err := st.AddEntry("/d", "file_3", false)
	require.NoError(t, err)
	require.Equal(t, want, got, "re-adding must resurrect the same id")
	require.Equal(t, before, st.Len(), "resurrection must not append a duplicate")
	require.Equal(t, before, st.LiveCount()+0, "the entry is live again")
}

// TestFindChildKindFlipThroughCache exercises AddEntry's dir-bit
// refresh on a cached large directory.
func TestFindChildKindFlipThroughCache(t *testing.T) {
	st := NewStore()
	buildDir(t, st, "/d", childIndexMin+5)
	pid := st.dirIndex["/d"]
	id, err := st.AddEntry("/d", "flip", true)
	require.NoError(t, err)
	require.True(t, st.IsDir(id))
	require.Equal(t, id, st.findChild(pid, "flip"))

	same, err := st.AddEntry("/d", "flip", false)
	require.NoError(t, err)
	require.Equal(t, id, same, "same (parent, name) keeps its id")
	require.False(t, st.IsDir(same), "the dir bit must be cleared")
	require.Equal(t, refFindChild(st, pid, "flip"), st.findChild(pid, "flip"))
}

// TestBuildChildIndexKeepsFirstDuplicate pins the tie-break the cache
// inherits from the scan. Duplicates cannot arise through AddEntry, so
// this drives appendEntry directly.
func TestBuildChildIndexKeepsFirstDuplicate(t *testing.T) {
	st := NewStore()
	pid := st.internParentDir("/d")
	first := st.appendEntry(pid, "dup", "", false)
	for k := 0; k < childIndexMin; k++ {
		st.appendEntry(pid, "pad_"+itoa(k), "", false)
	}
	second := st.appendEntry(pid, "dup", "", false)
	require.NotEqual(t, first, second)
	require.Equal(t, first, refFindChild(st, pid, "dup"), "the scan takes the first")
	require.Equal(t, first, st.findChild(pid, "dup"), "the cache must agree")
}
