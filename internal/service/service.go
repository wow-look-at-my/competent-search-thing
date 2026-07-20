// Package service installs the app as a per-user login service, so
// the searchbar starts with the desktop session, stays running (a
// crash restarts it) and logs somewhere inspectable: a launchd
// LaunchAgent on macOS (~/Library/LaunchAgents, logs under
// ~/Library/Logs/competent-search-thing/), a systemd user unit on
// Linux (~/.config/systemd/user, logs in the user journal). Windows
// has no support yet and reports so honestly.
//
// The package is pure logic over an injectable Runner (the
// internal/gsettings pattern): production execs launchctl/systemctl,
// unit tests script argv -> output and never touch a real service
// manager. File generation (the plist XML and the unit INI) is pure
// and untagged, so both CI jobs test every OS shape; the one
// darwin-only test lints a generated plist with the real plutil.
//
// Restart semantics are deliberately crash-only on both platforms
// (launchd KeepAlive SuccessfulExit=false, systemd Restart=on-failure):
// a service-spawned copy that finds another instance already running
// hands off over IPC and exits 0, which must NOT respawn-loop.
package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

const (
	// appName is the binary/service base name shared by every path
	// and hint this package emits.
	appName = "competent-search-thing"
	// Label is the launchd job label (reverse-DNS per launchd
	// convention; also the LaunchAgent plist file name).
	Label = "com.wow-look-at-my." + appName
	// UnitName is the systemd user unit file name.
	UnitName = appName + ".service"
	// JournalHint is the linux log-inspection command; the unit logs
	// to the user journal (systemd's default for user services).
	JournalHint = "journalctl --user -u " + appName
)

// runTimeout bounds each launchctl/systemctl invocation the default
// Runner makes; both answer well under this when healthy.
const runTimeout = 10 * time.Second

// Runner executes one service-manager CLI invocation (name is the
// binary, launchctl or systemctl) and returns its stdout. Production
// uses Run; tests inject a scripted fake.
type Runner func(ctx context.Context, name string, args ...string) (stdout string, err error)

// Run is the production Runner: it execs the named binary directly
// (no shell) with a per-call timeout, returning stdout and folding
// any stderr output into the error (the gsettings.Run pattern).
func Run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// Manager performs the install/uninstall/status/restart operations
// for one OS. Every field is injectable so unit tests run headless on
// any platform (GOOS-dispatched values, the internal/sysstats darwin
// pattern); NewManager fills the production values.
type Manager struct {
	// GOOS selects the backend: "darwin" = launchd LaunchAgent,
	// "linux" = systemd user unit, anything else = honest
	// not-supported errors.
	GOOS string
	// Run executes launchctl/systemctl invocations.
	Run Runner
	// Exe is the absolute binary path written into the service file.
	// Production passes the platform.StableExecutable spelling so a
	// Homebrew-Cellar-versioned path never gets baked in (it dies on
	// every brew upgrade -- the GNOME keybinding lesson).
	Exe string
	// Home is the user's home directory (service files and the
	// darwin log dir live under it).
	Home string
	// UID is the numeric user id as a string -- the launchd gui
	// domain ("gui/<uid>"). Unused on linux.
	UID string
	// Getenv reads environment variables (XDG_CONFIG_HOME on linux).
	Getenv func(string) string
}

// NewManager builds the production Manager for the running process.
func NewManager() (*Manager, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving the running binary: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the home directory: %w", err)
	}
	args0 := ""
	if len(os.Args) > 0 {
		args0 = os.Args[0]
	}
	return &Manager{
		GOOS:   runtime.GOOS,
		Run:    Run,
		Exe:    platform.StableExecutable(exe, args0),
		Home:   home,
		UID:    strconv.Itoa(os.Getuid()),
		Getenv: os.Getenv,
	}, nil
}

// InstallResult reports what Install did (idempotent: a converged
// second run reports Changed=false, Started=false).
type InstallResult struct {
	// ServicePath is the plist/unit file location.
	ServicePath string
	// LogHint says where the service's output goes (a file path on
	// darwin, the journalctl command on linux).
	LogHint string
	// Changed reports that the service file was written this run
	// (missing or content differed).
	Changed bool
	// Started reports that the service was started (or restarted to
	// pick up a change) this run.
	Started bool
	// Notes carries honest non-fatal observations for the user.
	Notes []string
}

// UninstallResult reports what Uninstall did (repeat runs no-op).
type UninstallResult struct {
	// ServicePath is the plist/unit file location.
	ServicePath string
	// Removed reports that the service file existed and was removed.
	Removed bool
	// Notes carries honest non-fatal observations for the user.
	Notes []string
}

// StatusInfo is the real observed service state.
type StatusInfo struct {
	// ServicePath is the plist/unit file location.
	ServicePath string
	// Installed reports the service file exists on disk.
	Installed bool
	// Loaded reports the service manager knows the service (launchd
	// job bootstrapped / systemd unit loaded).
	Loaded bool
	// Running reports a live service process.
	Running bool
	// PID is the running process id when the manager reports one
	// (0 = unknown or not running).
	PID int
	// LogHint says where the service's output goes.
	LogHint string
	// Extra carries per-OS honest detail lines (linux enabled state,
	// launchd non-running state, manager unavailability).
	Extra []string
}

// Install writes the service file and loads + starts the service,
// converging on repeat runs.
func (m *Manager) Install(ctx context.Context) (InstallResult, error) {
	switch m.GOOS {
	case "darwin":
		return m.installDarwin(ctx)
	case "linux":
		return m.installLinux(ctx)
	default:
		return InstallResult{}, m.unsupported()
	}
}

// Uninstall stops the service and removes the service file; when
// nothing is installed it no-ops gracefully.
func (m *Manager) Uninstall(ctx context.Context) (UninstallResult, error) {
	switch m.GOOS {
	case "darwin":
		return m.uninstallDarwin(ctx)
	case "linux":
		return m.uninstallLinux(ctx)
	default:
		return UninstallResult{}, m.unsupported()
	}
}

// Status reports the real observed state.
func (m *Manager) Status(ctx context.Context) (StatusInfo, error) {
	switch m.GOOS {
	case "darwin":
		return m.statusDarwin(ctx)
	case "linux":
		return m.statusLinux(ctx)
	default:
		return StatusInfo{}, m.unsupported()
	}
}

// Restart restarts the running service (kill + relaunch).
func (m *Manager) Restart(ctx context.Context) error {
	switch m.GOOS {
	case "darwin":
		return m.restartDarwin(ctx)
	case "linux":
		return m.restartLinux(ctx)
	default:
		return m.unsupported()
	}
}

// unsupported is the honest answer for platforms without a backend.
func (m *Manager) unsupported() error {
	return fmt.Errorf("service management is not supported on %s yet; start %s manually or via your system's autostart mechanism", m.GOOS, appName)
}

// writeIfChanged writes data to path atomically (temp file + rename,
// the config.Save pattern) unless the file already holds exactly
// those bytes; it reports whether a write happened.
func writeIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	return true, nil
}
