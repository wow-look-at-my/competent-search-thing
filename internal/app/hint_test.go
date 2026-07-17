package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// hintFixture builds an app over an index rooted at a real temp tree
// (one indexed file), with a second existing-but-unindexed tree
// outside the roots, and points the lstat seam at the real os.Lstat.
func hintFixture(t *testing.T) (a *App, r *seamRecorder, root, outside string) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "indexed.txt"), []byte("x"), 0o644))
	outside = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "sub", "hosts"), []byte("x"), 0o644))

	m := index.NewManager([]string{root}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	a, r = newTestApp(t, m, Options{})
	a.plat.lstat = os.Lstat
	return a, r, root, outside
}

func TestSearchOutsideRootsHint(t *testing.T) {
	a, r, _, outside := hintFixture(t)
	target := filepath.Join(outside, "sub", "hosts")

	res := a.Search(target)
	require.Len(t, res, 1, "an existing outside-roots path yields exactly one synthetic result")
	require.Equal(t, target, res[0].Path)
	require.Equal(t, "hosts", res[0].Name)
	require.False(t, res[0].IsDir)
	require.Equal(t, "outside indexed roots -- add /tmp to roots in config.json", res[0].Hint,
		"the hint names the query's first path component")

	// A directory query carries IsDir; surrounding whitespace is
	// trimmed like any query.
	res = a.Search("  " + filepath.Join(outside, "sub") + "  ")
	require.Len(t, res, 1)
	require.True(t, res[0].IsDir)

	// Enter must work on the synthetic row: Open takes the path
	// directly, no index involved.
	require.NoError(t, a.Open(target))
	require.True(t, r.has("open:"+target))
}

func TestSearchHintRequiresAllConditions(t *testing.T) {
	a, _, root, outside := hintFixture(t)

	// Inside the roots but missing from the index (created after the
	// build): an indexing gap, not a roots gap -- no hint.
	late := filepath.Join(root, "created-after-build.txt")
	require.NoError(t, os.WriteFile(late, []byte("x"), 0o644))
	require.Empty(t, a.Search(late))

	// Nonexistent absolute path: nothing to point at -- no hint.
	require.Empty(t, a.Search(filepath.Join(outside, "no-such-file")))

	// Relative query that matches nothing: no hint.
	require.Empty(t, a.Search("definitely-not-indexed-anywhere"))

	// Index results win: an indexed name that also exists stays a
	// normal result list, hint-free.
	res := a.Search("indexed.txt")
	require.NotEmpty(t, res)
	for _, r := range res {
		require.Empty(t, r.Hint)
	}
}

func TestSearchHintUsesLstatSeam(t *testing.T) {
	// newTestApp pins lstat to "nothing exists": even a path that IS
	// real on this machine must produce no hint, proving Search goes
	// through the seam rather than the disk.
	m := index.NewManager([]string{t.TempDir()}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	a, _ := newTestApp(t, m, Options{})
	require.Empty(t, a.Search("/etc"))
}

func TestPathWithinAny(t *testing.T) {
	roots := []string{"/data", "/home/me/"}
	cases := map[string]bool{
		"/data":         true,
		"/data/sub/x":   true,
		"/database":     false, // prefix of the string, not of the path
		"/home/me":      true,
		"/home/me/docs": true,
		"/home/metals":  false,
		"/etc/hosts":    false,
		"/":             false,
	}
	for p, want := range cases {
		require.Equal(t, want, pathWithinAny(p, roots), p)
	}
	require.False(t, pathWithinAny("/anything", nil))
	require.True(t, pathWithinAny("/anything", []string{"/"}),
		"the whole-filesystem root covers everything")
}

func TestTopComponent(t *testing.T) {
	cases := map[string]string{
		"/etc/hosts":   "/etc",
		"/etc":         "/etc",
		"/":            "/",
		"/a/b/c/d":     "/a",
		"/mnt/nfs/sub": "/mnt",
	}
	for p, want := range cases {
		require.Equal(t, want, topComponent(p), p)
	}
}
