package cli

// The config subcommand's tests live in their own file (the shared
// guiRecorder/testSocketEnv/run helpers are in cli_test.go), with a
// local live-server helper so the shared one stays untouched.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// testConfigSocketEnv isolates BOTH sockets: the settings window has
// its own (ipc.EnvConfigSocket), and pinning the searchbar's too keeps
// a stray real instance from ever entering these tests.
func testConfigSocketEnv(t *testing.T) string {
	t.Helper()
	testSocketEnv(t)
	dir, err := os.MkdirTemp("", "cli-config")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "config.sock")
	t.Setenv(ipc.EnvConfigSocket, path)
	return path
}

// liveConfigWindow starts a real IPC server on the settings-window
// socket whose Show handler signals invocations -- a settings window
// that is already open.
func liveConfigWindow(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	shows := make(chan struct{}, 8)
	srv.SetHandlers(ipc.Handlers{Show: func() { shows <- struct{}{} }})
	return shows
}

func TestConfigOpensTheSettingsWindow(t *testing.T) {
	testConfigSocketEnv(t)
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, stderr := run(t, gui, "config")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Equal(t, 1, gui.count())
	opts := gui.last(t)
	require.True(t, opts.ConfigWindow, "this process becomes the settings window")
	require.NotNil(t, opts.Server, "and owns the settings-window socket")
	require.False(t, opts.ShowOnStartup, "the searchbar is not involved at all")
}

func TestConfigRaisesAnOpenSettingsWindow(t *testing.T) {
	path := testConfigSocketEnv(t)
	shows := liveConfigWindow(t, path)
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "already open; showing it")
	select {
	case <-shows:
	case <-time.After(5 * time.Second):
		t.Fatal("the open settings window was never raised")
	}
	require.Equal(t, 0, gui.count(), "no second settings window opens")
}

// A settings window still booting (no handlers wired) answers "not
// ready" -- responsive, so it is raised rather than replaced.
func TestConfigAgainstBootingSettingsWindow(t *testing.T) {
	path := testConfigSocketEnv(t)
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	gui := &guiRecorder{}

	code, stdout, stderr := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "already open")
	require.Empty(t, stderr)
	require.Equal(t, 0, gui.count())
}

func TestConfigRunsDegradedWhenListenFails(t *testing.T) {
	// An unusable socket path: the settings window must still open,
	// just without single-instance IPC.
	testSocketEnv(t)
	t.Setenv(ipc.EnvConfigSocket, filepath.Join(t.TempDir(), "no-such-dir", "s.sock"))
	gui := &guiRecorder{}

	code, _, _ := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "the window still opens, degraded")
	opts := gui.last(t)
	require.Nil(t, opts.Server)
	require.True(t, opts.ConfigWindow)
}

func TestConfigHelpDescribesTheWindow(t *testing.T) {
	testConfigSocketEnv(t)
	gui := &guiRecorder{}
	code, stdout, _ := run(t, gui, "config", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "settings window")
	require.Equal(t, 0, gui.count())
}
