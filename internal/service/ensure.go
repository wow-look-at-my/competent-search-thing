package service

// Ensure is the app's automatic first-run registration: the decision
// matrix the GUI runs (async) at every startup so a plain install --
// deb, brew, raw binary -- becomes a login service with zero manual
// steps (the 2026-07-21 "obviously it must be fully automatic" owner
// ruling; Homebrew 6.0's default-on sandboxes killed every in-formula
// setup path, so the app itself is the only fully-automatic surface
// left). Unlike Install it NEVER starts, restarts or boots anything:
// the app is already running -- possibly AS the service instance, so
// a start here could kill or duplicate ourselves -- and the service
// simply takes over from the next login. On darwin that means no
// launchctl bootstrap either: bootstrapping a RunAtLoad agent launches
// a copy immediately (which would hand off over IPC and summon the
// bar uninvited); launchd loads ~/Library/LaunchAgents on its own at
// the next login, so writing the plist plus clearing any disabled
// override is the whole job.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// EnsureAction classifies what Ensure decided.
type EnsureAction int

const (
	// EnsureUnsupported: no backend for this OS; nothing was done.
	EnsureUnsupported EnsureAction = iota
	// EnsureYielded: another manager (brew services, the deb unit)
	// owns login startup; nothing was written.
	EnsureYielded
	// EnsureOptedOut: the opt-out marker is present ('service
	// uninstall' wrote it); nothing was written.
	EnsureOptedOut
	// EnsureCurrent: our service file exists and is byte-current;
	// nothing was written or run.
	EnsureCurrent
	// EnsureRegistered: fresh registration -- the service file was
	// written and enabled for login startup (never started).
	EnsureRegistered
	// EnsureRepaired: the existing file was stale (binary moved --
	// e.g. a brew upgrade) and was rewritten in place.
	EnsureRepaired
	// EnsureUnavailable: the service manager is unreachable (no user
	// bus / no GUI launchd domain -- headless or CI); a fresh write
	// was rolled back so the state stays "not registered".
	EnsureUnavailable
)

// EnsureResult reports the decision plus everything the caller's one
// log line needs; the app layer only formats, never re-derives.
type EnsureResult struct {
	// Action is the matrix verdict.
	Action EnsureAction
	// ServicePath is our plist/unit location.
	ServicePath string
	// Exe is the binary path written into the service file (the
	// repair log's "new" side).
	Exe string
	// Owner names the foreign owner on EnsureYielded (a full phrase:
	// "brew services (plist ...)", "the deb-installed unit ...").
	Owner string
	// OursToo reports that our own service file ALSO exists beside
	// the foreign owner's (the caller's cleanup hint).
	OursToo bool
	// Hint carries optional guidance for the yield log (the linux
	// brew-services stop-once pointer).
	Hint string
	// PreviousExe is the stale file's old binary path when it could
	// be parsed (the repair log's "old" side; "" otherwise).
	PreviousExe string
	// Note carries detail for EnsureUnavailable (what failed) and the
	// marker path for EnsureOptedOut.
	Note string
}

// Ensure runs the registration decision matrix for the running OS.
// The order is fixed: foreign owner -> opt-out marker -> current ->
// stale repair -> fresh registration. Errors are unexpected local
// failures (mkdir/write); the expected degrades (foreign owner,
// opt-out, unreachable manager) are Actions, not errors.
func (m *Manager) Ensure(ctx context.Context) (EnsureResult, error) {
	switch m.GOOS {
	case "darwin":
		return m.ensureDarwin(ctx)
	case "linux":
		return m.ensureLinux(ctx)
	default:
		return EnsureResult{Action: EnsureUnsupported}, nil
	}
}

// ensureDarwin is the launchd half of the matrix.
func (m *Manager) ensureDarwin(ctx context.Context) (EnsureResult, error) {
	res := EnsureResult{ServicePath: m.launchAgentPath(), Exe: m.Exe}
	if ev, owned := m.brewServiceOwner(ctx); owned {
		res.Action = EnsureYielded
		res.Owner = "brew services (" + ev + ")"
		res.OursToo = fileExists(res.ServicePath)
		return res, nil
	}
	if m.optedOut() {
		res.Action = EnsureOptedOut
		res.Note = m.OptOutFile
		return res, nil
	}
	desired := []byte(LaunchAgentPlist(m.Exe, m.darwinLogPath()))
	cur, readErr := os.ReadFile(res.ServicePath)
	fresh := readErr != nil // unreadable = treat as a fresh write
	if readErr == nil && string(cur) == string(desired) {
		res.Action = EnsureCurrent
		return res, nil
	}
	if !fresh {
		res.PreviousExe = plistProgram(cur)
	}
	if err := os.MkdirAll(filepath.Dir(res.ServicePath), 0o755); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Dir(m.darwinLogPath()), 0o755); err != nil {
		return res, err
	}
	if _, err := writeIfChanged(res.ServicePath, desired, 0o644); err != nil {
		return res, err
	}
	// enable clears a persistent disabled override so the agent loads
	// at the next login, and doubles as the GUI-domain probe: a
	// headless session (SSH, CI) has no gui/<uid> domain and must not
	// keep a plist it can never verify.
	if _, err := m.Run(ctx, "launchctl", "enable", m.guiService()); err != nil {
		if fresh {
			_ = os.Remove(res.ServicePath)
			res.Action = EnsureUnavailable
			res.Note = "no launchd GUI domain (headless session?): " + err.Error()
			return res, nil
		}
		// The repaired content is strictly better than the stale one;
		// keep it and note the enable failure.
		res.Note = "launchctl enable failed: " + err.Error()
	}
	if fresh {
		res.Action = EnsureRegistered
	} else {
		res.Action = EnsureRepaired
	}
	return res, nil
}

