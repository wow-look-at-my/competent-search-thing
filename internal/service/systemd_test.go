package service

// The systemd (linux) backend's install/uninstall/status/restart
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

// TestLinuxInstallRefusesWhenDebOwns pins the linux exactly-one-owner
// rule: a user unit at the same name SHADOWS the deb-shipped system
// unit (systemd's ~/.config precedence), silently replacing the
// packaged definition. The deb wins; install refuses.
func TestLinuxInstallRefusesWhenDebOwns(t *testing.T) {
	m, r := testManager(t, "linux")
	sys := t.TempDir()
	m.SystemUnitDirs = []string{sys}
	debUnit := filepath.Join(sys, UnitName)
	require.NoError(t, os.WriteFile(debUnit, []byte("deb"), 0o644))

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), debUnit)
	require.Contains(t, err.Error(), "exactly one startup manager")
	require.Zero(t, r.callCount(), "the unit file alone is evidence; nothing is written or run")
	require.NoFileExists(t, m.unitPath())
}

func TestLinuxInstallRefusesWhenBrewServicesOwns(t *testing.T) {
	m, r := testManager(t, "linux")
	require.NoError(t, os.MkdirAll(m.unitDir(), 0o755))
	require.NoError(t, os.WriteFile(m.brewUnitPath(), []byte("brew"), 0o644))

	_, err := m.Install(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "brew services")
	require.Contains(t, err.Error(), "brew services stop pazer/build/competent-search-thing")
	require.Zero(t, r.callCount())
	require.NoFileExists(t, m.unitPath())
}

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

func TestLinuxStatusNotesForeignOwner(t *testing.T) {
	m, r := testManager(t, "linux")
	sys := t.TempDir()
	m.SystemUnitDirs = []string{sys}
	require.NoError(t, os.WriteFile(filepath.Join(sys, UnitName), []byte("deb"), 0o644))
	r.fail(errors.New("Failed to connect to bus"),
		"systemctl", "--user", "show", UnitName, "--property=LoadState,UnitFileState,ActiveState,SubState,MainPID")

	st, err := m.Status(context.Background())
	require.NoError(t, err)
	require.Contains(t, st.Extra[0], "deb-installed unit")
	require.Contains(t, st.Extra[0], "also manages the app")
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
