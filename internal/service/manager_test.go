package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testExe    = "/opt/csearch/bin/competent-search-thing"
	guiSvc     = "gui/501/" + Label
	guiBrewSvc = "gui/501/" + brewLabel
)

// scriptedRunner answers launchctl/systemctl invocations from a
// canned per-argv table and records every call in order (the
// internal/gsettings scriptedRunner pattern). An invocation nothing
// was scripted for fails the test: the exact argv surface is part of
// the contract under test.
type scriptedRunner struct {
	t     *testing.T
	mu    sync.Mutex
	calls [][]string
	out   map[string][]string
	errs  map[string]error
}

func newScriptedRunner(t *testing.T) *scriptedRunner {
	return &scriptedRunner{t: t, out: map[string][]string{}, errs: map[string]error{}}
}

func argvKey(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

// on scripts one successful invocation; scripting the same argv again
// queues outputs (each call pops the next, the last stays sticky).
func (s *scriptedRunner) on(out, name string, args ...string) {
	k := argvKey(name, args)
	s.out[k] = append(s.out[k], out)
}

// fail scripts a failing invocation.
func (s *scriptedRunner) fail(err error, name string, args ...string) {
	s.errs[argvKey(name, args)] = err
}

func (s *scriptedRunner) run(_ context.Context, name string, args ...string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, append([]string{name}, args...))
	s.mu.Unlock()
	k := argvKey(name, args)
	if err, ok := s.errs[k]; ok {
		return "", err
	}
	if outs, ok := s.out[k]; ok {
		out := outs[0]
		if len(outs) > 1 {
			s.out[k] = outs[1:]
		}
		return out, nil
	}
	s.t.Fatalf("unexpected runner call: %q", append([]string{name}, args...))
	return "", nil
}

func (s *scriptedRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *scriptedRunner) allCalls() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.calls...)
}

// testManager builds a Manager over a temp home + scripted runner.
func testManager(t *testing.T, goos string) (*Manager, *scriptedRunner) {
	t.Helper()
	r := newScriptedRunner(t)
	return &Manager{
		GOOS:   goos,
		Run:    r.run,
		Exe:    testExe,
		Home:   t.TempDir(),
		UID:    "501",
		Getenv: func(string) string { return "" },
	}, r
}

// notLoaded is the launchctl print failure for an unknown service.
var errNotFound = errors.New("Could not find service in domain")

// scriptNoBrewAgent scripts the brew-ownership probe answering "no".
func scriptNoBrewAgent(r *scriptedRunner) {
	r.fail(errNotFound, "launchctl", "print", guiBrewSvc)
}

// --- darwin install -------------------------------------------------

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

// --- darwin uninstall/status/restart --------------------------------

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

// --- linux install --------------------------------------------------

func TestLinuxInstallFreshGraphical(t *testing.T) {
	m, r := testManager(t, "linux")
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	r.on("active\n", "systemctl", "--user", "is-active", "graphical-session.target")
	r.fail(errors.New("exit status 3"), "systemctl", "--user", "is-active", UnitName)
	r.on("", "systemctl", "--user", "start", UnitName)

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Started)
	require.Empty(t, res.Notes)
	require.Equal(t, JournalHint, res.LogHint)

	data, err := os.ReadFile(m.unitPath())
	require.NoError(t, err)
	require.Equal(t, SystemdUnit(testExe), string(data))

	require.Equal(t, [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", UnitName},
		{"systemctl", "--user", "is-active", "graphical-session.target"},
		{"systemctl", "--user", "is-active", UnitName},
		{"systemctl", "--user", "start", UnitName},
	}, r.allCalls())
}

func TestLinuxInstallSecondRunConverges(t *testing.T) {
	m, r := testManager(t, "linux")
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	r.on("active\n", "systemctl", "--user", "is-active", "graphical-session.target")
	r.fail(errors.New("exit status 3"), "systemctl", "--user", "is-active", UnitName)
	r.on("", "systemctl", "--user", "start", UnitName)
	_, err := m.Install(context.Background())
	require.NoError(t, err)

	// Second run: unchanged unit, unit active -- no reload, no start.
	r2 := newScriptedRunner(t)
	m.Run = r2.run
	r2.on("", "systemctl", "--user", "enable", UnitName)
	r2.on("active\n", "systemctl", "--user", "is-active", "graphical-session.target")
	r2.on("active\n", "systemctl", "--user", "is-active", UnitName)

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.False(t, res.Changed, "identical content is not rewritten")
	require.False(t, res.Started)
	require.Equal(t, [][]string{
		{"systemctl", "--user", "enable", UnitName},
		{"systemctl", "--user", "is-active", "graphical-session.target"},
		{"systemctl", "--user", "is-active", UnitName},
	}, r2.allCalls())
}

func TestLinuxInstallChangedWhileRunningRestarts(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte(SystemdUnit("/old/binary")), 0o644))

	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	r.on("active\n", "systemctl", "--user", "is-active", "graphical-session.target")
	r.on("active\n", "systemctl", "--user", "is-active", UnitName)
	r.on("", "systemctl", "--user", "restart", UnitName)

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.True(t, res.Started)
	calls := r.allCalls()
	require.Equal(t, []string{"systemctl", "--user", "restart", UnitName}, calls[len(calls)-1],
		"a changed definition converges the live service via restart")
}

func TestLinuxInstallNoGraphicalSession(t *testing.T) {
	m, r := testManager(t, "linux")
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	r.fail(errors.New("exit status 3"), "systemctl", "--user", "is-active", "graphical-session.target")

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.True(t, res.Changed)
	require.False(t, res.Started, "never start a GUI app without a display")
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "next login")
}

