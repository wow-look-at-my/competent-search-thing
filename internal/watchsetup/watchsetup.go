// Package watchsetup puts the app into its optimal filesystem-monitoring
// state before the GUI starts.
//
// On Linux the whole-filesystem fanotify backend gives full live
// coverage at negligible memory cost -- one kernel mark per filesystem,
// no per-directory watches, no max_user_watches ceiling -- but it needs
// file capabilities (CAP_SYS_ADMIN + CAP_DAC_READ_SEARCH) that the raw
// binary does not carry. Without them internal/watch silently falls
// back to a bounded per-directory inotify hot set plus periodic
// reconcile sweeps: only partial live coverage, and depending on how the
// user tuned fs.inotify.max_user_watches either wasted watch memory or
// churny sweep re-walks.
//
// Rather than log a "run setcap yourself" hint and run degraded, Ensure
// DOES it. It probes whether fanotify is merely blocked on capabilities
// (EPERM -- grantable) versus genuinely unsupported here (an old kernel
// or a container, where setcap cannot help). When it is grantable it
// prompts for privilege escalation through pkexec, runs a small script
// that grants the capabilities with setcap, and re-execs into the
// now-capable binary so the app comes up on fanotify. The capabilities
// stick to the binary, so this happens at most once per install; a
// decline or a failure is remembered in a marker keyed to the binary's
// identity so the prompt never nags on every launch, and a binary
// upgrade re-offers.
//
// Everything OS-touching sits behind seam fields (New fills the
// production values; tests inject fakes), so the decision matrix, the
// grant script content, and the marker bookkeeping are all unit-tested
// headlessly -- without privileges, pkexec, or a real re-exec.
package watchsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

const (
	// EnvDisable turns the automatic setup off for this process when set
	// to any non-empty value (CI runners, headless scripting) -- the
	// COMPETENT_SEARCH_NO_SERVICE precedent. The persistent per-user
	// opt-out is config watcher.setupEnabled=false.
	EnvDisable = "COMPETENT_SEARCH_NO_WATCH_SETUP"
	// envAttempted is set on the re-exec'd child so a capability grant
	// that did not take effect (e.g. the binary lives on a filesystem
	// that cannot store file capabilities, like overlayfs) degrades to
	// one honest log line instead of an infinite re-exec loop.
	envAttempted = "COMPETENT_SEARCH_WATCH_SETUP_ATTEMPTED"
)

// markerName is the decline/failure marker's file name under the config
// directory. It is NOT config.json's hot-apply path, so writing it can
// never trigger a config reload.
const markerName = "watch-setup-state.json"

// escalateTimeout bounds the whole pkexec exchange: the polkit dialog
// waits for the user, but a wedged agent must not hang startup forever.
const escalateTimeout = 3 * time.Minute

// State is what the fanotify support probe found.
type State int

const (
	// StateUnsupported: not Linux, or the kernel/container does not
	// support the fanotify mode the watcher needs (FAN_REPORT_DFID_NAME,
	// kernel >= 5.9). Capabilities cannot help.
	StateUnsupported State = iota
	// StateNeedsCaps: fanotify_init returned EPERM -- the kernel
	// supports it, the binary just lacks CAP_SYS_ADMIN. setcap fixes it.
	StateNeedsCaps
	// StateReady: fanotify_init succeeded -- the capabilities are
	// already present and the watcher will pick fanotify on its own.
	StateReady
)

// Action classifies what Ensure/Attempt decided (for tests and callers;
// the log lines are written internally).
type Action int

const (
	// ActionSkipped: nothing was attempted (wrong OS, disabled,
	// unsupported, already declined, or no capabilities missing after
	// all).
	ActionSkipped Action = iota
	// ActionOptimal: already optimal -- capabilities present, fanotify
	// will be used.
	ActionOptimal
	// ActionGranted: the capabilities were granted (Ensure then
	// re-execs; production never returns on that path unless the
	// re-exec itself failed).
	ActionGranted
	// ActionAttemptFailed: an attempt ran but did not complete (user
	// declined, no pkexec, setcap failed, or the grant did not take
	// effect).
	ActionAttemptFailed
)

// Result reports the decision plus a short machine reason (tests assert
// on it; humans read the log lines).
type Result struct {
	Action Action
	Reason string
}

// Config carries the app-supplied inputs New needs.
type Config struct {
	// Backend is config watcher.backend (already normalized): "auto"
	// and "fanotify" want fanotify and are eligible; "inotify" (the
	// user pinned per-directory watches) and "fsevents" (a wrong-OS
	// strict choice setcap cannot satisfy) are not.
	Backend string
	// Enabled is config watcher.setupEnabled: false is the persistent
	// per-user opt-out.
	Enabled bool
	// ConfigDir is where the decline marker lives (config.Dir()); ""
	// disables the marker (the setup still runs, it just cannot
	// remember a decline).
	ConfigDir string
}

