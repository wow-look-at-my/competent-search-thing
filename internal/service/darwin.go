package service

// The launchd (macOS) backend. Deliberately UNTAGGED pure logic over
// the Runner seam -- the Manager.GOOS field dispatches here, so the
// linux CI job tests every darwin shape too (the internal/sysstats
// darwin.go pattern). Invocations follow the modern launchctl
// surface, verified against launchctl(1): bootstrap/bootout on the
// gui/<uid> domain, enable on the service target, kickstart -k for
// kill+restart, print for real state.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// launchAgentPath is the plist location under ~/Library/LaunchAgents.
func (m *Manager) launchAgentPath() string {
	return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist")
}

// brewAgentPath is where a `brew services`-managed plist would live.
func (m *Manager) brewAgentPath() string {
	return filepath.Join(m.Home, "Library", "LaunchAgents", brewLabel+".plist")
}

// darwinLogPath is the service log file under ~/Library/Logs.
func (m *Manager) darwinLogPath() string {
	return filepath.Join(m.Home, "Library", "Logs", appName, appName+".log")
}

// guiDomain is the per-user launchd GUI domain target.
func (m *Manager) guiDomain() string { return "gui/" + m.UID }

// guiService is the service target inside the GUI domain.
func (m *Manager) guiService() string { return m.guiDomain() + "/" + Label }

// jobLoaded reports whether launchd knows the labeled job (launchctl
// print on the service target succeeds).
func (m *Manager) jobLoaded(ctx context.Context, label string) bool {
	_, err := m.Run(ctx, "launchctl", "print", m.guiDomain()+"/"+label)
	return err == nil
}

// brewServiceOwner reports a `brew services` unit already owning the
// app's startup (plist present or job loaded), naming the evidence.
// Exactly one unit may own startup: brew's keep_alive true respawns
// a clean-exiting loser every ~10s, each respawn re-summoning the
// bar. The brew unit wins -- its opt_bin path tracks brew upgrades
// and brew users expect `brew services` to own what it manages.
func (m *Manager) brewServiceOwner(ctx context.Context) (evidence string, owned bool) {
	if _, err := os.Stat(m.brewAgentPath()); err == nil {
		return m.brewAgentPath(), true
	}
	if m.jobLoaded(ctx, brewLabel) {
		return "loaded launchd job " + brewLabel, true
	}
	return "", false
}

// installDarwin writes the LaunchAgent plist and bootstraps it into
// the user's GUI domain, converging on repeat runs.
func (m *Manager) installDarwin(ctx context.Context) (InstallResult, error) {
	res := InstallResult{ServicePath: m.launchAgentPath(), LogHint: m.darwinLogPath()}
	if ev, owned := m.brewServiceOwner(ctx); owned {
		return res, fmt.Errorf("brew services already manages %s (%s); exactly one startup manager may own the app -- stop it first with 'brew services stop pazer/build/%s', or keep using brew services instead",
			appName, ev, appName)
	}
	if err := os.MkdirAll(filepath.Dir(res.ServicePath), 0o755); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(res.LogHint), 0o755); err != nil {
		return res, err
	}
	changed, err := writeIfChanged(res.ServicePath, []byte(LaunchAgentPlist(m.Exe, res.LogHint)), 0o644)
	if err != nil {
		return res, err
	}
	res.Changed = changed
	loaded := m.jobLoaded(ctx, Label)
	if loaded && changed {
		// The loaded job still runs the old definition; reload it.
		if _, err := m.Run(ctx, "launchctl", "bootout", m.guiService()); err != nil {
			return res, fmt.Errorf("unloading the previous agent: %w", err)
		}
		loaded = false
	}
	// Clear any disabled override BEFORE bootstrap -- launchd refuses
	// to bootstrap a disabled service. A failure here is noted, not
	// fatal: on a never-disabled service the bootstrap below is the
	// real gate.
	if _, err := m.Run(ctx, "launchctl", "enable", m.guiService()); err != nil {
		res.Notes = append(res.Notes, "launchctl enable failed: "+err.Error())
	}
	if !loaded {
		if _, err := m.Run(ctx, "launchctl", "bootstrap", m.guiDomain(), res.ServicePath); err != nil {
			return res, fmt.Errorf("loading the agent (run this from a terminal inside your desktop session): %w", err)
		}
		res.Started = true // RunAtLoad starts the app with the bootstrap
	}
	return res, nil
}

// uninstallDarwin unloads the job and removes the plist; repeat runs
// no-op gracefully. The log directory is deliberately left behind.
func (m *Manager) uninstallDarwin(ctx context.Context) (UninstallResult, error) {
	res := UninstallResult{ServicePath: m.launchAgentPath()}
	if m.jobLoaded(ctx, Label) {
		if _, err := m.Run(ctx, "launchctl", "bootout", m.guiService()); err != nil {
			return res, fmt.Errorf("unloading the agent: %w", err)
		}
	}
	switch err := os.Remove(res.ServicePath); {
	case err == nil:
		res.Removed = true
	case !os.IsNotExist(err):
		return res, err
	}
	return res, nil
}

// statusDarwin reports the plist presence plus launchd's real state.
func (m *Manager) statusDarwin(ctx context.Context) (StatusInfo, error) {
	st := StatusInfo{ServicePath: m.launchAgentPath(), LogHint: m.darwinLogPath()}
	if _, err := os.Stat(st.ServicePath); err == nil {
		st.Installed = true
	}
	if ev, owned := m.brewServiceOwner(ctx); owned {
		st.Extra = append(st.Extra, "brew services also manages the app ("+ev+"); exactly one startup manager should own it")
	}
	out, err := m.Run(ctx, "launchctl", "print", m.guiService())
	if err != nil {
		return st, nil // not loaded
	}
	st.Loaded = true
	state, pid := parseLaunchdPrint(out)
	st.Running = state == "running"
	st.PID = pid
	if state != "" && state != "running" {
		st.Extra = append(st.Extra, "launchd state: "+state)
	}
	return st, nil
}

// restartDarwin kills and relaunches the loaded job.
func (m *Manager) restartDarwin(ctx context.Context) error {
	if !m.jobLoaded(ctx, Label) {
		return fmt.Errorf("the service is not loaded; run '%s service install' first", appName)
	}
	if _, err := m.Run(ctx, "launchctl", "kickstart", "-k", m.guiService()); err != nil {
		return fmt.Errorf("restarting the agent: %w", err)
	}
	return nil
}

// parseLaunchdPrint extracts the "state = X" and "pid = N" facts from
// launchctl print output (first occurrence each; absent = "", 0).
func parseLaunchdPrint(out string) (state string, pid int) {
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case key == "state" && state == "":
			state = val
		case key == "pid" && pid == 0:
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				pid = n
			}
		}
	}
	return state, pid
}
