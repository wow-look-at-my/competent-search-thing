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