// Manager runs the setup. New fills the production seams; tests
// construct it directly with fakes.
type Manager struct {
	goos      string
	backend   string
	enabled   bool
	configDir string
	args      []string // re-exec argv (os.Args)
	environ   []string // re-exec environment (os.Environ())

	// Seams (production defaults in New).
	probe       func() State
	resolve     func() (exe string, identity string, ok bool)
	writeScript func(exe string) (path string, cleanup func(), err error)
	escalate    func(ctx context.Context, scriptPath string) error
	reExec      func(exe string, argv, env []string) error
	getenv      func(string) string
	logf        func(string, ...any)
	now         func() time.Time
}

// New builds the production Manager.
func New(cfg Config) *Manager {
	return &Manager{
		goos:        runtime.GOOS,
		backend:     cfg.Backend,
		enabled:     cfg.Enabled,
		configDir:   cfg.ConfigDir,
		args:        os.Args,
		environ:     os.Environ(),
		probe:       probeFanotifySupport,
		resolve:     prodResolve,
		writeScript: prodWriteScript,
		escalate:    prodEscalate,
		reExec:      prodReExec,
		getenv:      os.Getenv,
		logf:        log.Printf,
		now:         time.Now,
	}
}

// Ensure is the automatic pre-GUI path (main.go calls it before
// wails.Run). It walks the decision matrix and, when fanotify is
// grantable, prompts for privileges, grants the capabilities, and
// re-execs into the capable binary (production never returns from the
// grant path). Every expected outcome is an Action, not an error; the
// log lines are written internally.
func (m *Manager) Ensure() Result {
	if r, done := m.gate(); done {
		return r
	}
	// StateNeedsCaps: fanotify is supported but blocked on capabilities.
	exe, identity, ok := m.resolve()
	if !ok {
		m.logf("watch: cannot resolve the running binary to grant fanotify capabilities; using per-directory watching")
		return Result{ActionSkipped, "unresolved-exe"}
	}
	if m.getenv(envAttempted) != "" {
		// The grant ran on the previous launch, yet the capabilities are
		// still missing: this file cannot hold them here.
		m.writeMarker(identity, "capabilities did not take effect after setcap (the binary may be on a filesystem that cannot store file capabilities); grant ambient capabilities instead")
		m.logf("watch: full-filesystem watching is still unavailable after the capability grant -- %s is on a filesystem that cannot store file capabilities; using per-directory watching plus sweeps (see README: Enable full-filesystem watching)", exe)
		return Result{ActionAttemptFailed, "ineffective"}
	}
	if _, current := m.markerCurrent(identity); current {
		// Declined or failed before for this exact binary: do not nag.
		// The decline itself logged how to retry; the frontend keeps a
		// notice chip up.
		return Result{ActionSkipped, "marker"}
	}
	return m.grantAndRestart(exe, identity)
}

// gate resolves every reason NOT to attempt the grant. It returns
// (result, true) when the decision is made, or (zero, false) when the
// caller should proceed (StateNeedsCaps).
func (m *Manager) gate() (Result, bool) {
	if m.goos != "linux" {
		return Result{ActionSkipped, "not-linux"}, true
	}
	if m.getenv(EnvDisable) != "" {
		return Result{ActionSkipped, "env-disabled"}, true
	}
	if !m.enabled {
		return Result{ActionSkipped, "config-disabled"}, true
	}
	switch m.backend {
	case "inotify", "fsevents":
		// inotify: the user explicitly pinned per-directory watches.
		// fsevents on Linux: a wrong-OS strict choice setcap cannot
		// satisfy. Either way, respect it.
		return Result{ActionSkipped, "backend:" + m.backend}, true
	}
	if m.getenv("DISPLAY") == "" && m.getenv("WAYLAND_DISPLAY") == "" {
		// No graphical session -- there is nowhere to show a polkit
		// password prompt. A later launch inside a desktop session
		// re-checks.
		return Result{ActionSkipped, "headless"}, true
	}
	switch m.probe() {
	case StateReady:
		// Capabilities already present: the watcher uses fanotify on its
		// own. Tidy any stale marker from a past decline.
		m.clearMarker()
		return Result{ActionOptimal, "ready"}, true
	case StateUnsupported:
		m.logf("watch: full-filesystem watching (fanotify) is not available in this environment (kernel or container limitation) -- capabilities cannot help; using per-directory watching plus sweeps")
		return Result{ActionSkipped, "unsupported"}, true
	default:
		return Result{}, false // StateNeedsCaps: proceed.
	}
}

