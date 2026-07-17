package index

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFootprintEmptyStore(t *testing.T) {
	f := NewStore().Footprint()
	require.Zero(t, f.Entries)
	require.Zero(t, f.LiveEntries)
	require.Zero(t, f.Dirs)
	require.Equal(t, int64(8), f.OffsetBytes, "the two offset tables hold their leading 0 each")
	require.Equal(t, int64(8), f.TotalBytes)
	require.Zero(t, f.BytesPerEntry(), "no division by zero on an empty store")
}

func TestFootprintKnownTree(t *testing.T) {
	st := NewStore()
	mustAdd := func(dir, name string, isDir bool) {
		_, err := st.AddEntry(dir, name, isDir)
		require.NoError(t, err)
	}
	mustAdd("/Data", "Foo", false)
	mustAdd("/Data", "sub", true)
	mustAdd("/Data/sub", "Bar.txt", false)
	require.Equal(t, 1, st.RemoveByPath("/Data/Foo"))

	f := st.Footprint()
	require.Equal(t, 3, f.Entries)
	require.Equal(t, 2, f.LiveEntries)
	require.Equal(t, 2, f.Dirs, `"/Data" and "/Data/sub"`)

	// Exact columns: names "foo"+"sub"+"bar.txt" plus one separator
	// each = 16 bytes per blob; offsets are 2 tables of 4 uint32s;
	// parent 3 uint32s; flags 3 bytes.
	require.Equal(t, int64(16), f.NameLowerBytes)
	require.Equal(t, int64(16), f.NameOrigBytes)
	require.Equal(t, int64(32), f.OffsetBytes)
	require.Equal(t, int64(12), f.ParentBytes)
	require.Equal(t, int64(3), f.FlagBytes)

	// Dir columns: "/Data" (5) + "/Data/sub" (9) = 14 bytes; both
	// lowered forms differ from the originals, so the lowered copies
	// are separate allocations counted in full; 4 string headers.
	require.Equal(t, int64(14), f.DirStringBytes)
	require.Equal(t, int64(14), f.DirLowerExtraBytes)
	require.Equal(t, int64(64), f.DirHeaderBytes)

	// The dirIndex approximation is a deterministic formula over
	// len(key): (16+4+48)*2 entries + 14 key bytes.
	require.Equal(t, int64(150), f.DirIndexApproxBytes)

	// children caps depend on append growth, so bound it: at least the
	// len-based floor (2 map entries, 3 ids total), at most a doubling
	// of every slice.
	floor := int64(2*(24+48) + 4*3)
	require.GreaterOrEqual(t, f.ChildrenApproxBytes, floor)
	require.LessOrEqual(t, f.ChildrenApproxBytes, floor+4*3)

	sum := f.NameLowerBytes + f.NameOrigBytes + f.OffsetBytes +
		f.ParentBytes + f.FlagBytes + f.DirStringBytes +
		f.DirLowerExtraBytes + f.DirHeaderBytes +
		f.DirIndexApproxBytes + f.ChildrenApproxBytes
	require.Equal(t, sum, f.TotalBytes)
	require.InDelta(t, float64(sum)/3, f.BytesPerEntry(), 0.001)
}

func TestFootprintLowercaseDirsShareBytes(t *testing.T) {
	st := NewStore()
	_, err := st.AddEntry("/all/lower", "file.txt", false)
	require.NoError(t, err)
	f := st.Footprint()
	require.Equal(t, int64(10), f.DirStringBytes)
	require.Zero(t, f.DirLowerExtraBytes,
		"an already-lowercase dir path shares its bytes with the lowered column")
}

func TestManagerFootprintPassthrough(t *testing.T) {
	root, want := makeDiskTree(t, 3, 4)
	m := NewManager([]string{root}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)

	f := m.Footprint()
	require.Equal(t, want, f.Entries)
	require.Equal(t, want, f.LiveEntries)
	require.Positive(t, f.TotalBytes)
	require.Positive(t, f.BytesPerEntry())
	require.Equal(t, 4, f.Dirs, "the root plus three subdirectories")
}
