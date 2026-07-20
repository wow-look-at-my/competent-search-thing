package service

// The systemd user-unit (Linux) backend. UNTAGGED pure logic over the
// Runner seam, like darwin.go -- Manager.GOOS dispatches here, so the
// darwin CI job tests every linux shape too.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// unitDir is $XDG_CONFIG_HOME/systemd/user (default
// ~/.config/systemd/user), the standard per-user unit directory.
func (m *Manager) unitDir() string {
	if x := m.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user")
	}
	return filepath.Join(m.Home, ".config", "systemd", "user")
}

// unitPath is the unit file location.
func (m *Manager) unitPath() string { return filepath.Join(m.unitDir(), UnitName) }

// userUnitActive reports `systemctl --user is-active <unit>` success
// (is-active exits non-zero for anything but "active").
func (m *Manager) userUnitActive(ctx context.Context, unit string) bool {
	_, err := m.Run(ctx, "systemctl", "--user", "is-active", unit)
	return err == nil
}

// installLinux writes the user unit, enables it for the graphical
// session, and starts it when one is up, converging on repeat runs.
func (m *Manager) installLinux(ctx context.Context) (InstallResult, error) {
	res := InstallResult{ServicePath: m.unitPath(), LogHint: JournalHint}
	if err := os.MkdirAll(m.unitDir(), 0o755); err != nil {
		return res, err
	}
	changed, err := writeIfChanged(res.ServicePath, []byte(SystemdUnit(m.Exe)), 0o644)
	if err != nil {
		return res, err
	}
	res.Changed = changed
	if changed {
		if _, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return res, fmt.Errorf("wrote %s, but the systemd user manager is unavailable: %w", res.ServicePath, err)
		}
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
		return res, fmt.Errorf("enabling the unit: %w", err)
	}
	if !m.userUnitActive(ctx, "graphical-session.target") {
		// The unit binds to the graphical session; starting it now,
		// without a desktop, would flap a GTK/WebKit app that cannot
		// reach a display.
		res.Notes = append(res.Notes, "no active graphical session detected; the service starts at your next login")
		return res, nil
	}
	switch running := m.userUnitActive(ctx, UnitName); {
	case running && changed:
		// Converge the live service onto the changed definition.
		if _, err := m.Run(ctx, "systemctl", "--user", "restart", UnitName); err != nil {
			return res, fmt.Errorf("restarting the unit: %w", err)
		}
		res.Started = true
	case !running:
		if _, err := m.Run(ctx, "systemctl", "--user", "start", UnitName); err != nil {
			return res, fmt.Errorf("starting the unit: %w", err)
		}
		res.Started = true
	}
	return res, nil
}

// uninstallLinux stops + disables the unit and removes the file;
// repeat runs no-op gracefully. A missing systemd user manager only
// earns a note -- removing the file still converges at next login.
func (m *Manager) uninstallLinux(ctx context.Context) (UninstallResult, error) {
	res := UninstallResult{ServicePath: m.unitPath()}
	if _, err := os.Stat(res.ServicePath); err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, err
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "disable", "--now", UnitName); err != nil {
		res.Notes = append(res.Notes, "systemctl disable --now failed: "+err.Error())
	}
	if err := os.Remove(res.ServicePath); err != nil && !os.IsNotExist(err) {
		return res, err
	}
	res.Removed = true
	if _, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		res.Notes = append(res.Notes, "systemctl daemon-reload failed: "+err.Error())
	}
	return res, nil
}

// statusLinux reports the unit file presence plus systemd's real
// state via `systemctl show` (which succeeds even for unknown units,
// answering key=value properties).
func (m *Manager) statusLinux(ctx context.Context) (StatusInfo, error) {
	st := StatusInfo{ServicePath: m.unitPath(), LogHint: JournalHint}
	if _, err := os.Stat(st.ServicePath); err == nil {
		st.Installed = true
	}
	out, err := m.Run(ctx, "systemctl", "--user", "show", UnitName,
		"--property=LoadState,UnitFileState,ActiveState,SubState,MainPID")
	if err != nil {
		st.Extra = append(st.Extra, "systemd user manager unavailable: "+err.Error())
		return st, nil
	}
	props := parseShowProps(out)
	st.Loaded = props["LoadState"] == "loaded"
	st.Running = props["ActiveState"] == "active"
	if n, err := strconv.Atoi(props["MainPID"]); err == nil && n > 0 {
		st.PID = n
	}
	if ufs := props["UnitFileState"]; ufs != "" {
		enabled := "no"
		if ufs == "enabled" {
			enabled = "yes"
		}
		st.Extra = append(st.Extra, "enabled at login: "+enabled+" ("+ufs+")")
	}
	if sub := props["SubState"]; sub != "" && !st.Running {
		st.Extra = append(st.Extra, "systemd state: "+props["ActiveState"]+" ("+sub+")")
	}
	return st, nil
}

// restartLinux restarts the unit, with a friendly pointer when the
// service was never installed.
func (m *Manager) restartLinux(ctx context.Context) error {
	if _, err := os.Stat(m.unitPath()); err != nil {
		return fmt.Errorf("the service is not installed; run '%s service install' first", appName)
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "restart", UnitName); err != nil {
		return fmt.Errorf("restarting the unit: %w", err)
	}
	return nil
}

// parseShowProps parses `systemctl show` key=value lines.
func parseShowProps(out string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return props
}