// grantAndRestart runs the escalation and, on success, re-execs into the
// now-capable binary.
func (m *Manager) grantAndRestart(exe, identity string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), escalateTimeout)
	defer cancel()
	m.logf("watch: requesting administrator privileges to enable full-filesystem watching (fanotify); you may be prompted for your password")
	if err := m.runGrant(ctx, exe); err != nil {
		m.writeMarker(identity, err.Error())
		m.logf("watch: automatic full-filesystem watch setup did not complete: %v. It will not ask again automatically -- run 'competent-search-thing setup-watch' to retry (or set watcher.setupEnabled=false to silence this).", err)
		return Result{ActionAttemptFailed, "escalation"}
	}
	m.clearMarker()
	m.logf("watch: fanotify capabilities granted; restarting to enable full-filesystem watching")
	env := append(append([]string(nil), m.environ...), envAttempted+"=1")
	if err := m.reExec(exe, m.args, env); err != nil {
		m.logf("watch: could not restart after the capability grant (%v); full-filesystem watching applies at the next launch", err)
		return Result{ActionGranted, "reexec-failed"}
	}
	return Result{ActionGranted, "reexec"} // unreachable in production
}

// Attempt is the explicit, forced setup the `setup-watch` command uses.
// It ignores the decline marker and the config switch, respects only the
// technical gates (Linux, a supported kernel, capabilities actually
// missing), prints progress to out, clears the marker on success, and
// NEVER re-execs -- the capabilities stick to the binary and take effect
// at the next launch.
func (m *Manager) Attempt(ctx context.Context, out io.Writer) Result {
	if m.goos != "linux" {
		fmt.Fprintln(out, "Full-filesystem watching (fanotify) is a Linux feature; nothing to do on this OS.")
		return Result{ActionSkipped, "not-linux"}
	}
	switch m.probe() {
	case StateReady:
		m.clearMarker()
		fmt.Fprintln(out, "Full-filesystem watching is already enabled (the capabilities are present).")
		return Result{ActionOptimal, "ready"}
	case StateUnsupported:
		fmt.Fprintln(out, "Full-filesystem watching (fanotify) is not available in this environment (kernel or container limitation); capabilities cannot help. The index uses per-directory watching plus sweeps.")
		return Result{ActionSkipped, "unsupported"}
	}
	exe, identity, ok := m.resolve()
	if !ok {
		fmt.Fprintln(out, "Could not resolve the running binary to grant capabilities.")
		return Result{ActionAttemptFailed, "unresolved-exe"}
	}
	fmt.Fprintf(out, "Requesting administrator privileges to grant fanotify capabilities to %s ...\n", exe)
	ctx, cancel := context.WithTimeout(ctx, escalateTimeout)
	defer cancel()
	if err := m.runGrant(ctx, exe); err != nil {
		m.writeMarker(identity, err.Error())
		fmt.Fprintf(out, "Setup did not complete: %v\n", err)
		return Result{ActionAttemptFailed, "escalation"}
	}
	m.clearMarker()
	fmt.Fprintf(out, "Done -- full-filesystem watching is enabled for %s.\nRestart competent-search-thing to use it.\n", exe)
	return Result{ActionGranted, "granted"}
}

// runGrant writes the grant script and runs it under escalation.
func (m *Manager) runGrant(ctx context.Context, exe string) error {
	scriptPath, cleanup, err := m.writeScript(exe)
	if err != nil {
		return fmt.Errorf("could not write the grant script: %w", err)
	}
	defer cleanup()
	return m.escalate(ctx, scriptPath)
}

// --- marker bookkeeping -------------------------------------------

