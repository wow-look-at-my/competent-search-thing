package cli

// The CLI half of the self-heal matrix: every path that used to end in
// "already running but did not respond" (exit 1) against a dead,
// wedged, pre-JSON or version-skewed holder now takes the socket over
// and becomes the instance. The fakes run IN-PROCESS, so the takeover
// ladder's peercred pid is the TEST's own -- which is why every env
// here scripts ipc.ListenOptions.Kill with a recorder whose SIGTERM
// hook closes the fake instead of signaling anything real.

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// testBuild is the pinned build stamp for skew scenarios (the real
// derivation depends on whether the test binary carries a vcs stamp,
// which must never decide a test's outcome).
const testBuild = "thisbuild1234"

// cliKill records SIGTERMs (and answers every signal-0 liveness probe
// "alive"); onTerm runs on the first SIGTERM.
type cliKill struct {
	mu     sync.Mutex
	terms  []int
	onTerm func()
}

func (k *cliKill) kill(pid int, sig syscall.Signal) error {
	if sig != syscall.SIGTERM {
		return nil
	}
	k.mu.Lock()
	k.terms = append(k.terms, pid)
	first := len(k.terms) == 1
	hook := k.onTerm
	k.mu.Unlock()
	if first && hook != nil {
		hook()
	}
	return nil
}

func (k *cliKill) sigterms() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.terms)
}

// takeoverEnv builds a CLI env whose socket acquisition runs the real
// takeover engine at test speed with the recorded kill seam.
func takeoverEnv(gui *guiRecorder, kr *cliKill) *env {
	e := &env{version: testVersion, build: testBuild, runGUI: gui.run}
	e.listenFn = func(path string) (*ipc.Server, error) {
		return ipc.ListenWith(path, testVersion, ipc.ListenOptions{
			Build:        testBuild,
			Kill:         kr.kill,
			ProbeTimeout: 100 * time.Millisecond,
			ProbeGap:     5 * time.Millisecond,
			ReleaseWait:  400 * time.Millisecond,
		})
	}
	return e
}

