package ipc

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPeerCredOfSelfConnection(t *testing.T) {
	path := testSocket(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	// Deliberately never accepted: SO_PEERCRED / LOCAL_PEERPID must
	// answer for a backlog-queued connection too (the frozen-daemon
	// case the takeover ladder depends on).
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	pid, uid, ok := peerCredOf(conn)
	switch runtime.GOOS {
	case "linux", "darwin":
		require.True(t, ok)
		require.Equal(t, os.Getpid(), pid, "the listener's pid, captured at connect time")
		require.Equal(t, os.Getuid(), uid)
	default:
		require.False(t, ok, "no peer credentials on this platform")
	}
}

func TestProcIdentAt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "4242")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	target := filepath.Join(root, "competent-search-thing")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "exe")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte("competent-searc\n"), 0o644))

	exe, comm := procIdentAt(root, 4242)
	require.Equal(t, target, exe)
	require.Equal(t, "competent-searc", comm, "comm arrives kernel-truncated and newline-trimmed")

	exe, comm = procIdentAt(root, 999)
	require.Empty(t, exe, "a vanished pid reads as absent metadata")
	require.Empty(t, comm)
}
