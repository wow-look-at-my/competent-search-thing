package service

// The launchd (darwin) backend's install/uninstall/status/restart
// tests, driven headlessly through the scripted runner on every OS
// (manager_test.go holds the shared harness).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDarwinInstallFresh(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "print", guiSvc)
	r.on("", "launchctl", "enable", guiSvc)
	r.on("", "launchctl", "bootstrap", "gui/501", m.launchAgentPath())

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Started)
	require.Empty(t, res.Notes)
	require.Equal(t, m.launchAgentPath(), res.ServicePath)
	require.Equal(t, m.darwinLogPath(), res.LogHint)

	data, err := os.ReadFile(res.ServicePath)
	require.NoError(t, err)
	require.Equal(t, LaunchAgentPlist(testExe, m.darwinLogPath()), string(data))
	require.DirExists(t, filepath.Dir(m.darwinLogPath()), "the log directory is pre-created")

	require.Equal(t, [][]string{
		{"launchctl", "print", guiBrewSvc},
		{"launchctl", "print", guiSvc},
		{"launchctl", "enable", guiSvc},
		{"launchctl", "bootstrap", "gui/501", m.launchAgentPath()},
	}, r.allCalls())
}

func TestDarwinInstallSecondRunConverges(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "print", guiSvc)
	r.on("", "launchctl", "enable", guiSvc)
	r.on("", "launchctl", "bootstrap", "gui/501", m.launchAgentPath())
	_, err := m.Install(context.Background())
	require.NoError(t, err)

	// Second run: the job is loaded now; nothing may be rewritten,
	// booted out or re-bootstrapped.
	r2 := newScriptedRunner(t)
	m.Run = r2.run
	scriptNoBrewAgent(r2)
	r2.on("state = running\npid = 4242\n", "launchctl", "print", guiSvc)
	r2.on("", "launchctl", "enable", guiSvc)

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.False(t, res.Changed, "identical content is not rewritten")
	require.False(t, res.Started, "a loaded unchanged service is left alone")
	require.Equal(t, [][]string{
		{"launchctl", "print", guiBrewSvc},
		{"launchctl", "print", guiSvc},
		{"launchctl", "enable", guiSvc},
	}, r2.allCalls())
}

func TestDarwinInstallChangedWhileLoadedReloads(t *testing.T) {
	m, r := testManager(t, "darwin")
	// Simulate a previous install of a different binary path.
	require.NoError(t, os.MkdirAll(filepath.Dir(m.launchAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.launchAgentPath(),
		[]byte(LaunchAgentPlist("/old/binary", m.darwinLogPath())), 0o644))

	scriptNoBrewAgent(r)
	r.on("state = running\n", "launchctl", "print", guiSvc)
	r.on("", "launchctl", "bootout", guiSvc)
	r.on("", "launchctl", "enable", guiSvc)
	r.on("", "launchctl", "bootstrap", "gui/501", m.launchAgentPath())

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Started)
	require.Equal(t, [][]string{
		{"launchctl", "print", guiBrewSvc},
		{"launchctl", "print", guiSvc},
		{"launchctl", "bootout", guiSvc},
		{"launchctl", "enable", guiSvc},
		{"launchctl", "bootstrap", "gui/501", m.launchAgentPath()},
	}, r.allCalls())

	data, err := os.ReadFile(m.launchAgentPath())
	require.NoError(t, err)
	require.Contains(t, string(data), testExe, "the plist now names the current binary")
}

func TestDarwinInstallEnableFailureIsANote(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "print", guiSvc)
	r.fail(errors.New("enable exploded"), "launchctl", "enable", guiSvc)
	r.on("", "launchctl", "bootstrap", "gui/501", m.launchAgentPath())

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Started, "bootstrap still runs")
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "enable exploded")
}

func TestDarwinInstallBootstrapFailureIsFatal(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "print", guiSvc)
	r.on("", "launchctl", "enable", guiSvc)
	r.fail(errors.New("Bootstrap failed: 5: Input/output error"),
		"launchctl", "bootstrap", "gui/501", m.launchAgentPath())

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "Bootstrap failed")
	require.Contains(t, err.Error(), "desktop session", "the error points at the common SSH-session cause")
}

// TestDarwinInstallRefusesWhenBrewServicesOwns pins the exactly-one-
// owner rule: a `brew services` plist (keep_alive true) plus our
// agent would respawn-loop the exit-0 handoff copy, re-summoning the
// bar every ~10s. The brew unit wins; install refuses with the stop
// command.
func TestDarwinInstallRefusesWhenBrewServicesOwns(t *testing.T) {
	m, r := testManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.brewAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.brewAgentPath(), []byte("brew"), 0o644))

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "brew services already manages")
	require.Contains(t, err.Error(), "brew services stop pazer/build/competent-search-thing")
	require.Zero(t, r.callCount(), "the plist file alone is evidence; nothing is written or run")
	require.NoFileExists(t, m.launchAgentPath())
}

