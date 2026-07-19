package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	ran := make(chan string, 3)
	s.SetHandlers(Handlers{
		Toggle: func() { ran <- CmdToggle },
		Show:   func() { ran <- CmdShow },
		Hide:   func() { ran <- CmdHide },
	})
	for _, cmd := range []string{CmdToggle, CmdShow, CmdHide} {
		resp, err := Send(path, cmd, time.Second)
		require.NoError(t, err, cmd)
		require.Equal(t, ReplyOK, resp, cmd)
		// The handler runs after the reply is written: wait for its
		// signal instead of asserting it already ran.
		select {
		case got := <-ran:
			require.Equal(t, cmd, got)
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s handler never ran", cmd)
		}
	}
}

func TestNilHandlerMemberIsNotReady(t *testing.T) {
	s, path := listen(t, "v")
	ran := make(chan struct{}, 1)
	s.SetHandlers(Handlers{Toggle: func() { ran <- struct{}{} }})

	resp, err := Send(path, CmdShow, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyNotReady, resp, "nil Show member answers not ready")

	resp, err = Send(path, CmdToggle, time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the toggle handler never ran")
	}
}

func TestReplyArrivesBeforeHandlerFinishes(t *testing.T) {
	s, path := listen(t, "v")
	entered := make(chan struct{})
	release := make(chan struct{})
	var unblock sync.Once
	// Whatever happens below, never leave the handler -- and with it
	// the server's Close -- blocked past the end of the test.
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	s.SetHandlers(Handlers{Toggle: func() {
		close(entered)
		<-release
	}})

	// The acknowledgement must arrive while the handler is blocked:
	// the old order (handler first) would sit on <-release until the
	// client deadline killed the exchange with an i/o timeout.
	start := time.Now()
	resp, err := Send(path, CmdToggle, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, ReplyOK, resp)
	require.Less(t, time.Since(start), time.Second, "the reply must not wait for the handler")

	// The handler is provably still mid-flight: wait for its entry
	// signal (the reply can beat the handler's first statement), and
	// nothing has released it yet.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the toggle handler never started")
	}

	// Close must keep waiting for the in-flight handler (the conn
	// goroutine running it is tracked by the same WaitGroup) ...
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while the handler was still blocked")
	case <-time.After(50 * time.Millisecond):
		// Bounded no-progress check, not synchronization: a buggy
		// Close would have delivered by now; a correct one is parked
		// in wg.Wait until the handler is released below.
	}

	// ... and return once the handler finishes.
	unblock.Do(func() { close(release) })
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the handler was released")
	}
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
	const n = 24
	done := make(chan struct{}, n)
	s.SetHandlers(Handlers{Toggle: func() { done <- struct{}{} }})

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
	// Handlers run after their replies: collect all n invocation
	// signals instead of asserting a count right after the sends.
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d toggle handlers ran", i, n)
		}
	}
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
