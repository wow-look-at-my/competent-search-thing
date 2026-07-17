package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testSocket returns a fresh, short socket path. unix socket paths are
// limited to ~104 bytes on darwin (108 on linux), and t.TempDir embeds
// the full test name -- on macOS, where TMPDIR is already ~50 bytes of
// /var/folders/..., a long test name overflows sun_path and bind fails
// with EINVAL. A bare MkdirTemp keeps the whole path short on every OS.
func testSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// listen starts a server on a fresh socket and closes it at test end.
func listen(t *testing.T, version string) (*Server, string) {
	t.Helper()
	path := testSocket(t)
	s, err := Listen(path, version)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestSocketPath(t *testing.T) {
	fallback := filepath.Join(os.TempDir(), fmt.Sprintf("competent-search-thing-%d.sock", os.Getuid()))
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "override wins",
			env:  map[string]string{EnvSocket: "/run/x.sock", "XDG_RUNTIME_DIR": "/run/user/7"},
			want: "/run/x.sock",
		},
		{
			name: "xdg runtime dir",
			env:  map[string]string{"XDG_RUNTIME_DIR": "/run/user/7"},
			want: "/run/user/7/competent-search-thing.sock",
		},
		{
			name: "temp dir fallback",
			env:  map[string]string{},
			want: fallback,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			require.Equal(t, tt.want, SocketPath(getenv))
		})
	}
}

func TestVersionAndPingWorkBeforeHandlers(t *testing.T) {
	_, path := listen(t, "1.2.3-test")

	resp, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)

	resp, err = Send(path, CmdVersion, time.Second)
	require.NoError(t, err)
	require.Equal(t, "1.2.3-test", resp)
}

func TestCommandsBeforeSetHandlersAreNotReady(t *testing.T) {
	_, path := listen(t, "v")
	for _, cmd := range []string{CmdToggle, CmdShow, CmdHide} {
		resp, err := Send(path, cmd, time.Second)
		require.NoError(t, err, cmd)
		require.Equal(t, ReplyNotReady, resp, cmd)
	}
}

func TestHandlersInvoked(t *testing.T) {
	s, path := listen(t, "v")
	var toggles, shows, hides atomic.Int32
	s.SetHandlers(Handlers{
		Toggle: func() { toggles.Add(1) },
		Show:   func() { shows.Add(1) },
		Hide:   func() { hides.Add(1) },
	})
	for _, cmd := range []string{CmdToggle, CmdShow, CmdHide} {
		resp, err := Send(path, cmd, time.Second)
		require.NoError(t, err, cmd)
		require.Equal(t, ReplyOK, resp, cmd)
	}
	require.Equal(t, int32(1), toggles.Load())
	require.Equal(t, int32(1), shows.Load())
	require.Equal(t, int32(1), hides.Load())
}

func TestNilHandlerMemberIsNotReady(t *testing.T) {
	s, path := listen(t, "v")
	var toggles atomic.Int32
	s.SetHandlers(Handlers{Toggle: func() { toggles.Add(1) }})

	resp, err := Send(path, CmdShow, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyNotReady, resp, "nil Show member answers not ready")

	resp, err = Send(path, CmdToggle, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)
	require.Equal(t, int32(1), toggles.Load())
}

func TestUnknownAndEmptyCommands(t *testing.T) {
	_, path := listen(t, "v")
	for _, cmd := range []string{"bogus", "", "TOGGLE"} {
		resp, err := Send(path, cmd, time.Second)
		require.NoError(t, err, "cmd %q", cmd)
		require.Equal(t, replyUnknown, resp, "cmd %q", cmd)
	}
}

func TestRawConnectionCRLFAndEOFTermination(t *testing.T) {
	_, path := listen(t, "v")

	// CRLF-terminated request still parses (TrimSpace).
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	_, err = conn.Write([]byte(CmdPing + "\r\n"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, ReplyOK+"\n", string(buf[:n]))
	require.NoError(t, conn.Close())

	// A request terminated by closing the write side (no newline)
	// also parses.
	uc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	_, err = uc.Write([]byte(CmdPing))
	require.NoError(t, err)
	require.NoError(t, uc.CloseWrite())
	n, err = uc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, ReplyOK+"\n", string(buf[:n]))
	require.NoError(t, uc.Close())
}

