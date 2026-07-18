package preview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListCappedSortsDirsFirstCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "zeta"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "Alpha"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "beta.txt"), []byte("12345"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ALPHA.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nested-not-recursed"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Alpha", "inner.txt"), []byte("y"), 0o644))

	dp, err := ListCapped(dir, 100)
	require.NoError(t, err)
	require.Equal(t, 5, dp.Total)
	require.False(t, dp.Truncated)
	names := make([]string, len(dp.Entries))
	for i, e := range dp.Entries {
		names[i] = e.Name
	}
	require.Equal(t, []string{"Alpha", "zeta", "ALPHA.txt", "beta.txt", "nested-not-recursed"}, names,
		"directories first, then files, each case-insensitively sorted; never recursed")
	require.True(t, dp.Entries[0].IsDir)
	require.True(t, dp.Entries[1].IsDir)
	require.False(t, dp.Entries[3].IsDir)
	require.Equal(t, int64(5), dp.Entries[3].Size, "beta.txt size from entry.Info()")
	require.Equal(t, int64(0), dp.Entries[4].Size)
}

func TestListCappedTruncates(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	dp, err := ListCapped(dir, 3)
	require.NoError(t, err)
	require.Len(t, dp.Entries, 3)
	require.Equal(t, 5, dp.Total)
	require.True(t, dp.Truncated)
	require.Equal(t, "a", dp.Entries[0].Name)
}

func TestListCappedEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	dp, err := ListCapped(dir, 10)
	require.NoError(t, err)
	require.NotNil(t, dp.Entries, "entries are never nil")
	require.Empty(t, dp.Entries)
	require.Equal(t, 0, dp.Total)
	require.False(t, dp.Truncated)

	_, err = ListCapped(filepath.Join(dir, "missing"), 10)
	require.Error(t, err)
}

func TestListCappedZeroMaxMeansUncapped(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	dp, err := ListCapped(dir, 0)
	require.NoError(t, err)
	require.Len(t, dp.Entries, 3)
	require.False(t, dp.Truncated)
}
