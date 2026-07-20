package ipc

// Tests for the self-healing occupied-socket probe + takeover engine
// (takeover.go): the hard-gate matrix from the death-race incident.
// Every fake daemon here runs IN-PROCESS, so the peer credentials the
// engine reads are the TEST's own pid -- which is why every test that
// can reach the terminate ladder scripts ListenOptions.Kill with a
// recorder. Nothing in this file may ever signal a real process (the
// one owned-child integration test lives in integration_test.go).

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// logRec records the takeover engine's log lines for assertions.
type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRec) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logRec) contains(t *testing.T, sub string) {
	t.Helper()
	require.Contains(t, l.joined(), sub)
}

// killRec records signal deliveries instead of making them; onTerm
// runs on the first SIGTERM (tests use it to "kill" their fake daemon
// by closing its listener). Signal-0 liveness probes answer alive.
type killRec struct {
	mu     sync.Mutex
	calls  []killCall
	onTerm func()
}

type killCall struct {
	pid int
	sig syscall.Signal
}

func (k *killRec) kill(pid int, sig syscall.Signal) error {
	k.mu.Lock()
	k.calls = append(k.calls, killCall{pid: pid, sig: sig})
	hook := k.onTerm
	first := sig == syscall.SIGTERM && len(k.sigtermsLocked()) == 1
	k.mu.Unlock()
	if first && hook != nil {
		hook()
	}
	return nil
}

func (k *killRec) sigtermsLocked() []int {
	var pids []int
	for _, c := range k.calls {
		if c.sig == syscall.SIGTERM {
			pids = append(pids, c.pid)
		}
	}
	return pids
}

func (k *killRec) sigterms() []int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sigtermsLocked()
}

// fastOpts is the test-speed ListenOptions base: recorded kills,
// recorded logs, millisecond-scale probe/release budgets.
func fastOpts(lr *logRec, kr *killRec, build string) ListenOptions {
	return ListenOptions{
		Logf:         lr.logf,
		Build:        build,
		Kill:         kr.kill,
		ProbeTimeout: 80 * time.Millisecond,
		ProbeGap:     5 * time.Millisecond,
		ReleaseWait:  300 * time.Millisecond,
	}
}

// wedgeListener binds path and never accepts: the frozen-daemon shape
// (probe connects land in the backlog and time out).
func wedgeListener(t *testing.T, path string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// acceptCloseListener accepts and instantly closes every connection
// unread -- the kernel resets the client (the field incident's exact
// client-side signature).
func acceptCloseListener(t *testing.T, path string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln
}

// scriptedFake runs a fake daemon answering each request line via
// respond ("" = close without replying), recording every line it saw.
type scriptedFake struct {
	ln   net.Listener
	mu   sync.Mutex
	got  []string
	once sync.Once
}

func newScriptedFake(t *testing.T, path string, respond func(line string) string) *scriptedFake {
	t.Helper()
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	f := &scriptedFake{ln: ln}
	t.Cleanup(f.close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil && line == "" {
					return
				}
				line = strings.TrimSpace(line)
				f.mu.Lock()
				f.got = append(f.got, line)
				f.mu.Unlock()
				if r := respond(line); r != "" {
					_, _ = c.Write([]byte(r + "\n"))
				}
			}(conn)
		}
	}()
	return f
}

func (f *scriptedFake) close() { f.once.Do(func() { _ = f.ln.Close() }) }

func (f *scriptedFake) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// cmdOf parses the "cmd" a fake received (the fakes see real Request
// JSON lines).
func cmdOf(line string) string {
	for _, c := range []string{CmdVersion, CmdQuit, CmdPing, CmdShow, CmdToggle, CmdConfig} {
		if strings.Contains(line, `"`+c+`"`) {
			return c
		}
	}
	return line
}

// requireWorkingServer proves the returned server actually serves.
func requireWorkingServer(t *testing.T, s *Server, path string) {
	t.Helper()
	require.NotNil(t, s)
	t.Cleanup(func() { _ = s.Close() })
	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
}

// --- matrix row 1: wedged-alive peer -> timeout -> takeover ------------------