func TestConnectionWithoutRequestIsHarmless(t *testing.T) {
	_, path := listen(t, "v")
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	require.NoError(t, conn.Close()) // no request at all

	resp, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err, "server keeps serving after an empty connection")
	require.Equal(t, ReplyOK, resp)
}

func TestSecondInstanceGetsErrAlreadyRunning(t *testing.T) {
	_, path := listen(t, "v")
	s2, err := Listen(path, "v")
	require.Nil(t, s2)
	require.ErrorIs(t, err, ErrAlreadyRunning)
}

func TestStaleSocketIsRecovered(t *testing.T) {
	path := testSocket(t)

	// Simulate a crashed instance: a listener whose socket file
	// survives its close.
	addr, err := net.ResolveUnixAddr("unix", path)
	require.NoError(t, err)
	stale, err := net.ListenUnix("unix", addr)
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "the stale socket file is still on disk")

	s, err := Listen(path, "v")
	require.NoError(t, err, "Listen recovers the stale socket")
	defer func() { _ = s.Close() }()

	resp, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)
}

func TestRegularFileAtSocketPathIsRecovered(t *testing.T) {
	path := testSocket(t)
	require.NoError(t, os.WriteFile(path, []byte("junk"), 0o600))

	s, err := Listen(path, "v")
	require.NoError(t, err, "a non-socket file at the path is treated as stale")
	defer func() { _ = s.Close() }()

	resp, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)
}

func TestListenErrorsOnUnusablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "s.sock")
	s, err := Listen(path, "v")
	require.Nil(t, s)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrAlreadyRunning))
}

func TestSocketFileIsOwnerOnly(t *testing.T) {
	_, path := listen(t, "v")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestConcurrentSends(t *testing.T) {
	s, path := listen(t, "v")
	var toggles atomic.Int32
	s.SetHandlers(Handlers{Toggle: func() { toggles.Add(1) }})

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	resps := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resps[i], errs[i] = Send(path, CmdToggle, 5*time.Second)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "send %d", i)
		require.Equal(t, ReplyOK, resps[i], "send %d", i)
	}
	require.Equal(t, int32(n), toggles.Load())
}

func TestCloseIsIdempotentAndUnlinks(t *testing.T) {
	path := testSocket(t)
	s, err := Listen(path, "v")
	require.NoError(t, err)

	require.NoError(t, s.Close())
	require.NoError(t, s.Close(), "second Close is a no-op")

	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "closing unlinks the socket file")

	_, err = Send(path, CmdPing, 200*time.Millisecond)
	require.Error(t, err)
	require.True(t, IsNotRunning(err), "a closed server counts as not running")
}

func TestNilServerIsSafe(t *testing.T) {
	var s *Server
	s.SetHandlers(Handlers{Toggle: func() {}})
	require.NoError(t, s.Close())
}

func TestSendToMissingSocketIsNotRunning(t *testing.T) {
	path := testSocket(t) // never listened on
	resp, err := Send(path, CmdPing, 200*time.Millisecond)
	require.Empty(t, resp)
	require.Error(t, err)
	require.True(t, IsNotRunning(err))
}

func TestIsNotRunning(t *testing.T) {
	require.False(t, IsNotRunning(nil))
	require.False(t, IsNotRunning(errors.New("boom")))
	require.True(t, IsNotRunning(fmt.Errorf("wrap: %w", ErrNotRunning)))
}

func TestSendTimesOutOnSilentServer(t *testing.T) {
	path := testSocket(t)
	// A listener that never accepts: the dial succeeds (backlog), the
	// write is buffered, and the response read must hit the deadline.
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	start := time.Now()
	_, err = Send(path, CmdPing, 250*time.Millisecond)
	require.Error(t, err)
	require.False(t, IsNotRunning(err), "a wedged instance is not 'not running'")
	require.Less(t, time.Since(start), 5*time.Second, "the deadline bounds the exchange")
}
