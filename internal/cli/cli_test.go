package cli

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

const testVersion = "9.9.9-test"

// guiRecorder is a fake GUI entry point recording every invocation.
type guiRecorder struct {
	mu    sync.Mutex
	calls []RunOptions
	err   error
}

func (g *guiRecorder) run(opts RunOptions) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, opts)
	return g.err
}

func (g *guiRecorder) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *guiRecorder) last(t *testing.T) RunOptions {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(t, g.calls, "runGUI was never called")
	return g.calls[len(g.calls)-1]
}

// closeServers closes any IPC servers the fake GUI received, so tests
// that make the CLI acquire the socket do not leak listeners.
func (g *guiRecorder) closeServers() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		_ = c.Server.Close()
	}
}

// testSocketEnv points the CLI at a private socket path and returns
// it.
func testSocketEnv(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	t.Setenv(ipc.EnvSocket, path)
	return path
}

// run executes the CLI against args with a fake GUI, returning the
// exit code and the captured stdout/stderr.
func run(t *testing.T, gui *guiRecorder, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = execute(testVersion, gui.run, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// ipcCounters tracks handler invocations on a live test server.
type ipcCounters struct {
	toggles, shows, hides atomic.Int32
}

// liveServer starts a real IPC server with counting handlers on the
// test socket.
func liveServer(t *testing.T, path string) *ipcCounters {
	t.Helper()
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	c := &ipcCounters{}
	srv.SetHandlers(ipc.Handlers{
		Toggle: func() { c.toggles.Add(1) },
		Show:   func() { c.shows.Add(1) },
		Hide:   func() { c.hides.Add(1) },
	})
	return c
}

func TestRootStartsGUIWithServer(t *testing.T) {
	path := testSocketEnv(t)
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := run(t, gui)
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	opts := gui.last(t)
	require.NotNil(t, opts.Server, "the root path hands the GUI a live server")
	require.False(t, opts.ShowOnStartup, "a bare launch starts hidden")

	// The server really is listening on the configured socket and
	// carries the version.
	resp, err := ipc.Send(path, ipc.CmdVersion, time.Second)
	require.NoError(t, err)
	require.Equal(t, testVersion, resp)
}

func TestRootSecondInstanceShowsTheFirst(t *testing.T) {
	path := testSocketEnv(t)
	c := liveServer(t, path)
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui)
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "competent-search-thing is already running; showing it")
	require.Equal(t, int32(1), c.shows.Load(), "the running instance was asked to show")
	require.Equal(t, 0, gui.count(), "no second GUI starts")
}

func TestRootRunsGUIWithoutIPCWhenListenFails(t *testing.T) {
	// A socket path in a directory that does not exist: Listen fails
	// with something other than ErrAlreadyRunning.
	t.Setenv(ipc.EnvSocket, filepath.Join(t.TempDir(), "no-such-dir", "s.sock"))
	gui := &guiRecorder{}

	code, _, _ := run(t, gui)
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "the GUI still runs, degraded")
	require.Nil(t, gui.last(t).Server)
}

func TestRootPropagatesGUIError(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{err: errors.New("webview exploded")}
	defer gui.closeServers()

	code, _, stderr := run(t, gui)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "webview exploded")
}

func TestToggleTalksToRunningInstance(t *testing.T) {
	path := testSocketEnv(t)
	c := liveServer(t, path)
	gui := &guiRecorder{}

	code, _, _ := run(t, gui, "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, int32(1), c.toggles.Load())
	require.Equal(t, 0, gui.count())
}

func TestShowTalksToRunningInstance(t *testing.T) {
	path := testSocketEnv(t)
	c := liveServer(t, path)
	gui := &guiRecorder{}

	code, _, _ := run(t, gui, "show")
	require.Equal(t, 0, code)
	require.Equal(t, int32(1), c.shows.Load())
	require.Equal(t, 0, gui.count())
}

func TestHideTalksToRunningInstance(t *testing.T) {
	path := testSocketEnv(t)
	c := liveServer(t, path)
	gui := &guiRecorder{}

	code, _, stderr := run(t, gui, "hide")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Equal(t, int32(1), c.hides.Load())
	require.Equal(t, 0, gui.count())
}

func TestToggleStartsGUIWhenNotRunning(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := run(t, gui, "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	opts := gui.last(t)
	require.NotNil(t, opts.Server, "the fallback GUI owns the socket")
	require.True(t, opts.ShowOnStartup, "the bar shows once the frontend is ready")
}

func TestShowStartsGUIWhenNotRunning(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := run(t, gui, "show")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	require.True(t, gui.last(t).ShowOnStartup)
}

func TestToggleRunsGUIDegradedWhenListenFails(t *testing.T) {
	// The dial fails (not running) and the fallback Listen fails too
	// (unusable path): the GUI must still start, without IPC.
	t.Setenv(ipc.EnvSocket, filepath.Join(t.TempDir(), "no-such-dir", "s.sock"))
	gui := &guiRecorder{}

	code, _, _ := run(t, gui, "toggle")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "the GUI still runs, degraded")
	opts := gui.last(t)
	require.Nil(t, opts.Server)
	require.True(t, opts.ShowOnStartup)
}

func TestHideErrorsWhenNotRunning(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}

	code, stdout, stderr := run(t, gui, "hide")
	require.Equal(t, 1, code)
	require.Empty(t, stdout)
	require.Equal(t, "competent-search-thing is not running\n", stderr,
		"exactly the notice, no cobra Error line, no usage dump")
	require.Equal(t, 0, gui.count(), "hide never starts the app")
}

func TestSummonTreatsNotReadyAsSuccess(t *testing.T) {
	path := testSocketEnv(t)
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	// No SetHandlers: the instance is "booting".
	gui := &guiRecorder{}

	for _, sub := range []string{"toggle", "show", "hide"} {
		code, _, stderr := run(t, gui, sub)
		require.Equal(t, 0, code, "%s against a booting instance succeeds (stderr: %s)", sub, stderr)
	}
	require.Equal(t, 0, gui.count())
}

func TestUnexpectedReplyIsAnError(t *testing.T) {
	path := testSocketEnv(t)
	// A fake server that answers garbage to whatever arrives.
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			_, _ = conn.Read(buf)
			_, _ = conn.Write([]byte("wat\n"))
			_ = conn.Close()
		}
	}()
	gui := &guiRecorder{}

	code, _, stderr := run(t, gui, "toggle")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "unexpected reply")
	require.Equal(t, 0, gui.count())
}

func TestVersionFlag(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "--version")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, testVersion)
	require.Equal(t, 0, gui.count())
}

func TestUnknownSubcommandFails(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}

	code, _, stderr := run(t, gui, "frobnicate")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "unknown command")
	require.Equal(t, 0, gui.count())
}

func TestSummonHelpMentionsStarting(t *testing.T) {
	testSocketEnv(t)
	for _, sub := range []string{"toggle", "show"} {
		gui := &guiRecorder{}
		code, stdout, _ := run(t, gui, sub, "--help")
		require.Equal(t, 0, code)
		require.Contains(t, stdout, "starts the app if it is not running",
			"%s help documents the start-if-needed behavior", sub)
		require.Equal(t, 0, gui.count())
	}
}