func TestListenTakesOverWedgedPeer(t *testing.T) {
	path := testSocket(t)
	wedge := wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	kr.onTerm = func() { _ = wedge.Close() } // the scripted SIGTERM "kills" the wedge (close unlinks)

	s, err := ListenWith(path, "v", fastOpts(lr, kr, "b1"))
	require.NoError(t, err, "an unresponsive holder is replaced, not surrendered to")
	requireWorkingServer(t, s, path)

	require.Equal(t, []int{os.Getpid()}, kr.sigterms(),
		"exactly one SIGTERM, to the exact peercred pid (the in-process fake reports the test's own)")
	lr.contains(t, "is bound; probing the holder")
	lr.contains(t, "timeout after")
	lr.contains(t, "no healthy instance behind")
	lr.contains(t, "sent SIGTERM to pid")
	lr.contains(t, "released the socket in")
	lr.contains(t, "took over as the single instance")
}

// --- matrix row 2 (engine half): reset-on-read -> takeover -------------------

func TestListenTakesOverResetPeer(t *testing.T) {
	path := testSocket(t)
	fake := acceptCloseListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	kr.onTerm = func() { _ = fake.Close() }

	s, err := ListenWith(path, "v", fastOpts(lr, kr, "b1"))
	require.NoError(t, err, "a mid-death holder (reset every read) is replaced")
	requireWorkingServer(t, s, path)

	require.Len(t, kr.sigterms(), 1)
	lr.contains(t, "no healthy instance behind")
	lr.contains(t, "took over as the single instance")
}

// --- matrix row 3: cold start stays zero-probe -------------------------------

func TestColdStartRunsZeroProbes(t *testing.T) {
	path := testSocket(t)
	lr := &logRec{}
	var dials int
	opts := ListenOptions{Logf: lr.logf}
	opts.dial = func(p string, d time.Duration) (net.Conn, error) {
		dials++
		return net.DialTimeout("unix", p, d)
	}

	s, err := ListenWith(path, "v", opts)
	require.NoError(t, err)
	requireWorkingServer(t, s, path)
	require.Zero(t, dials, "the no-file cold start must not probe anything")
	require.Empty(t, lr.joined(), "and must log nothing")
}

// --- matrix row 4: pre-JSON legacy daemon -> new instance wins ---------------

func TestListenReplacesLegacyDaemon(t *testing.T) {
	path := testSocket(t)
	// A v1-era daemon matches no bare command word against the JSON
	// probe line and answers its raw legacy reply (recovered-source
	// behavior pinned in the diagnosis).
	fake := newScriptedFake(t, path, func(string) string { return "err unknown command" })
	lr, kr := &logRec{}, &killRec{}
	kr.onTerm = fake.close

	s, err := ListenWith(path, "v", fastOpts(lr, kr, "b1"))
	require.NoError(t, err)
	requireWorkingServer(t, s, path)

	require.Len(t, kr.sigterms(), 1, "a legacy daemon is terminated (it has no quit command)")
	for _, line := range fake.received() {
		require.NotEqual(t, CmdQuit, cmdOf(line),
			"no quit request may be wasted on a pre-JSON daemon")
	}
	lr.contains(t, "speaks the pre-JSON protocol")
	lr.contains(t, `(reply "err unknown command"`)
	lr.contains(t, "new instance wins")
}

// --- matrix row 5: old JSON daemon without quit -> SIGTERM fallback ----------

func TestSkewOldJSONDaemonQuitFallsBackToSigterm(t *testing.T) {
	path := testSocket(t)
	fake := newScriptedFake(t, path, func(line string) string {
		switch cmdOf(line) {
		case CmdVersion:
			return `{"ok":true,"version":"v"}` // no build field: an older vintage
		default:
			return `{"ok":false,"error":"unknown command"}` // predates quit
		}
	})
	lr, kr := &logRec{}, &killRec{}
	kr.onTerm = fake.close

	s, err := ListenWith(path, "v", fastOpts(lr, kr, "newbuild12345"))
	require.NoError(t, err)
	requireWorkingServer(t, s, path)

	// The graceful handshake was attempted FIRST, then the ladder.
	var cmds []string
	for _, l := range fake.received() {
		cmds = append(cmds, cmdOf(l))
	}
	require.Contains(t, cmds, CmdQuit, "the quit handshake precedes any signal")
	require.Equal(t, []int{os.Getpid()}, kr.sigterms())
	lr.contains(t, "asking it to quit")
	lr.contains(t, "does not know the quit command")
	lr.contains(t, "took over as the single instance")
}

// --- matrix row 6: graceful quit honored, zero kills -------------------------

