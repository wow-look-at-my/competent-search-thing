package appctx

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// ProcTree must satisfy the frecency seam it was built for.
var _ frecency.ProcTree = (*ProcTree)(nil)

// writeProc adds one process to a fixture proc root: a stat line with
// the given comm/ppid/tpgid and, when cwd is non-empty, a cwd symlink
// to it (the target directory is created).
func writeProc(t *testing.T, root string, pid int, comm string, ppid, tpgid int, cwd string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stat := strconv.Itoa(pid) + " (" + comm + ") S " +
		strconv.Itoa(ppid) + " " + strconv.Itoa(pid) + " " + strconv.Itoa(pid) +
		" 34816 " + strconv.Itoa(tpgid) + " 4194304 0 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	if cwd != "" {
		require.NoError(t, os.MkdirAll(cwd, 0o755))
		require.NoError(t, os.Symlink(cwd, filepath.Join(dir, "cwd")))
	}
}

func TestProcTreeChildrenAndCwd(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	writeProc(t, root, 100, "terminal", 1, -1, "")
	writeProc(t, root, 210, "bash", 100, 300, filepath.Join(work, "proj"))
	writeProc(t, root, 205, "bash", 100, -1, filepath.Join(work, "other"))
	writeProc(t, root, 300, "vim", 210, 300, filepath.Join(work, "proj", "sub"))
	// Non-numeric entries and unreadable stats are skipped quietly.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "self"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "999"), 0o755)) // no stat file

	tree := NewProcTree(root)
	require.Equal(t, []int{205, 210}, tree.Children(100), "children sorted numerically")
	require.Equal(t, []int{300}, tree.Children(210))
	require.Nil(t, tree.Children(300))
	require.Nil(t, tree.Children(4242), "unknown pid has no children")

	cwd, err := tree.Cwd(210)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(work, "proj"), cwd)
	_, err = tree.Cwd(100)
	require.Error(t, err, "a process without a readable cwd link errors (cross-user /proc)")
}

func TestProcTreeForeground(t *testing.T) {
	root := t.TempDir()
	// A terminal (no controlling-terminal tpgid) whose shell reports
	// vim's pid as the foreground process group.
	writeProc(t, root, 100, "terminal", 1, -1, "")
	writeProc(t, root, 210, "bash", 100, 300, "")
	writeProc(t, root, 300, "vim", 210, 300, "")

	tree := NewProcTree(root)
	fg, ok := tree.Foreground(100)
	require.True(t, ok)
	require.Equal(t, 300, fg, "the shell's tpgid names the foreground process")

	// Starting at a process that itself has a tpgid returns it.
	fg, ok = tree.Foreground(210)
	require.True(t, ok)
	require.Equal(t, 300, fg)

	// A tree with no controlling terminal anywhere has no hint.
	writeProc(t, root, 400, "gui-app", 1, -1, "")
	writeProc(t, root, 410, "helper", 400, -1, "")
	tree2 := NewProcTree(root)
	_, ok = tree2.Foreground(400)
	require.False(t, ok)

	// An unknown pid has no hint either.
	_, ok = tree2.Foreground(31337)
	require.False(t, ok)
}

func TestProcTreeStatParsing(t *testing.T) {
	// comm may contain spaces and parentheses; parsing starts after
	// the LAST ')'.
	ppid, tpgid, ok := parseStatFields([]byte("42 (my (weird) app) R 7 42 42 34816 99 0 0\n"))
	require.True(t, ok)
	require.Equal(t, 7, ppid)
	require.Equal(t, 99, tpgid)

	_, _, ok = parseStatFields([]byte("no closing paren"))
	require.False(t, ok)
	_, _, ok = parseStatFields([]byte("42 (short) R 7"))
	require.False(t, ok, "too few fields after comm")
	_, _, ok = parseStatFields([]byte("42 (x) R notanint 1 1 1 1"))
	require.False(t, ok)
	_, _, ok = parseStatFields([]byte("42 (x) R 7 1 1 1 notanint"))
	require.False(t, ok)
}

func TestProcTreeMissingRoot(t *testing.T) {
	tree := NewProcTree(filepath.Join(t.TempDir(), "nope"))
	require.Nil(t, tree.Children(1))
	_, ok := tree.Foreground(1)
	require.False(t, ok)
	_, err := tree.Cwd(1)
	require.Error(t, err)
}

func TestProcTreeDeriveCwdEndToEnd(t *testing.T) {
	// The production pair: appctx.ProcTree feeding frecency.DeriveCwd.
	// The terminal parks at "/" (not meaningful), bash sits in the
	// project dir, and bash's tpgid points at vim one level deeper --
	// the foreground hint wins.
	root := t.TempDir()
	work := t.TempDir()
	proj := filepath.Join(work, "proj")
	deep := filepath.Join(proj, "sub")
	writeProc(t, root, 100, "terminal", 1, -1, "/")
	writeProc(t, root, 210, "bash", 100, 300, proj)
	writeProc(t, root, 300, "vim", 210, 300, deep)

	cwd, ok := frecency.DeriveCwd(NewProcTree(root), 100)
	require.True(t, ok)
	require.Equal(t, deep, cwd)
}