func TestDarwinInstallRefusesWhenBrewJobLoadedWithoutPlist(t *testing.T) {
	m, r := testManager(t, "darwin")
	r.on("state = running\n", "launchctl", "print", guiBrewSvc)

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), brewLabel)
	require.NoFileExists(t, m.launchAgentPath())
}

func TestDarwinUninstallLoadedAndAgain(t *testing.T) {
	m, r := testManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.launchAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.launchAgentPath(), []byte("x"), 0o644))
	r.on("state = running\n", "launchctl", "print", guiSvc)
	r.on("", "launchctl", "bootout", guiSvc)

	res, err := m.Uninstall(context.Background())
	require.NoError(t, err)
	require.True(t, res.Removed)
	require.NoFileExists(t, m.launchAgentPath())
	require.Equal(t, [][]string{
		{"launchctl", "print", guiSvc},
		{"launchctl", "bootout", guiSvc},
	}, r.allCalls())

	// Again: nothing loaded, nothing on disk -- a graceful no-op.
	r2 := newScriptedRunner(t)
	m.Run = r2.run
	r2.fail(errNotFound, "launchctl", "print", guiSvc)
	res, err = m.Uninstall(context.Background())
	require.NoError(t, err)
	require.False(t, res.Removed)
}

func TestDarwinUninstallBootoutFailureIsFatal(t *testing.T) {
	m, r := testManager(t, "darwin")
	r.on("state = running\n", "launchctl", "print", guiSvc)
	r.fail(errors.New("bootout exploded"), "launchctl", "bootout", guiSvc)

	_, err := m.Uninstall(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "bootout exploded")
}

func TestDarwinStatusRunning(t *testing.T) {
	m, r := testManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.launchAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.launchAgentPath(), []byte("x"), 0o644))
	scriptNoBrewAgent(r)
	r.on("com.wow-look-at-my.competent-search-thing = {\n\tactive count = 1\n\tstate = running\n\tprogram = /opt/x\n\tpid = 777\n}\n",
		"launchctl", "print", guiSvc)

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.True(t, st.Installed)
	require.True(t, st.Loaded)
	require.True(t, st.Running)
	require.Equal(t, 777, st.PID)
	require.Equal(t, m.darwinLogPath(), st.LogHint)
	require.Empty(t, st.Extra)
}

func TestDarwinStatusNotLoaded(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.fail(errNotFound, "launchctl", "print", guiSvc)

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.False(t, st.Installed)
	require.False(t, st.Loaded)
	require.False(t, st.Running)
	require.Zero(t, st.PID)
}

func TestDarwinStatusLoadedNotRunning(t *testing.T) {
	m, r := testManager(t, "darwin")
	scriptNoBrewAgent(r)
	r.on("\tstate = not running\n\tlast exit code = 0\n", "launchctl", "print", guiSvc)

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.True(t, st.Loaded)
	require.False(t, st.Running)
	require.Contains(t, st.Extra, "launchd state: not running")
}

func TestDarwinStatusNotesBrewOwnership(t *testing.T) {
	m, r := testManager(t, "darwin")
	require.NoError(t, os.MkdirAll(filepath.Dir(m.brewAgentPath()), 0o755))
	require.NoError(t, os.WriteFile(m.brewAgentPath(), []byte("brew"), 0o644))
	r.fail(errNotFound, "launchctl", "print", guiSvc)

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.Len(t, st.Extra, 1)
	require.Contains(t, st.Extra[0], "brew services also manages")
}

func TestDarwinRestart(t *testing.T) {
	m, r := testManager(t, "darwin")
	r.on("state = running\n", "launchctl", "print", guiSvc)
	r.on("", "launchctl", "kickstart", "-k", guiSvc)

	require.NoError(t, m.Restart(context.Background()))
	require.Equal(t, [][]string{
		{"launchctl", "print", guiSvc},
		{"launchctl", "kickstart", "-k", guiSvc},
	}, r.allCalls())
}

func TestDarwinRestartNotLoaded(t *testing.T) {
	m, r := testManager(t, "darwin")
	r.fail(errNotFound, "launchctl", "print", guiSvc)

	err := m.Restart(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "service install")
}

func TestParseLaunchdPrint(t *testing.T) {
	cases := []struct {
		name, out string
		state     string
		pid       int
	}{
		{"running", "\tstate = running\n\tpid = 123\n", "running", 123},
		{"not running", "\tstate = not running\n", "not running", 0},
		{"first wins", "state = running\nstate = spawned\npid = 5\npid = 9\n", "running", 5},
		{"garbage", "no equals here\n= dangling\n", "", 0},
		{"non-numeric pid", "state = running\npid = soon\n", "running", 0},
		{"empty", "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, pid := parseLaunchdPrint(tc.out)
			require.Equal(t, tc.state, state)
			require.Equal(t, tc.pid, pid)
		})
	}
}