func TestSkewGracefulQuitReleasesWithoutKill(t *testing.T) {
	path := testSocket(t)
	quiet := &logRec{}
	old, err := ListenWith(path, "v", ListenOptions{Logf: quiet.logf, Build: "oldbuild11111"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = old.Close() })
	// The production shape: the quit handler triggers an asynchronous
	// app shutdown, whose Close unlinks the socket.
	old.SetHandlers(Handlers{Quit: func() { go old.Close() }})

	lr, kr := &logRec{}, &killRec{}
	s, err := ListenWith(path, "v", fastOpts(lr, kr, "newbuild22222"))
	require.NoError(t, err)
	requireWorkingServer(t, s, path)

	require.Empty(t, kr.sigterms(), "an honored quit needs no signal at all")
	lr.contains(t, "asking it to quit")
	lr.contains(t, "accepted the quit request")
	lr.contains(t, "released the socket in")
	lr.contains(t, "took over as the single instance")
}

// --- matrix row 7: a booting (not-ready) daemon is HEALTHY -------------------

func TestBootingSameBuildDaemonIsAlreadyRunning(t *testing.T) {
	path := testSocket(t)
	quiet := &logRec{}
	booting, err := ListenWith(path, "v", ListenOptions{Logf: quiet.logf, Build: "same-build-12"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = booting.Close() })
	// No SetHandlers: the instance is booting; version answers anyway.

	lr, kr := &logRec{}, &killRec{}
	s, err := ListenWith(path, "v", fastOpts(lr, kr, "same-build-12"))
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning,
		"a responsive same-build daemon is the one healthy outcome")
	require.Empty(t, kr.calls, "and is never probed with signals, let alone killed")
	lr.contains(t, "answered in")
}

// --- matrix row 9: holder dies mid-probe -> plain dead recovery --------------

func TestHolderDyingMidProbeBecomesPlainRecovery(t *testing.T) {
	path := testSocket(t)
	wedge := wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	var once sync.Once
	opts := fastOpts(lr, kr, "b1")
	// The gap sleep between attempts is the death moment: the wedge
	// closes (and unlinks), so the next attempt sees a dead socket.
	opts.sleep = func(time.Duration) {
		once.Do(func() { _ = wedge.Close() })
	}

	s, err := ListenWith(path, "v", opts)
	require.NoError(t, err)
	requireWorkingServer(t, s, path)
	require.Empty(t, kr.calls, "a holder that finished dying needs no ladder at all")
	lr.contains(t, "nothing holds the socket")
}

// --- matrix row 11: peer identity mismatch -> refuse to signal ---------------

func TestTakeoverRefusesForeignProcess(t *testing.T) {
	path := testSocket(t)
	_ = wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	opts := fastOpts(lr, kr, "b1")
	opts.procIdent = func(int) (string, string) { return "/usr/bin/definitely-not-us", "not-us" }

	s, err := ListenWith(path, "v", opts)
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning,
		"a foreign process on our path earns today's honest error, never a signal")
	require.Empty(t, kr.sigterms(), "NEVER signal a pid whose identity does not match")
	lr.contains(t, "refusing to signal it")
}

func TestTakeoverRefusesForeignUID(t *testing.T) {
	path := testSocket(t)
	_ = wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	opts := fastOpts(lr, kr, "b1")
	opts.getuid = func() int { return os.Getuid() + 1 } // "our" uid differs from the peer's

	s, err := ListenWith(path, "v", opts)
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning)
	require.Empty(t, kr.sigterms())
	lr.contains(t, "not ours); refusing to signal it")
}

// --- matrix row 12: two concurrent takeover deciders, one winner -------------

func TestConcurrentTakeoverHasExactlyOneWinner(t *testing.T) {
	path := testSocket(t)
	// A dead instance's leftover: a socket file with no listener.
	addr, err := net.ResolveUnixAddr("unix", path)
	require.NoError(t, err)
	stale, err := net.ListenUnix("unix", addr)
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())

	lr := &logRec{}
	kr := &killRec{}
	type outcome struct {
		s   *Server
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			s, err := ListenWith(path, "v", fastOpts(lr, kr, "same-build-77"))
			results <- outcome{s: s, err: err}
		}()
	}
	var servers []*Server
	var already int
	for i := 0; i < 2; i++ {
		o := <-results
		switch {
		case o.err == nil:
			servers = append(servers, o.s)
		case errors.Is(o.err, ErrAlreadyRunning):
			already++
		default:
			t.Fatalf("unexpected outcome: %v", o.err)
		}
	}
	require.Len(t, servers, 1, "exactly one decider binds")
	require.Equal(t, 1, already, "the other concedes to the winner's healthy socket")
	t.Cleanup(func() { _ = servers[0].Close() })
	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK, "no ghost daemon: the surviving listener serves the path")
	require.Empty(t, kr.sigterms())
}

