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

	rep, err = Send(path, CmdVersion, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, "1.2.3-test", rep.Version)
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

// TestNonJSONRequestsAreRejected pins the deletion of the legacy v1
// line protocol: every request line must parse as JSON, so bare
// command words -- what an old, pre-JSON CLI sends -- and arbitrary
// garbage bytes all earn the terse JSON invalid-request error, never
// a raw legacy reply line, and never a handler invocation.
func TestNonJSONRequestsAreRejected(t *testing.T) {
	s, path := listen(t, "9.8.7-test")
	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Toggle: func() { ran <- CmdToggle }})

	for _, line := range []string{
		"ping",                // the old line protocol's health check
		"version",             // its version request
		"toggle",              // its summon -- wired handler or not, it must not run
		"err unknown command", // a legacy REPLY line bounced back
		"\x01\x02garbage",     // arbitrary junk bytes
		"",                    // a blank line
		`"toggle"`,            // valid JSON, but not an object
		"123",                 // valid JSON, but not an object
	} {
		require.JSONEq(t, `{"ok":false,"error":"invalid request"}`, rawExchange(t, path, line),
			"request %q", line)
	}
	// JSON null unmarshals into the request as a no-op: it IS valid
	// JSON, so it takes the normal path and fails as an unknown (empty)
	// command rather than an invalid request.
	require.JSONEq(t, `{"ok":false,"error":"unknown command"}`, rawExchange(t, path, "null"))

	select {
	case cmd := <-ran:
		t.Fatalf("a rejected request ran the %s handler", cmd)
	default:
	}
}

// TestJSONWireShapes pins the responses at the wire level: one JSON
// object per line, in exactly the documented shapes. The build stamp
// is pinned explicitly -- the plain Listen wrapper derives it from the
// test binary's own (environment-dependent) vcs stamp, which must
// never decide a wire assertion.
func TestJSONWireShapes(t *testing.T) {
	path := testSocket(t)
	s, err := ListenWith(path, "1.2.3-test", ListenOptions{Build: "wire-build-42"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.JSONEq(t, `{"ok":true}`, rawExchange(t, path, `{"cmd":"ping"}`))
	require.JSONEq(t, `{"ok":true,"version":"1.2.3-test","build":"wire-build-42"}`, rawExchange(t, path, `{"cmd":"version"}`))
	require.JSONEq(t, `{"ok":false,"error":"not ready"}`, rawExchange(t, path, `{"cmd":"toggle"}`))
	require.JSONEq(t, `{"ok":false,"error":"unknown command"}`, rawExchange(t, path, `{"cmd":"bogus"}`))
	require.JSONEq(t, `{"ok":false,"error":"invalid request"}`, rawExchange(t, path, `{not json`),
		"a '{' line that is not valid JSON is an invalid request")

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

// TestSendReturnsOldDaemonReplyInBand pins the deliberate version-skew
// behavior after the legacy protocol's deletion: a still-running
// pre-JSON daemon answers the JSON request with its raw
// "err unknown command" line, which Send now returns IN-BAND (Raw set,
// OK false, empty Err) instead of retrying with a legacy line -- the
// caller reports it as an unexpected reply and the user restarts the
// old instance once.
func TestSendReturnsOldDaemonReplyInBand(t *testing.T) {
	path := testSocket(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	dials := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			dials <- struct{}{}
			// An old daemon matches no bare command word against the
			// JSON line and answers its raw legacy reply.
			_, _ = bufio.NewReader(conn).ReadString('\n')
			_, _ = conn.Write([]byte("err unknown command\n"))
			_ = conn.Close()
		}
	}()

	rep, err := Send(path, CmdToggle, 5*time.Second)
	require.NoError(t, err, "a non-JSON reply is in-band, not a transport error")
	require.False(t, rep.OK)
	require.Empty(t, rep.Err, "no wire error text: the line did not parse")
	require.Equal(t, "err unknown command", rep.Raw, "the raw line is the caller's evidence")
	require.False(t, rep.NotReady())

	// Exactly one connection: the legacy retry is gone.
	<-dials
	select {
	case <-dials:
		t.Fatal("Send dialed a second time -- the deleted legacy retry is back")
	default:
	}
}

// TestParseReplyMappings unit-tests the client-side reply parser:
// response-side unknown-field tolerance and the in-band garbage path
// (which now covers every pre-JSON reply line).
func TestParseReplyMappings(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Reply
	}{
		{
			name: "accepted command",
			line: `{"ok":true,"accepted":"toggle"}`,
			want: Reply{OK: true, Accepted: "toggle", Raw: `{"ok":true,"accepted":"toggle"}`, Parsed: true},
		},
		{
			name: "version answer",
			line: `{"ok":true,"version":"1.2.3"}`,
			want: Reply{OK: true, Version: "1.2.3", Raw: `{"ok":true,"version":"1.2.3"}`, Parsed: true},
		},
		{
			name: "version answer with build stamp",
			line: `{"ok":true,"version":"1.2.3","build":"abcdef123456"}`,
			want: Reply{OK: true, Version: "1.2.3", Build: "abcdef123456",
				Raw: `{"ok":true,"version":"1.2.3","build":"abcdef123456"}`, Parsed: true},
		},
		{
			name: "json with unknown fields is tolerated",
			line: `{"ok":true,"accepted":"toggle","future":1}`,
			want: Reply{OK: true, Accepted: "toggle", Raw: `{"ok":true,"accepted":"toggle","future":1}`, Parsed: true},
		},
		{
			name: "json error shape",
			line: `{"ok":false,"error":"not ready"}`,
			want: Reply{Err: "not ready", Raw: `{"ok":false,"error":"not ready"}`, Parsed: true},
		},
		{
			name: "legacy ok line is garbage now",
			line: "ok",
			want: Reply{Raw: "ok"},
		},
		{
			name: "legacy err line is garbage now",
			line: "err not ready",
			want: Reply{Raw: "err not ready"},
		},
		{
			name: "garbage line stays in-band",
			line: "wat",
			want: Reply{Raw: "wat"},
		},
		{
			name: "empty line stays in-band",
			line: "",
			want: Reply{Raw: ""},
		},
		{
			name: "broken json-looking line stays in-band",
			line: "{broken",
			want: Reply{Raw: "{broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseReply(tt.line))
		})
	}
	require.True(t, parseReply(`{"ok":false,"error":"not ready"}`).NotReady())
	require.False(t, parseReply("err not ready").NotReady(),
		"the old daemon's raw not-ready line no longer maps to NotReady")
	require.False(t, parseReply("wat").NotReady(), "garbage is not the not-ready answer")
}

func TestRawConnectionCRLFAndEOFTermination(t *testing.T) {
	_, path := listen(t, "v")
	pingReq := `{"cmd":"ping"}`
	pingOK := `{"ok":true}` + "\n"

	// A CRLF-terminated request still parses (TrimSpace).
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	_, err = conn.Write([]byte(pingReq + "\r\n"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	require.Equal(t, pingOK, string(buf[:n]))
	require.NoError(t, conn.Close())

	// A request terminated by closing the write side (no newline)
	// also parses.
	uc, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	_, err = uc.Write([]byte(pingReq))
	require.NoError(t, err)
	require.NoError(t, uc.CloseWrite())
	n, err = uc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, pingOK, string(buf[:n]))
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