// ensureLinux is the systemd half of the matrix.
func (m *Manager) ensureLinux(ctx context.Context) (EnsureResult, error) {
	res := EnsureResult{ServicePath: m.unitPath(), Exe: m.Exe}
	if owner, hint, owned := m.linuxForeignOwner(); owned {
		res.Action = EnsureYielded
		res.Owner = owner
		res.Hint = hint
		res.OursToo = fileExists(res.ServicePath)
		return res, nil
	}
	if m.optedOut() {
		res.Action = EnsureOptedOut
		res.Note = m.OptOutFile
		return res, nil
	}
	desired := []byte(SystemdUnit(m.Exe))
	cur, readErr := os.ReadFile(res.ServicePath)
	fresh := readErr != nil
	if readErr == nil && string(cur) == string(desired) {
		res.Action = EnsureCurrent
		return res, nil
	}
	if !fresh {
		res.PreviousExe = unitExecStart(cur)
	}
	if err := os.MkdirAll(m.unitDir(), 0o755); err != nil {
		return res, err
	}
	if _, err := writeIfChanged(res.ServicePath, desired, 0o644); err != nil {
		return res, err
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return m.linuxManagerUnavailable(res, fresh, "systemd user manager unavailable: "+err.Error()), nil
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
		return m.linuxManagerUnavailable(res, fresh, "systemctl --user enable failed: "+err.Error()), nil
	}
	// Deliberately NO start/restart: the running app may BE the
	// service instance; the unit takes over from the next login.
	if fresh {
		res.Action = EnsureRegistered
	} else {
		res.Action = EnsureRepaired
	}
	return res, nil
}

// linuxManagerUnavailable resolves a failed daemon-reload/enable: a
// fresh write is rolled back (the matrix must re-run cleanly on a
// later boot with a live bus), a repair keeps the better content and
// carries the failure as a note.
func (m *Manager) linuxManagerUnavailable(res EnsureResult, fresh bool, note string) EnsureResult {
	if fresh {
		_ = os.Remove(res.ServicePath)
		res.Action = EnsureUnavailable
		res.Note = note
		return res
	}
	res.Action = EnsureRepaired
	res.Note = note
	return res
}

// brewUnitPath is where a `brew services`-managed systemd user unit
// would live (brew's linux naming is homebrew.<formula>.service, the
// homebrew.mxcl label's systemd sibling).
func (m *Manager) brewUnitPath() string {
	return filepath.Join(m.unitDir(), "homebrew."+appName+".service")
}

// linuxForeignOwner reports a foreign login-startup owner on linux:
// the deb-shipped system unit (a user unit here would SHADOW it --
// systemd's ~/.config/systemd/user precedence -- silently replacing
// the packaged definition), or a brew services user unit. Taking over
// a brew-services-managed unit uninvited would be rude, so we yield
// with a stop-once pointer instead: after one `brew services stop`,
// self-registration owns startup and survives upgrades on its own.
func (m *Manager) linuxForeignOwner() (owner, hint string, owned bool) {
	for _, d := range m.SystemUnitDirs {
		p := filepath.Join(d, UnitName)
		if fileExists(p) {
			return "the deb-installed unit " + p, "", true
		}
	}
	if p := m.brewUnitPath(); fileExists(p) {
		return "brew services (unit " + p + ")",
			"run 'brew services stop pazer/build/" + appName + "' once if you want the app to manage its own login service",
			true
	}
	return "", "", false
}

// optedOut reports the auto-registration opt-out marker's presence.
func (m *Manager) optedOut() bool {
	return m.OptOutFile != "" && fileExists(m.OptOutFile)
}

// unitExecStart extracts the ExecStart= value from an existing unit
// file (best effort; the repair log's "old" side).
func unitExecStart(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// plistProgram extracts the first ProgramArguments string from an
// existing LaunchAgent plist (best effort; the repair log's "old"
// side). Parses only the shape LaunchAgentPlist emits.
func plistProgram(data []byte) string {
	s := string(data)
	i := strings.Index(s, "<key>ProgramArguments</key>")
	if i < 0 {
		return ""
	}
	s = s[i:]
	j := strings.Index(s, "<string>")
	if j < 0 {
		return ""
	}
	s = s[j+len("<string>"):]
	k := strings.Index(s, "</string>")
	if k < 0 {
		return ""
	}
	return xmlUnescape(strings.TrimSpace(s[:k]))
}
