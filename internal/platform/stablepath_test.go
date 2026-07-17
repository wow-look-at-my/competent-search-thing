package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeExecutable creates an executable file at path (parents
// included) and returns path.
func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}

// brewLayout builds the Homebrew install shape: the real binary in a
// versioned Cellar directory plus a stable symlink shim in
// <root>/bin, which is put on PATH. Returns (cellar binary, shim).
func brewLayout(t *testing.T, root, version string) (exe, shim string) {
	t.Helper()
	exe = writeExecutable(t, filepath.Join(root, "Cellar", "competent-search-thing", version, "bin", "competent-search-thing"))
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	shim = filepath.Join(binDir, "competent-search-thing")
	require.NoError(t, os.Symlink(exe, shim))
	t.Setenv("PATH", binDir)
	return exe, shim
}

// emptyPath points PATH at a fresh directory containing nothing, so
// exec.LookPath deterministically misses.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestStableExecutablePrefersPathShimUnresolved(t *testing.T) {
	exe, shim := brewLayout(t, t.TempDir(), "1.2.3")

	got := StableExecutable(exe, "")

	require.Equal(t, shim, got, "the PATH shim wins over the resolved Cellar path")
	fi, err := os.Lstat(got)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&os.ModeSymlink, "the shim is returned UNRESOLVED -- the symlink is the stable part")
}

func TestStableExecutableRejectsForeignPathHit(t *testing.T) {
	// A different binary with the same name sits first in PATH (another
	// install, the wrong version): it must never be substituted for the
	// binary that is actually running.
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "opt", "competent-search-thing"))
	foreign := writeExecutable(t, filepath.Join(root, "bin", "competent-search-thing"))
	t.Setenv("PATH", filepath.Dir(foreign))

	require.Equal(t, exe, StableExecutable(exe, ""), "a same-named foreign PATH hit is rejected")
}

func TestStableExecutableFallsBackToArgs0Symlink(t *testing.T) {
	// Nothing on PATH, but the binary was launched through a symlink
	// outside PATH: argv[0] is the stable spelling.
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "store", "abc123", "competent-search-thing"))
	link := filepath.Join(root, "apps", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(exe, link))
	emptyPath(t)

	got := StableExecutable(exe, link)

	require.Equal(t, link, got)
	fi, err := os.Lstat(got)
	require.NoError(t, err)
	require.NotZero(t, fi.Mode()&os.ModeSymlink, "argv[0] is kept unresolved")
}

func TestStableExecutableResolvesRelativeArgs0(t *testing.T) {
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "bin", "competent-search-thing"))
	emptyPath(t)
	t.Chdir(root)

	got := StableExecutable(exe, filepath.Join("bin", "competent-search-thing"))

	require.Equal(t, exe, got, "a relative argv[0] is Abs-resolved before the guard")
	require.True(t, filepath.IsAbs(got))
}

func TestStableExecutableRejectsForeignArgs0(t *testing.T) {
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "a", "competent-search-thing"))
	other := writeExecutable(t, filepath.Join(root, "b", "competent-search-thing"))
	emptyPath(t)

	require.Equal(t, exe, StableExecutable(exe, other), "argv[0] naming a different file is rejected")
}

func TestStableExecutableFallsBackToExe(t *testing.T) {
	root := t.TempDir()
	exe := writeExecutable(t, filepath.Join(root, "deep", "competent-search-thing"))
	emptyPath(t)

	require.Equal(t, exe, StableExecutable(exe, ""), "no candidate: the resolved path stands")
	require.Equal(t, exe, StableExecutable(exe, filepath.Join(root, "missing")), "a dead argv[0] falls through")
}

func TestStableExecutableSurvivesBrewUpgrade(t *testing.T) {
	// The field scenario end-to-end: v1 registers the shim; after the
	// upgrade retargets the shim and deletes the old Cellar dir, the
	// new binary picks the SAME shim path -- the stored command never
	// has to change again.
	root := t.TempDir()
	oldExe, shim := brewLayout(t, root, "1.0.0")
	require.Equal(t, shim, StableExecutable(oldExe, ""))

	newExe := writeExecutable(t, filepath.Join(root, "Cellar", "competent-search-thing", "1.1.0", "bin", "competent-search-thing"))
	require.NoError(t, os.Remove(shim))
	require.NoError(t, os.Symlink(newExe, shim))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "Cellar", "competent-search-thing", "1.0.0")))

	require.Equal(t, shim, StableExecutable(newExe, ""), "the same stable path is chosen by every version")
}
