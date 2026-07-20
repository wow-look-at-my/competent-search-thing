package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// canonical is the expectation-side EvalSymlinks: t.TempDir itself can
// sit behind a symlink (darwin's /var -> /private/var), so expected
// values are canonicalized the same way the helper resolves.
func canonical(t *testing.T, path string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return r
}

func TestResolvedExecutableResolvesSymlinkToRealFile(t *testing.T) {
	// The motivating field case: the Homebrew bin/ shim is a symlink
	// into the versioned Cellar, and setcap refuses symlinks -- the
	// resolved spelling is the pasteable one.
	exe, shim := brewLayout(t, t.TempDir(), "1.2.3")

	got, ok := ResolvedExecutable(shim)
	require.True(t, ok)
	require.Equal(t, canonical(t, exe), got, "the symlink resolves to the real Cellar file")
	fi, err := os.Lstat(got)
	require.NoError(t, err)
	require.Zero(t, fi.Mode()&os.ModeSymlink, "the result is the regular file, never a link")
}

func TestResolvedExecutableFollowsChainedLinks(t *testing.T) {
	// opt -> Cellar -> real file: multi-hop layouts (brew's opt dir,
	// stow) resolve all the way down.
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "store", "v2", "tool"))
	mid := filepath.Join(root, "opt", "tool")
	require.NoError(t, os.MkdirAll(filepath.Dir(mid), 0o755))
	require.NoError(t, os.Symlink(exe, mid))
	link := filepath.Join(root, "bin", "tool")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(mid, link))

	got, ok := ResolvedExecutable(link)
	require.True(t, ok)
	require.Equal(t, canonical(t, exe), got)
}

func TestResolvedExecutablePlainFileAndRelative(t *testing.T) {
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "bin", "tool"))

	got, ok := ResolvedExecutable(exe)
	require.True(t, ok)
	require.Equal(t, canonical(t, exe), got, "a regular file resolves to itself (canonicalized)")

	// A relative path is Abs-resolved against the working directory
	// before following links.
	t.Chdir(root)
	got, ok = ResolvedExecutable(filepath.Join("bin", "tool"))
	require.True(t, ok)
	require.Equal(t, canonical(t, exe), got)
	require.True(t, filepath.IsAbs(got))
}

func TestResolvedExecutableFailures(t *testing.T) {
	_, ok := ResolvedExecutable("")
	require.False(t, ok, "empty input never resolves")

	_, ok = ResolvedExecutable(filepath.Join(t.TempDir(), "does-not-exist"))
	require.False(t, ok, "a missing path never resolves")

	// A dangling symlink has no real target: the caller keeps its
	// previous spelling (the hint's StableExecutable fallback).
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(root, "gone"), dangling))
	_, ok = ResolvedExecutable(dangling)
	require.False(t, ok)
}
