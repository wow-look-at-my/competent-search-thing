package cli

// The config subcommand's tests live in their own file (the shared
// guiRecorder/testSocketEnv/run helpers are in cli_test.go), with a
// local live-server helper so the shared one stays untouched.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// liveConfigServer starts a real IPC server whose config handler
// signals invocations.
func liveConfigServer(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	configs := make(chan struct{}, 8)
	srv.SetHandlers(ipc.Handlers{Config: func() { configs <- struct{}{} }})
	return configs
}

func TestConfigTalksToRunningInstance(t *testing.T) {
	path := testSocketEnv(t)
	configs := liveConfigServer(t, path)
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "opening the config editor in the running instance")
	select {
	case <-configs:
	case <-time.After(5 * time.Second):
		t.Fatal("the config handler never ran")
	}
	require.Equal(t, 0, gui.count(), "no second GUI starts")
}

func TestConfigStartsGUIWhenNotRunning(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}
	defer gui.closeServers()

	code, _, _ := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count())
	opts := gui.last(t)
	require.NotNil(t, opts.Server, "the fallback GUI owns the socket")
	require.True(t, opts.ShowOnStartup)
	require.True(t, opts.OpenConfig, "the GUI starts straight into the config editor")
}

func TestConfigRunsGUIDegradedWhenListenFails(t *testing.T) {
	// The dial fails (not running) and the fallback Listen fails too
	// (unusable path): the GUI must still start, editor-first.
	t.Setenv(ipc.EnvSocket, filepath.Join(t.TempDir(), "no-such-dir", "s.sock"))
	gui := &guiRecorder{}

	code, _, _ := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Equal(t, 1, gui.count(), "the GUI still runs, degraded")
	opts := gui.last(t)
	require.Nil(t, opts.Server)
	require.True(t, opts.OpenConfig)
}

func TestConfigAgainstBootingInstance(t *testing.T) {
	path := testSocketEnv(t)
	srv, err := ipc.Listen(path, testVersion)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	// No SetHandlers: the instance is "booting".
	gui := &guiRecorder{}

	code, stdout, stderr := run(t, gui, "config")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "still starting up")
	require.Empty(t, stderr)
	require.Equal(t, 0, gui.count())
}

// The old TestConfigAgainstOlderDaemon ("older version without the
// config command; restart it", exit 1) and
// TestConfigUnexpectedReplyIsAnError pinned dead ends the self-heal
// work DELIBERATELY retired: version skew and pre-JSON daemons now
// converge automatically -- see TestConfigReplacesOlderDaemon and
// TestConfigReplacesPreJSONDaemon in takeover_test.go.

func TestConfigHelpMentionsStarting(t *testing.T) {
	testSocketEnv(t)
	gui := &guiRecorder{}
	code, stdout, _ := run(t, gui, "config", "--help")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "starts the app if it is not running")
	require.Equal(t, 0, gui.count())
}