// --- matrix row 13: quit wire shapes + build-stamped version -----------------

func TestQuitCommandWireShapes(t *testing.T) {
	path := testSocket(t)
	quiet := &logRec{}
	s, err := ListenWith(path, "1.2.3-test", ListenOptions{Logf: quiet.logf, Build: "abcdef123456"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Unwired quit answers not-ready like every command.
	require.JSONEq(t, `{"ok":false,"error":"not ready"}`, rawExchange(t, path, `{"cmd":"quit"}`))

	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Quit: func() { ran <- CmdQuit }})
	require.JSONEq(t, `{"ok":true,"accepted":"quit"}`, rawExchange(t, path, `{"cmd":"quit"}`))
	awaitSignal(t, ran, CmdQuit)

	// a8 parity: the bare legacy word stays an invalid request.
	require.JSONEq(t, `{"ok":false,"error":"invalid request"}`, rawExchange(t, path, "quit"))

	// The version reply carries the build stamp, and Send parses it.
	require.JSONEq(t, `{"ok":true,"version":"1.2.3-test","build":"abcdef123456"}`,
		rawExchange(t, path, `{"cmd":"version"}`))
	rep, err := Send(path, CmdVersion, time.Second)
	require.NoError(t, err)
	require.Equal(t, "abcdef123456", rep.Build)
	require.True(t, rep.Parsed)
}

// --- verified unlink: Close never removes a successor's socket ---------------

func TestCloseLeavesSuccessorSocketAlone(t *testing.T) {
	path := testSocket(t)
	lr := &logRec{}
	s1, err := ListenWith(path, "v", ListenOptions{Logf: lr.logf, Build: "b"})
	require.NoError(t, err)

	// A successor takes over: unlink + fresh bind (what the takeover
	// ladder does to a force-replaced zombie's path).
	require.NoError(t, os.Remove(path))
	ln2, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln2.Close() })

	// The zombie un-wedges into its graceful Close: the successor's
	// socket must survive.
	require.NoError(t, s1.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "the successor's socket file survives the old instance's Close")
	conn, err := net.Dial("unix", path)
	require.NoError(t, err, "and still accepts connections")
	_ = conn.Close()
	lr.contains(t, "now belongs to a newer instance")
}

// --- unit coverage for the helpers ------------------------------------------

func TestSameBinary(t *testing.T) {
	tests := []struct {
		name               string
		exe, comm, ownBase string
		want               bool
	}{
		{"exact exe", "/usr/bin/competent-search-thing", "competent-sear", "competent-search-thing", true},
		{"deleted suffix tolerated", "/usr/bin/competent-search-thing (deleted)", "", "competent-search-thing", true},
		{"comm prefix match", "", "competent-searc", "competent-search-thing", true},
		{"comm exact short name", "", "shortname", "shortname", true},
		{"foreign exe", "/usr/bin/evil", "evil", "competent-search-thing", false},
		{"foreign comm only", "", "evil", "competent-search-thing", false},
		{"empty metadata fails open", "", "", "competent-search-thing", true},
		{"no own base fails open", "/usr/bin/anything", "x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sameBinary(tt.exe, tt.comm, tt.ownBase))
		})
	}
}

func TestOwnBuildIsStable(t *testing.T) {
	a, b := OwnBuild(), OwnBuild()
	require.Equal(t, a, b)
	require.LessOrEqual(t, len(a), 12, "the stamp is the 12-char vcs revision prefix (or empty)")
}

func TestDescribeFailure(t *testing.T) {
	require.Contains(t, describeFailure(syscall.ECONNRESET, time.Second), "connection reset by peer")
	require.Contains(t, describeFailure(fmt.Errorf("read: %w", syscall.EPIPE), time.Second), "broken pipe")
	require.Contains(t, describeFailure(timeoutErr{}, 500*time.Millisecond), "timeout after 500ms")
	require.Equal(t, "weird", describeFailure(fmt.Errorf("weird"), time.Second))
}

// timeoutErr is a minimal net.Error whose Timeout reports true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "deadline" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }
