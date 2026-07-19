package ipc

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// rawExchange performs one wire-verbatim exchange: line out (newline
// appended), one raw reply line back with its trailing newline
// stripped and nothing else touched -- the byte-level view Send hides.
func rawExchange(t *testing.T, path, line string) string {
	t.Helper()
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte(line + "\n"))
	require.NoError(t, err)
	reply, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err, "no reply line to %q", line)
	return strings.TrimSuffix(reply, "\n")
}

// awaitSignal waits for one handler-invocation signal.
func awaitSignal(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		require.Equal(t, want, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("the %s handler never ran", want)
	}
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

	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.False(t, rep.Legacy, "Send speaks JSON natively against the new server")

	rep, err = Send(path, CmdVersion, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, "1.2.3-test", rep.Version)
	require.False(t, rep.Legacy)
}

func TestCommandsBeforeSetHandlersAreNotReady(t *testing.T) {
	_, path := listen(t, "v")
	for _, cmd := range []string{CmdToggle, CmdShow, CmdHide} {
		rep, err := Send(path, cmd, time.Second)
		require.NoError(t, err, cmd)
		require.False(t, rep.OK, cmd)
		require.True(t, rep.NotReady(), cmd)
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
		rep, err := Send(path, cmd, time.Second)
		require.NoError(t, err, cmd)
		require.True(t, rep.OK, cmd)
		require.Equal(t, cmd, rep.Accepted, "the JSON ack names the accepted command")
		// The handler runs after the reply is written: wait for its
		// signal instead of asserting it already ran.
		awaitSignal(t, ran, cmd)
	}
}

func TestNilHandlerMemberIsNotReady(t *testing.T) {
	s, path := listen(t, "v")
	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Toggle: func() { ran <- CmdToggle }})

	rep, err := Send(path, CmdShow, time.Second)
	require.NoError(t, err)
	require.True(t, rep.NotReady(), "nil Show member answers not ready")

	rep, err = Send(path, CmdToggle, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	awaitSignal(t, ran, CmdToggle)
}

func TestReplyArrivesBeforeHandlerFinishes(t *testing.T) {
	// Send speaks JSON, so this proves ack-before-execute in JSON mode
	// (the legacy mode shares the exact same handle/plan ordering).
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
	rep, err := Send(path, CmdToggle, 5*time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, CmdToggle, rep.Accepted)
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
		rep, err := Send(path, cmd, time.Second)
		require.NoError(t, err, "cmd %q", cmd)
		require.False(t, rep.OK, "cmd %q", cmd)
		require.Equal(t, errUnknownCommand, rep.Err, "cmd %q", cmd)
		require.False(t, rep.NotReady(), "cmd %q", cmd)
	}
}

// TestLegacyWireIsByteIdentical pins the v1 compatibility contract: a
// bare-word request takes the legacy path and every reply is the exact
// pre-JSON byte sequence (string literals on purpose -- constant drift
// must fail here), so an OLD client keeps working against a NEW
// daemon untouched.
func TestLegacyWireIsByteIdentical(t *testing.T) {
	s, path := listen(t, "9.8.7-legacy")

	require.Equal(t, "ok", rawExchange(t, path, "ping"))
	require.Equal(t, "9.8.7-legacy", rawExchange(t, path, "version"))
	require.Equal(t, "err not ready", rawExchange(t, path, "toggle"))
	require.Equal(t, "err unknown command", rawExchange(t, path, "bogus"))

	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Toggle: func() { ran <- CmdToggle }})
	require.Equal(t, "ok", rawExchange(t, path, "toggle"))
	awaitSignal(t, ran, CmdToggle)
}

// TestJSONWireShapes pins the v2 responses at the wire level: one JSON
// object per line, in exactly the documented shapes.
func TestJSONWireShapes(t *testing.T) {
	s, path := listen(t, "1.2.3-test")

	require.JSONEq(t, `{"ok":true}`, rawExchange(t, path, `{"cmd":"ping"}`))
	require.JSONEq(t, `{"ok":true,"version":"1.2.3-test"}`, rawExchange(t, path, `{"cmd":"version"}`))
	require.JSONEq(t, `{"ok":false,"error":"not ready"}`, rawExchange(t, path, `{"cmd":"toggle"}`))
	require.JSONEq(t, `{"ok":false,"error":"unknown command"}`, rawExchange(t, path, `{"cmd":"bogus"}`))
	require.JSONEq(t, `{"ok":false,"error":"invalid request"}`, rawExchange(t, path, `{not json`),
		"a '{' line that is not valid JSON is answered in JSON, not legacy")

	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Show: func() { ran <- CmdShow }})
	require.JSONEq(t, `{"ok":true,"accepted":"show"}`, rawExchange(t, path, `{"cmd":"show"}`))
	awaitSignal(t, ran, CmdShow)
}

// TestJSONRequestUnknownFieldsIgnored pins the tolerance contract:
// unknown request fields must be ignored, so future clients can add
// fields without breaking this daemon.
func TestJSONRequestUnknownFieldsIgnored(t *testing.T) {
	s, path := listen(t, "v")
	require.JSONEq(t, `{"ok":true}`, rawExchange(t, path, `{"cmd":"ping","future":"stuff","n":7}`))

	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Toggle: func() { ran <- CmdToggle }})
	require.JSONEq(t, `{"ok":true,"accepted":"toggle"}`, rawExchange(t, path, `{"gen":3,"cmd":"toggle"}`))
	awaitSignal(t, ran, CmdToggle)
}

// legacyOnlyServer fakes an OLD, pre-JSON daemon verbatim: bare
// command words in, raw "ok"/"err not ready"/"err unknown command"/
// bare-version lines out -- a JSON request line matches no command and
// is answered "err unknown command", exactly what the old server code
// did. toggles receives each accepted toggle/show/hide command word.
func legacyOnlyServer(t *testing.T, version string, ready bool) (path string, cmds <-chan string) {
	t.Helper()
	path = testSocket(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil && line == "" {
					return
				}
				switch cmd := strings.TrimSpace(line); cmd {
				case "ping":
					_, _ = conn.Write([]byte("ok\n"))
				case "version":
					_, _ = conn.Write([]byte(version + "\n"))
				case "toggle", "show", "hide":
					if !ready {
						_, _ = conn.Write([]byte("err not ready\n"))
						return
					}
					ch <- cmd
					_, _ = conn.Write([]byte("ok\n"))
				default:
					_, _ = conn.Write([]byte("err unknown command\n"))
				}
			}(conn)
		}
	}()
	return path, ch
}

// TestSendFallsBackToLegacyAgainstOldDaemon proves the new-client /
// old-daemon skew cell: the JSON request earns "err unknown command",
// Send redials and retries with the legacy line, and the command
// lands.
func TestSendFallsBackToLegacyAgainstOldDaemon(t *testing.T) {
	path, cmds := legacyOnlyServer(t, "0.9-old", true)

	rep, err := Send(path, CmdToggle, 5*time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK, "the legacy retry lands the toggle")
	require.True(t, rep.Legacy, "the reply came over the legacy protocol")
	awaitSignal(t, cmds, CmdToggle)

	rep, err = Send(path, CmdVersion, 5*time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, "0.9-old", rep.Version, "the legacy bare-version reply maps into Version")
	require.True(t, rep.Legacy)

	rep, err = Send(path, CmdPing, 5*time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.True(t, rep.Legacy)
}

func TestSendMapsLegacyNotReadyOnRetry(t *testing.T) {
	path, _ := legacyOnlyServer(t, "0.9-old", false)

	rep, err := Send(path, CmdShow, 5*time.Second)
	require.NoError(t, err)
	require.True(t, rep.NotReady(), "the old daemon's 'err not ready' maps to NotReady")
	require.True(t, rep.Legacy)
}

// TestParseReplyMappings unit-tests the client-side reply parser: the
// stray legacy lines a middle-state server might emit, response-side
// unknown-field tolerance, and the in-band garbage path.
func TestParseReplyMappings(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		line string
		want Reply
	}{
		{
			name: "legacy ok",
			cmd:  CmdToggle, line: "ok",
			want: Reply{OK: true, Raw: "ok", Legacy: true},
		},
		{
			name: "legacy not ready",
			cmd:  CmdToggle, line: "err not ready",
			want: Reply{Err: "not ready", Raw: "err not ready", Legacy: true},
		},
		{
			name: "legacy bare version",
			cmd:  CmdVersion, line: "1.2.3",
			want: Reply{OK: true, Version: "1.2.3", Raw: "1.2.3", Legacy: true},
		},
		{
			name: "garbage line stays in-band",
			cmd:  CmdToggle, line: "wat",
			want: Reply{Raw: "wat", Legacy: true},
		},
		{
			name: "empty line stays in-band",
			cmd:  CmdVersion, line: "",
			want: Reply{Raw: "", Legacy: true},
		},
		{
			name: "json with unknown fields is tolerated",
			cmd:  CmdToggle, line: `{"ok":true,"accepted":"toggle","future":1}`,
			want: Reply{OK: true, Accepted: "toggle", Raw: `{"ok":true,"accepted":"toggle","future":1}`},
		},
		{
			name: "json error shape",
			cmd:  CmdToggle, line: `{"ok":false,"error":"not ready"}`,
			want: Reply{Err: "not ready", Raw: `{"ok":false,"error":"not ready"}`},
		},
		{
			name: "broken json-looking line stays in-band",
			cmd:  CmdToggle, line: "{broken",
			want: Reply{Raw: "{broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseReply(tt.cmd, tt.line))
		})
	}
	require.True(t, parseReply(CmdToggle, "err not ready").NotReady())
	require.False(t, parseReply(CmdToggle, "wat").NotReady(), "garbage is not the not-ready answer")
}

func TestRawConnectionCRLFAndEOFTermination(t *testing.T) {
	_, path := listen(t, "v")

	// CRLF-terminated legacy request still parses (TrimSpace).
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

	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err, "server keeps serving after an empty connection")
	require.True(t, rep.OK)
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

	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
}

func TestRegularFileAtSocketPathIsRecovered(t *testing.T) {
	path := testSocket(t)
	require.NoError(t, os.WriteFile(path, []byte("junk"), 0o600))

	s, err := Listen(path, "v")
	require.NoError(t, err, "a non-socket file at the path is treated as stale")
	defer func() { _ = s.Close() }()

	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
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
	reps := make([]Reply, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reps[i], errs[i] = Send(path, CmdToggle, 5*time.Second)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "send %d", i)
		require.True(t, reps[i].OK, "send %d", i)
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
	rep, err := Send(path, CmdPing, 200*time.Millisecond)
	require.Zero(t, rep)
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