func TestLinuxInstallDaemonReloadFailure(t *testing.T) {
	m, r := testManager(t, "linux")
	r.fail(errors.New("Failed to connect to bus"), "systemctl", "--user", "daemon-reload")

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "systemd user manager is unavailable")
	require.Contains(t, err.Error(), m.unitPath(), "the error says the unit file WAS written")
	require.FileExists(t, m.unitPath())
}

func TestLinuxInstallHonorsXDGConfigHome(t *testing.T) {
	m, r := testManager(t, "linux")
	xdg := t.TempDir()
	m.Getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return xdg
		}
		return ""
	}
	r.on("", "systemctl", "--user", "daemon-reload")
	r.on("", "systemctl", "--user", "enable", UnitName)
	r.fail(errors.New("exit status 3"), "systemctl", "--user", "is-active", "graphical-session.target")

	res, err := m.Install(context.Background())
	require.NoError(t, err)
	require.Equal(t, filepath.Join(xdg, "systemd", "user", UnitName), res.ServicePath)
	require.FileExists(t, res.ServicePath)
}

// --- linux uninstall/status/restart ---------------------------------

func TestLinuxUninstallAndAgain(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte("x"), 0o644))
	r.on("", "systemctl", "--user", "disable", "--now", UnitName)
	r.on("", "systemctl", "--user", "daemon-reload")

	res, err := m.Uninstall(context.Background())
	require.NoError(t, err)
	require.True(t, res.Removed)
	require.Empty(t, res.Notes)
	require.NoFileExists(t, m.unitPath())

	// Again: nothing on disk -- zero systemctl calls, graceful no-op.
	r2 := newScriptedRunner(t)
	m.Run = r2.run
	res, err = m.Uninstall(context.Background())
	require.NoError(t, err)
	require.False(t, res.Removed)
	require.Zero(t, r2.callCount())
}

func TestLinuxUninstallToleratesUnavailableManager(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte("x"), 0o644))
	r.fail(errors.New("no bus"), "systemctl", "--user", "disable", "--now", UnitName)
	r.fail(errors.New("no bus"), "systemctl", "--user", "daemon-reload")

	res, err := m.Uninstall(context.Background())
	require.NoError(t, err, "removing the file still converges at next login")
	require.True(t, res.Removed)
	require.Len(t, res.Notes, 2)
	require.NoFileExists(t, m.unitPath())
}

func TestLinuxStatusRunning(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte("x"), 0o644))
	r.on("LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running\nMainPID=888\n",
		"systemctl", "--user", "show", UnitName, "--property=LoadState,UnitFileState,ActiveState,SubState,MainPID")

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.True(t, st.Installed)
	require.True(t, st.Loaded)
	require.True(t, st.Running)
	require.Equal(t, 888, st.PID)
	require.Equal(t, JournalHint, st.LogHint)
	require.Equal(t, []string{"enabled at login: yes (enabled)"}, st.Extra)
}

func TestLinuxStatusInactiveDisabled(t *testing.T) {
	m, r := testManager(t, "linux")
	r.on("LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead\nMainPID=0\n",
		"systemctl", "--user", "show", UnitName, "--property=LoadState,UnitFileState,ActiveState,SubState,MainPID")

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.False(t, st.Installed)
	require.True(t, st.Loaded)
	require.False(t, st.Running)
	require.Zero(t, st.PID)
	require.Contains(t, st.Extra, "enabled at login: no (disabled)")
	require.Contains(t, st.Extra, "systemd state: inactive (dead)")
}

func TestLinuxStatusManagerUnavailable(t *testing.T) {
	m, r := testManager(t, "linux")
	r.fail(errors.New("Failed to connect to bus"),
		"systemctl", "--user", "show", UnitName, "--property=LoadState,UnitFileState,ActiveState,SubState,MainPID")

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.False(t, st.Loaded)
	require.Len(t, st.Extra, 1)
	require.Contains(t, st.Extra[0], "systemd user manager unavailable")
}

func TestLinuxRestart(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.unitPath(), []byte("x"), 0o644))
	r.on("", "systemctl", "--user", "restart", UnitName)

	require.NoError(t, m.Restart(context.Background()))
	require.Equal(t, [][]string{{"systemctl", "--user", "restart", UnitName}}, r.allCalls())
}

func TestLinuxRestartNotInstalled(t *testing.T) {
	m, _ := testManager(t, "linux")
	err := m.Restart(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "service install")
}

// --- shared ---------------------------------------------------------

func TestUnsupportedOS(t *testing.T) {
	m, r := testManager(t, "windows")
	ctx := context.Background()

	_, err := m.Install(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	_, err = m.Uninstall(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	_, err = m.Status(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	err = m.Restart(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	require.Zero(t, r.callCount())
}

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	changed, err := writeIfChanged(path, []byte("one"), 0o644)
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = writeIfChanged(path, []byte("one"), 0o644)
	require.NoError(t, err)
	require.False(t, changed, "identical content is not rewritten")

	changed, err = writeIfChanged(path, []byte("two"), 0o644)
	require.NoError(t, err)
	require.True(t, changed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "two", string(data))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files left behind")
}

func TestProductionRun(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", "echo hi")
	require.NoError(t, err)
	require.Equal(t, "hi\n", out)

	_, err = Run(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom", "stderr is folded into the error")
	require.Contains(t, err.Error(), "exit status 3")
}

func TestNewManagerProduction(t *testing.T) {
	m, err := NewManager()
	require.NoError(t, err)
	require.NotEmpty(t, m.GOOS)
	require.NotEmpty(t, m.Exe)
	require.True(t, filepath.IsAbs(m.Exe))
	require.NotEmpty(t, m.Home)
	require.NotEmpty(t, m.UID)
	require.NotNil(t, m.Run)
	require.NotNil(t, m.Getenv)
}
