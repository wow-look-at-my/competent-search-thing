package appctx

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcInfo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte("myproc\n"), 0o644))
	// The symlink may dangle (like a deleted binary); Readlink still
	// reports its target.
	require.NoError(t, os.Symlink("/usr/bin/myproc", filepath.Join(dir, "exe")))

	exe, comm := ProcInfo(root, 42)
	require.Equal(t, "/usr/bin/myproc", exe)
	require.Equal(t, "myproc", comm)
}

func TestProcInfoMissingPieces(t *testing.T) {
	root := t.TempDir()

	exe, comm := ProcInfo(root, 1)
	require.Empty(t, exe, "missing pid")
	require.Empty(t, comm, "missing pid")

	// comm readable but exe not: the everyday cross-user /proc case.
	dir := filepath.Join(root, "7")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte("noexe"), 0o644))
	exe, comm = ProcInfo(root, 7)
	require.Empty(t, exe)
	require.Equal(t, "noexe", comm)
}