// markerState is the decline/failure marker's JSON shape.
type markerState struct {
	Version  int    `json:"v"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

func (m *Manager) markerPath() string {
	if m.configDir == "" {
		return ""
	}
	return filepath.Join(m.configDir, markerName)
}

// markerCurrent reports whether a marker exists for exactly this binary
// identity (path + mtime + size). A binary upgrade changes the identity,
// so a stale marker never blocks a fresh attempt.
func (m *Manager) markerCurrent(identity string) (reason string, current bool) {
	p := m.markerPath()
	if p == "" {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var st markerState
	if json.Unmarshal(data, &st) != nil || st.Identity != identity {
		return "", false
	}
	return st.Reason, true
}

// writeMarker records a decline/failure so the next launch does not nag.
// Best effort: a failure to persist just means the prompt may reappear.
func (m *Manager) writeMarker(identity, reason string) {
	p := m.markerPath()
	if p == "" {
		return
	}
	data, err := json.MarshalIndent(markerState{
		Version:  1,
		Identity: identity,
		Reason:   reason,
		At:       m.now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(m.configDir, 0o755)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(data, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func (m *Manager) clearMarker() {
	if p := m.markerPath(); p != "" {
		_ = os.Remove(p)
	}
}

// --- grant script -------------------------------------------------

// grantScript is the shell script pkexec runs as root: locate setcap,
// grant the two capabilities on the resolved binary, then verify with
// getcap. exe is the app's own resolved path (no user input), and it is
// single-quoted regardless. Exit codes: 3 = no setcap, 4 = verification
// failed.
func grantScript(exe string) string {
	return `#!/bin/sh
# competent-search-thing: grant the fanotify capabilities that enable
# full-filesystem watching. Run as root via pkexec.
set -eu
bin=` + shellSingleQuote(exe) + `
setcap=
for c in /usr/sbin/setcap /sbin/setcap /usr/bin/setcap setcap; do
  if command -v "$c" >/dev/null 2>&1; then setcap=$c; break; fi
done
if [ -z "$setcap" ]; then
  echo 'setcap not found; install libcap2-bin (Debian/Ubuntu) or libcap (Fedora/Arch)' >&2
  exit 3
fi
"$setcap" cap_sys_admin,cap_dac_read_search+ep "$bin"
getcap=
for c in /usr/sbin/getcap /sbin/getcap /usr/bin/getcap getcap; do
  if command -v "$c" >/dev/null 2>&1; then getcap=$c; break; fi
done
if [ -n "$getcap" ]; then
  "$getcap" "$bin" | grep -qi cap_sys_admin || { echo 'capability verification failed after setcap' >&2; exit 4; }
fi
echo "granted cap_sys_admin,cap_dac_read_search to $bin"
`
}

// shellSingleQuote wraps s in single quotes, escaping any embedded
// single quote the POSIX way ('\”).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- production seams ---------------------------------------------

// prodResolve resolves the running binary to the real regular file
// setcap must target (ResolvedExecutable: setcap refuses symlinks), plus
// an identity string (path|mtime|size) that changes across upgrades.
func prodResolve() (string, string, bool) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "", "", false
	}
	resolved, ok := platform.ResolvedExecutable(exe)
	if !ok {
		return "", "", false
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", "", false
	}
	return resolved, fmt.Sprintf("%s|%d|%d", resolved, fi.ModTime().UnixNano(), fi.Size()), true
}

// prodWriteScript writes the grant script to a fresh 0700 temp dir.
func prodWriteScript(exe string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "cst-watchsetup-*")
	if err != nil {
		return "", func() {}, err
	}
	p := filepath.Join(dir, "grant.sh")
	if err := os.WriteFile(p, []byte(grantScript(exe)), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, err
	}
	return p, func() { _ = os.RemoveAll(dir) }, nil
}

// prodEscalate runs the grant script as root through pkexec's polkit
// dialog. A missing pkexec, a dismissed dialog, a failed auth, and a
// failed script all come back as readable errors.
func prodEscalate(ctx context.Context, scriptPath string) error {
	pk, err := exec.LookPath("pkexec")
	if err != nil {
		return errors.New("no graphical privilege-escalation tool found (install pkexec / polkit)")
	}
	cmd := exec.CommandContext(ctx, pk, "/bin/sh", scriptPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return escalateError(err, stderr.String())
	}
	return nil
}

// escalateError turns pkexec's exit into a readable message. pkexec uses
// 126 for "dismissed / not authorized" and 127 for "authentication
// failed / could not run"; any other non-zero code is the grant
// script's own failure.
func escalateError(runErr error, stderr string) error {
	msg := lastLine(stderr)
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		switch ee.ExitCode() {
		case 126:
			return errors.New("the request was dismissed or not authorized")
		case 127:
			if msg != "" {
				return fmt.Errorf("authentication failed: %s", msg)
			}
			return errors.New("authentication failed")
		default:
			if msg != "" {
				return fmt.Errorf("the grant script failed (exit %d): %s", ee.ExitCode(), msg)
			}
			return fmt.Errorf("the grant script failed (exit %d)", ee.ExitCode())
		}
	}
	if msg != "" {
		return fmt.Errorf("%v: %s", runErr, msg)
	}
	return runErr
}

// lastLine returns the last non-empty line of s, bounded to 200 bytes so
// a chatty tool cannot flood the log.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if len(t) > 200 {
				t = t[:200]
			}
			return t
		}
	}
	return ""
}