// runEnv executes the CLI over a caller-built env (the takeover tests
// need the scripted listen seam and the pinned build).
func runEnv(t *testing.T, e *env, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	e.out = &out
	code = executeEnv(e, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// cliFake is an in-process fake daemon with an idempotent close.
type cliFake struct {
	ln   net.Listener
	mu   sync.Mutex
	got  []string
	once sync.Once
}

func (f *cliFake) close() { f.once.Do(func() { _ = f.ln.Close() }) }

func (f *cliFake) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// acceptCloseFake accepts and instantly closes every connection unread
// -- the kernel resets the client, the field incident's signature.
func acceptCloseFake(t *testing.T, path string) *cliFake {
	t.Helper()
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	f := &cliFake{ln: ln}
	t.Cleanup(f.close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return f
}

// scriptedFake answers each request line via respond ("" = close
// without replying) and records what it saw.
func scriptedFake(t *testing.T, path string, respond func(line string) string) *cliFake {
	t.Helper()
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	f := &cliFake{ln: ln}
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

// sawCmd reports whether the fake received a request for cmd.
func sawCmd(f *cliFake, cmd string) bool {
	for _, l := range f.received() {
		if strings.Contains(l, `"`+cmd+`"`) {
			return true
		}
	}
	return false
}

// --- matrix row 2: the bare-GUI inversion ------------------------------------
// The deliberate inversion of the old TestRootReportsUnresponsiveInstance:
// an accept-close (reset-every-read) holder used to earn "already
// running but did not respond" and exit 1; now the probe classifies it
// unresponsive, the ladder replaces it, and THIS process becomes the
// instance.

func TestRootTakesOverUnresponsiveInstance(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := acceptCloseFake(t, path)
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, stderr := runEnv(t, takeoverEnv(gui, kr))
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Equal(t, 1, gui.count(), "the bare launch becomes the instance")
	opts := gui.last(t)
	require.NotNil(t, opts.Server, "with a working socket of its own")
	require.False(t, opts.ShowOnStartup, "a bare launch still starts hidden, like any cold start")
	require.Equal(t, 1, kr.sigterms())
	require.NotContains(t, stderr, "did not respond", "the dead-end report is gone")
}

// --- root against a pre-JSON daemon: legacy -> new instance wins -------------
// Inverts the old TestRootReportsUnexpectedReply ("wat" reply, exit 1).

func TestRootReplacesPreJSONDaemon(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := scriptedFake(t, path, func(string) string { return "wat" })
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := runEnv(t, takeoverEnv(gui, kr))
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	require.NotNil(t, gui.last(t).Server)
	require.Equal(t, 1, kr.sigterms(), "the legacy daemon was terminated by exact pid")
}

// --- toggle against a reset-everything holder --------------------------------

func TestToggleTakesOverResetInstance(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := acceptCloseFake(t, path)
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := runEnv(t, takeoverEnv(gui, kr), "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "toggle becomes the instance instead of exiting 1")
	opts := gui.last(t)
	require.NotNil(t, opts.Server)
	require.True(t, opts.ShowOnStartup, "the summon intent survives the takeover")
}

// --- toggle against a pre-JSON daemon ----------------------------------------
// Inverts the old TestUnexpectedReplyIsAnError (garbage reply, exit 1).

func TestToggleReplacesPreJSONDaemon(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := scriptedFake(t, path, func(string) string { return "wat" })
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := runEnv(t, takeoverEnv(gui, kr), "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	require.True(t, gui.last(t).ShowOnStartup)
	require.Equal(t, 1, kr.sigterms())
}

// --- toggle against a version-skewed daemon: new instance wins ---------------

func TestToggleReplacesSkewedDaemon(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := scriptedFake(t, path, func(line string) string {
		if strings.Contains(line, `"version"`) {
			return `{"ok":true,"version":"` + testVersion + `","build":"oldbuild00001"}`
		}
		return `{"ok":false,"error":"unknown command"}` // an old daemon: no quit
	})
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := runEnv(t, takeoverEnv(gui, kr), "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "brew-upgrade-then-summon converges to the new binary")
	require.True(t, gui.last(t).ShowOnStartup)
	require.True(t, sawCmd(fake, ipc.CmdQuit), "the graceful quit handshake ran before any signal")
	require.Equal(t, 1, kr.sigterms(), "and the quit-less old daemon was then terminated")
}

// --- config against an older daemon: automatic convergence -------------------
// Replaces the old TestConfigAgainstOlderDaemon dead end ("older version
// without the config command; restart it", exit 1).

func TestConfigReplacesOlderDaemon(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := scriptedFake(t, path, func(line string) string {
		if strings.Contains(line, `"version"`) {
			return `{"ok":true,"version":"` + testVersion + `"}` // no build: an older vintage
		}
		return `{"ok":false,"error":"unknown command"}`
	})
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, stdout, stderr := runEnv(t, takeoverEnv(gui, kr), "config")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.NotContains(t, stderr, "restart it", "the restart-it dead end is retired")
	require.NotContains(t, stdout, "restart it")
	require.Equal(t, 1, gui.count())
	opts := gui.last(t)
	require.True(t, opts.OpenConfig, "the editor intent survives the takeover")
	require.True(t, opts.ShowOnStartup)
}

// --- config against a pre-JSON daemon ----------------------------------------
// Inverts the old TestConfigUnexpectedReplyIsAnError.

func TestConfigReplacesPreJSONDaemon(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	fake := scriptedFake(t, path, func(string) string { return "wat" })
	kr.onTerm = fake.close
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := runEnv(t, takeoverEnv(gui, kr), "config")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	require.True(t, gui.last(t).OpenConfig)
	require.Equal(t, 1, kr.sigterms())
}

// --- the defensive same-build unknown-command branch -------------------------
// A daemon of THIS exact build that still denies the config command is
// pathological (a same-build daemon knows config); the convergence
// attempt finds it healthy, concedes, and reports honestly -- it never
// kills a responsive same-build daemon over it.

func TestConfigSameBuildUnknownCommandStaysHonest(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	_ = scriptedFake(t, path, func(line string) string {
		if strings.Contains(line, `"version"`) {
			return `{"ok":true,"version":"` + testVersion + `","build":"` + testBuild + `"}`
		}
		return `{"ok":false,"error":"unknown command"}`
	})
	gui := &guiRecorder{}

	code, _, stderr := runEnv(t, takeoverEnv(gui, kr), "config")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "unexpected reply")
	require.Equal(t, 0, gui.count(), "no GUI is started over a live same-build daemon")
	require.Zero(t, kr.sigterms(), "and it is never signaled")
}

// --- matrix row 10: hide never takes over ------------------------------------

func TestHideNeverTakesOver(t *testing.T) {
	path := testSocketEnv(t)
	kr := &cliKill{}
	_ = acceptCloseFake(t, path)
	gui := &guiRecorder{}

	code, stdout, _ := runEnv(t, takeoverEnv(gui, kr), "hide")
	require.Equal(t, 1, code, "hide keeps its honest-failure contract")
	require.Empty(t, stdout)
	require.Equal(t, 0, gui.count(), "hide never starts the app")
	require.Zero(t, kr.sigterms(), "and never kills anything")
}

// --- startup races -----------------------------------------------------------

func TestSummonRaceLoserDeliversToWinner(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}
	var c *ipcSignals
	e := &env{version: testVersion, build: testBuild, runGUI: gui.run}
	e.listenFn = func(p string) (*ipc.Server, error) {
		// The race: by the time this launcher tries to become the
		// instance, a healthy winner holds the socket.
		c = liveServer(t, p)
		return nil, ipc.ErrAlreadyRunning
	}

	code, _, _ := runEnv(t, e, "toggle")
	require.Equal(t, 0, code)
	awaitHandler(t, c.shows, "show")
	require.Equal(t, 0, gui.count(), "the loser delivers to the winner instead of starting")
}

func TestRootRetryFindsHealthyWinner(t *testing.T) {
	path := testSocketEnv(t)
	fake := acceptCloseFake(t, path)
	gui := &guiRecorder{}
	var (
		calls int
		c     *ipcSignals
	)
	e := &env{version: testVersion, build: testBuild, runGUI: gui.run}
	e.listenFn = func(p string) (*ipc.Server, error) {
		calls++
		if calls == 1 {
			// The first acquisition saw a "healthy" holder...
			return nil, ipc.ErrAlreadyRunning
		}
		// ...which died mid-show; by the re-listen a fresh healthy
		// instance won the takeover race.
		fake.close()
		c = liveServer(t, p)
		return nil, ipc.ErrAlreadyRunning
	}

	code, stdout, _ := runEnv(t, e)
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "already running; showing it",
		"the final delivery to the healthy winner reports honestly")
	awaitHandler(t, c.shows, "show")
	require.Equal(t, 0, gui.count())
	require.Equal(t, 2, calls, "exactly one bounded re-listen, no loop")
}

func TestRootRetryListenFailureRunsDegraded(t *testing.T) {
	path := testSocketEnv(t)
	fake := acceptCloseFake(t, path)
	gui := &guiRecorder{}
	var calls int
	e := &env{version: testVersion, build: testBuild, runGUI: gui.run}
	e.listenFn = func(p string) (*ipc.Server, error) {
		calls++
		if calls == 1 {
			return nil, ipc.ErrAlreadyRunning
		}
		fake.close()
		return nil, errors.New("bind exploded")
	}

	code, _, _ := runEnv(t, e)
	require.Equal(t, 0, code, "the GUI still runs, degraded (the runRoot contract)")
	require.Equal(t, 1, gui.count())
	require.Nil(t, gui.last(t).Server)
}
