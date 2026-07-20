package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/service"
)

// withServiceManager swaps the newServiceManager seam for a Manager
// over a temp home and the given inline runner, restoring it after
// the test. The orchestration matrix itself is tested in
// internal/service; these tests pin the cobra wiring + printing.
func withServiceManager(t *testing.T, goos string, run service.Runner) *service.Manager {
	t.Helper()
	m := &service.Manager{
		GOOS:   goos,
		Run:    run,
		Exe:    "/opt/bin/competent-search-thing",
		Home:   t.TempDir(),
		UID:    "501",
		Getenv: func(string) string { return "" },
	}
	old := newServiceManager
	newServiceManager = func() (*service.Manager, error) { return m, nil }
	t.Cleanup(func() { newServiceManager = old })
	return m
}

// systemctlScript answers systemctl invocations from a joined-argv
// map; anything unscripted fails the test.
func systemctlScript(t *testing.T, script map[string]string, fails map[string]error) service.Runner {
	return func(_ context.Context, name string, args ...string) (string, error) {
		t.Helper()
		require.Equal(t, "systemctl", name, "only systemctl runs on linux")
		key := strings.Join(args, " ")
		if err, ok := fails[key]; ok {
			return "", err
		}
		out, ok := script[key]
		require.True(t, ok, "unexpected systemctl call: %q", key)
		return out, nil
	}
}

func TestServiceGroupShowsHelp(t *testing.T) {
	gui := &guiRecorder{}
	code, stdout, _ := run(t, gui, "service")
	require.Equal(t, 0, code)
	for _, sub := range []string{"install", "uninstall", "status", "restart"} {
		require.Contains(t, stdout, sub)
	}
	require.Equal(t, 0, gui.count(), "service commands never boot the GUI")
}

func TestServiceInstallLinux(t *testing.T) {
	withServiceManager(t, "linux", systemctlScript(t, map[string]string{
		"--user daemon-reload":                         "",
		"--user enable competent-search-thing.service": "",
		"--user is-active graphical-session.target":    "active\n",
		"--user start competent-search-thing.service":  "",
	}, map[string]error{
		"--user is-active competent-search-thing.service": errors.New("exit status 3"),
	}))
	gui := &guiRecorder{}

	code, stdout, stderr := run(t, gui, "service", "install")
	require.Equal(t, 0, code, "stderr: %s", stderr)
	require.Contains(t, stdout, "wrote ")
	require.Contains(t, stdout, "competent-search-thing.service")
	require.Contains(t, stdout, "service started (starts at login, restarts on crash)")
	require.Contains(t, stdout, "logs: journalctl --user -u competent-search-thing")
	require.Equal(t, 0, gui.count())
}

func TestServiceInstallPrintsNotes(t *testing.T) {
	withServiceManager(t, "linux", systemctlScript(t, map[string]string{
		"--user daemon-reload":                         "",
		"--user enable competent-search-thing.service": "",
	}, map[string]error{
		"--user is-active graphical-session.target": errors.New("exit status 3"),
	}))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "install")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "note: no active graphical session detected")
}

func TestServiceStatusLinux(t *testing.T) {
	m := withServiceManager(t, "linux", systemctlScript(t, map[string]string{
		"--user show competent-search-thing.service --property=LoadState,UnitFileState,ActiveState,SubState,MainPID": "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\nSubState=running\nMainPID=999\n",
	}, nil))
	unitPath := filepath.Join(m.Home, ".config", "systemd", "user", "competent-search-thing.service")
	require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
	require.NoError(t, os.WriteFile(unitPath, []byte("x"), 0o644))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "status")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "service file: "+unitPath+" (installed)")
	require.Contains(t, stdout, "loaded: yes")
	require.Contains(t, stdout, "running: yes (pid 999)")
	require.Contains(t, stdout, "enabled at login: yes (enabled)")
	require.Contains(t, stdout, "logs: journalctl --user -u competent-search-thing")
}

func TestServiceStatusNotInstalled(t *testing.T) {
	withServiceManager(t, "linux", systemctlScript(t, nil, map[string]error{
		"--user show competent-search-thing.service --property=LoadState,UnitFileState,ActiveState,SubState,MainPID": errors.New("Failed to connect to bus"),
	}))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "status")
	require.Equal(t, 0, code, "status reports honestly instead of failing")
	require.Contains(t, stdout, "(not installed)")
	require.Contains(t, stdout, "loaded: no")
	require.Contains(t, stdout, "running: no")
	require.Contains(t, stdout, "systemd user manager unavailable")
}

func TestServiceRestartLinux(t *testing.T) {
	m := withServiceManager(t, "linux", systemctlScript(t, map[string]string{
		"--user restart competent-search-thing.service": "",
	}, nil))
	unitPath := filepath.Join(m.Home, ".config", "systemd", "user", "competent-search-thing.service")
	require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
	require.NoError(t, os.WriteFile(unitPath, []byte("x"), 0o644))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "restart")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "service restarted")
}

func TestServiceRestartNotInstalledFails(t *testing.T) {
	withServiceManager(t, "linux", systemctlScript(t, nil, nil))
	gui := &guiRecorder{}

	code, _, stderr := run(t, gui, "service", "restart")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "service install")
}

func TestServiceUninstallNothingInstalled(t *testing.T) {
	withServiceManager(t, "linux", systemctlScript(t, nil, nil))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "uninstall")
	require.Equal(t, 0, code, "repeated uninstall no-ops gracefully")
	require.Contains(t, stdout, "service was not installed; nothing to do")
}

func TestServiceUninstallRemoves(t *testing.T) {
	m := withServiceManager(t, "linux", systemctlScript(t, map[string]string{
		"--user disable --now competent-search-thing.service": "",
		"--user daemon-reload":                                "",
	}, nil))
	unitPath := filepath.Join(m.Home, ".config", "systemd", "user", "competent-search-thing.service")
	require.NoError(t, os.MkdirAll(filepath.Dir(unitPath), 0o755))
	require.NoError(t, os.WriteFile(unitPath, []byte("x"), 0o644))
	gui := &guiRecorder{}

	code, stdout, _ := run(t, gui, "service", "uninstall")
	require.Equal(t, 0, code)
	require.Contains(t, stdout, "service uninstalled (removed "+unitPath+")")
	require.NoFileExists(t, unitPath)
}

func TestServiceUnsupportedOS(t *testing.T) {
	withServiceManager(t, "windows", func(context.Context, string, ...string) (string, error) {
		t.Fatal("no runner call may happen on an unsupported OS")
		return "", nil
	})
	gui := &guiRecorder{}

	for _, sub := range []string{"install", "uninstall", "status", "restart"} {
		code, _, stderr := run(t, gui, "service", sub)
		require.Equal(t, 1, code, sub)
		require.Contains(t, stderr, "not supported on windows", sub)
	}
}

func TestServiceManagerConstructionErrorPropagates(t *testing.T) {
	old := newServiceManager
	newServiceManager = func() (*service.Manager, error) { return nil, errors.New("no home dir") }
	t.Cleanup(func() { newServiceManager = old })
	gui := &guiRecorder{}

	for _, sub := range []string{"install", "uninstall", "status", "restart"} {
		code, _, stderr := run(t, gui, "service", sub)
		require.Equal(t, 1, code, sub)
		require.Contains(t, stderr, "no home dir", sub)
	}
}
