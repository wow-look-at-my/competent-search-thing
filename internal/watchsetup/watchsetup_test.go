package watchsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recorder captures what a Manager's fake seams saw.
type recorder struct {
	state         State
	resolveExe    string
	resolveID     string
	resolveOK     bool
	scriptExe     string
	scriptErr     error
	cleanups      int
	escalateCalls int
	escalatePath  string
	escalateErr   error
	reExecCalls   int
	reExecExe     string
	reExecArgv    []string
	reExecEnv     []string
	reExecErr     error
	env           map[string]string
	logs          []string
}

// testManager builds a Manager whose every seam is a fake, in the common
// "Linux, graphical, fanotify needs caps" starting state.
func testManager(t *testing.T) (*Manager, *recorder) {
	t.Helper()
	r := &recorder{
		state:      StateNeedsCaps,
		resolveExe: "/opt/app/competent-search-thing",
		resolveID:  "/opt/app/competent-search-thing|123|456",
		resolveOK:  true,
		env:        map[string]string{"DISPLAY": ":0"},
	}
	m := &Manager{
		goos:      "linux",
		backend:   "auto",
		enabled:   true,
		configDir: t.TempDir(),
		args:      []string{"/opt/app/competent-search-thing"},
		environ:   []string{"HOME=/home/u", "PATH=/usr/bin"},
		probe:     func() State { return r.state },
		resolve:   func() (string, string, bool) { return r.resolveExe, r.resolveID, r.resolveOK },
		writeScript: func(exe string) (string, func(), error) {
			r.scriptExe = exe
			if r.scriptErr != nil {
				return "", func() {}, r.scriptErr
			}
			return "/tmp/grant.sh", func() { r.cleanups++ }, nil
		},
		escalate: func(_ context.Context, p string) error {
			r.escalateCalls++
			r.escalatePath = p
			return r.escalateErr
		},
		reExec: func(exe string, argv, env []string) error {
			r.reExecCalls++
			r.reExecExe = exe
			r.reExecArgv = argv
			r.reExecEnv = env
			return r.reExecErr
		},
		getenv: func(k string) string { return r.env[k] },
		logf:   func(f string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(f, a...)) },
		now:    func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	return m, r
}

func (r *recorder) logText() string { return strings.Join(r.logs, "\n") }

func markerExists(t *testing.T, m *Manager) bool {
	t.Helper()
	_, err := os.Stat(m.markerPath())
	return err == nil
}

// --- gate() decisions ---------------------------------------------

func TestEnsureSkipsNonLinux(t *testing.T) {
	m, r := testManager(t)
	m.goos = "darwin"
	res := m.Ensure()
	require.Equal(t, ActionSkipped, res.Action)
	require.Equal(t, "not-linux", res.Reason)
	require.Zero(t, r.escalateCalls, "nothing should run off Linux")
	require.Zero(t, r.reExecCalls, "nothing should run off Linux")
}

func TestEnsureSkipsEnvDisabled(t *testing.T) {
	m, r := testManager(t)
	r.env[EnvDisable] = "1"
	require.Equal(t, "env-disabled", m.Ensure().Reason)
	require.Zero(t, r.escalateCalls, "env disable must short-circuit")
}

func TestEnsureSkipsConfigDisabled(t *testing.T) {
	m, _ := testManager(t)
	m.enabled = false
	require.Equal(t, "config-disabled", m.Ensure().Reason)
}

func TestEnsureSkipsExplicitBackends(t *testing.T) {
	for _, b := range []string{"inotify", "fsevents"} {
		m, r := testManager(t)
		m.backend = b
		res := m.Ensure()
		require.Equal(t, ActionSkipped, res.Action, b)
		require.Equal(t, "backend:"+b, res.Reason)
		require.Zero(t, r.escalateCalls, "backend %s should not escalate", b)
	}
}

func TestEnsureProceedsForFanotifyBackend(t *testing.T) {
	// "fanotify" is strict and wants fanotify: escalation is even more
	// valuable there (a missing cap disables live watching outright).
	m, r := testManager(t)
	m.backend = "fanotify"
	require.Equal(t, ActionGranted, m.Ensure().Action)
	require.Equal(t, 1, r.escalateCalls, "fanotify backend must attempt the grant")
}

func TestEnsureSkipsHeadless(t *testing.T) {
	m, r := testManager(t)
	r.env = map[string]string{} // no DISPLAY, no WAYLAND_DISPLAY
	require.Equal(t, "headless", m.Ensure().Reason)
	require.Zero(t, r.escalateCalls, "no prompt without a graphical session")
}

func TestEnsureWaylandCountsAsGraphical(t *testing.T) {
	m, r := testManager(t)
	r.env = map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	require.Equal(t, ActionGranted, m.Ensure().Action)
	require.Equal(t, 1, r.escalateCalls, "Wayland session should allow the prompt")
}

func TestEnsureOptimalWhenReady(t *testing.T) {
	m, r := testManager(t)
	r.state = StateReady
	// A stale marker from a past decline must be tidied when caps appear.
	m.writeMarker(r.resolveID, "declined earlier")
	require.True(t, markerExists(t, m), "precondition: marker written")
	res := m.Ensure()
	require.Equal(t, ActionOptimal, res.Action)
	require.Equal(t, "ready", res.Reason)
	require.Zero(t, r.escalateCalls, "no work when already optimal")
	require.Zero(t, r.reExecCalls, "no work when already optimal")
	require.False(t, markerExists(t, m), "a stale marker should be cleared once caps are present")
}

func TestEnsureSkipsUnsupported(t *testing.T) {
	m, r := testManager(t)
	r.state = StateUnsupported
	res := m.Ensure()
	require.Equal(t, ActionSkipped, res.Action)
	require.Equal(t, "unsupported", res.Reason)
	require.Zero(t, r.escalateCalls, "setcap cannot help an unsupported kernel")
	require.Contains(t, r.logText(), "not available in this environment")
}

// --- Ensure() grant path ------------------------------------------

func TestEnsureGrantsAndReExecs(t *testing.T) {
	m, r := testManager(t)
	require.Equal(t, ActionGranted, m.Ensure().Action)
	require.Equal(t, 1, r.escalateCalls)
	require.Equal(t, "/tmp/grant.sh", r.escalatePath)
	require.Equal(t, 1, r.cleanups, "the script temp dir must be cleaned up")
	require.Equal(t, 1, r.reExecCalls)
	require.Equal(t, r.resolveExe, r.reExecExe, "re-exec should target the resolved binary")
	require.Equal(t, []string{"/opt/app/competent-search-thing"}, r.reExecArgv)
	require.Contains(t, r.reExecEnv, envAttempted+"=1", "re-exec env must carry the loop guard")
	require.Contains(t, r.reExecEnv, "HOME=/home/u", "re-exec env must preserve the inherited environment")
	require.False(t, markerExists(t, m), "a successful grant must leave no decline marker")
}

func TestEnsureDeclinedWritesMarkerNoReExec(t *testing.T) {
	m, r := testManager(t)
	r.escalateErr = errors.New("the request was dismissed or not authorized")
	res := m.Ensure()
	require.Equal(t, ActionAttemptFailed, res.Action)
	require.Equal(t, "escalation", res.Reason)
	require.Zero(t, r.reExecCalls, "a declined grant must not re-exec")
	require.Equal(t, 1, r.cleanups, "the script temp dir must still be cleaned up")
	require.True(t, markerExists(t, m), "a decline must be remembered")
	reason, cur := m.markerCurrent(r.resolveID)
	require.True(t, cur)
	require.Contains(t, reason, "dismissed")
	require.Contains(t, r.logText(), "setup-watch", "the decline log should point at the retry command")
}

func TestEnsureRespectsMarker(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker(r.resolveID, "declined before")
	res := m.Ensure()
	require.Equal(t, ActionSkipped, res.Action)
	require.Equal(t, "marker", res.Reason)
	require.Zero(t, r.escalateCalls, "a current marker must suppress the prompt")
}

func TestEnsureMarkerStaleAfterUpgrade(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker("/opt/app/competent-search-thing|OLD|OLD", "declined on the old build")
	// r.resolveID is the NEW identity: the marker is stale, so the
	// prompt runs again.
	require.Equal(t, ActionGranted, m.Ensure().Action, "an upgraded binary should re-offer")
	require.Equal(t, 1, r.escalateCalls, "stale marker must not block a fresh attempt")
}

func TestEnsureAttemptedButIneffective(t *testing.T) {
	m, r := testManager(t)
	r.env[envAttempted] = "1" // re-exec'd child, caps still missing
	res := m.Ensure()
	require.Equal(t, ActionAttemptFailed, res.Action)
	require.Equal(t, "ineffective", res.Reason)
	require.Zero(t, r.escalateCalls, "no second escalation (that is the loop guard)")
	require.Zero(t, r.reExecCalls, "no second re-exec (that is the loop guard)")
	require.True(t, markerExists(t, m), "an ineffective grant must be remembered")
	require.Contains(t, r.logText(), "cannot store file capabilities")
}

func TestEnsureUnresolvedExe(t *testing.T) {
	m, r := testManager(t)
	r.resolveOK = false
	res := m.Ensure()
	require.Equal(t, ActionSkipped, res.Action)
	require.Equal(t, "unresolved-exe", res.Reason)
	require.Zero(t, r.escalateCalls, "cannot grant without a resolved target")
}

func TestEnsureReExecFailureReported(t *testing.T) {
	m, r := testManager(t)
	r.reExecErr = errors.New("exec: no such file")
	res := m.Ensure()
	require.Equal(t, ActionGranted, res.Action)
	require.Equal(t, "reexec-failed", res.Reason)
	require.Contains(t, r.logText(), "applies at the next launch")
}

func TestEnsureScriptWriteFailure(t *testing.T) {
	m, r := testManager(t)
	r.scriptErr = errors.New("read-only tmp")
	require.Equal(t, ActionAttemptFailed, m.Ensure().Action)
	require.Zero(t, r.escalateCalls, "a script that could not be written must not be escalated")
	require.True(t, markerExists(t, m), "a write failure is remembered")
}

// --- Attempt() (the forced CLI path) ------------------------------

func TestAttemptGrantsWithoutReExec(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker(r.resolveID, "declined before") // forced path ignores it
	var out strings.Builder
	require.Equal(t, ActionGranted, m.Attempt(context.Background(), &out).Action)
	require.Equal(t, 1, r.escalateCalls, "forced attempt must escalate regardless of the marker")
	require.Zero(t, r.reExecCalls, "the CLI path never re-execs")
	require.False(t, markerExists(t, m), "a successful attempt clears the marker")
	require.Contains(t, out.String(), "Restart competent-search-thing")
}

func TestAttemptReady(t *testing.T) {
	m, r := testManager(t)
	r.state = StateReady
	var out strings.Builder
	require.Equal(t, ActionOptimal, m.Attempt(context.Background(), &out).Action)
	require.Contains(t, out.String(), "already enabled")
}

func TestAttemptUnsupported(t *testing.T) {
	m, r := testManager(t)
	r.state = StateUnsupported
	var out strings.Builder
	require.Equal(t, "unsupported", m.Attempt(context.Background(), &out).Reason)
	require.Contains(t, out.String(), "not available")
}

func TestAttemptNonLinux(t *testing.T) {
	m, _ := testManager(t)
	m.goos = "windows"
	var out strings.Builder
	require.Equal(t, "not-linux", m.Attempt(context.Background(), &out).Reason)
}

func TestAttemptDeclinedWritesMarker(t *testing.T) {
	m, r := testManager(t)
	r.escalateErr = errors.New("authentication failed")
	var out strings.Builder
	require.Equal(t, ActionAttemptFailed, m.Attempt(context.Background(), &out).Action)
	require.True(t, markerExists(t, m), "a forced-attempt failure is also remembered")
	require.Contains(t, out.String(), "did not complete")
}

func TestAttemptUnresolvedExe(t *testing.T) {
	m, r := testManager(t)
	r.resolveOK = false
	var out strings.Builder
	require.Equal(t, "unresolved-exe", m.Attempt(context.Background(), &out).Reason)
}

// --- marker round trips -------------------------------------------

func TestMarkerRoundTrip(t *testing.T) {
	m, _ := testManager(t)
	_, cur := m.markerCurrent("id-a")
	require.False(t, cur, "no marker yet")
	m.writeMarker("id-a", "reason-a")
	reason, cur := m.markerCurrent("id-a")
	require.True(t, cur)
	require.Equal(t, "reason-a", reason)
	_, cur = m.markerCurrent("id-b")
	require.False(t, cur, "a different identity must not match")
	m.clearMarker()
	_, cur = m.markerCurrent("id-a")
	require.False(t, cur, "cleared marker should not match")
}

func TestMarkerNoConfigDir(t *testing.T) {
	m, _ := testManager(t)
	m.configDir = ""
	require.Empty(t, m.markerPath(), "no config dir means no marker path")
	m.writeMarker("id", "reason") // must not panic
	_, cur := m.markerCurrent("id")
	require.False(t, cur, "no persistence without a config dir")
	m.clearMarker() // must not panic
}

func TestMarkerCorruptFile(t *testing.T) {
	m, _ := testManager(t)
	require.NoError(t, os.WriteFile(m.markerPath(), []byte("{not json"), 0o600))
	_, cur := m.markerCurrent("id")
	require.False(t, cur, "a corrupt marker is treated as absent")
}

// --- grant script + helpers ---------------------------------------

func TestGrantScriptContent(t *testing.T) {
	s := grantScript("/opt/app/competent-search-thing")
	for _, want := range []string{
		"cap_sys_admin,cap_dac_read_search+ep",
		"'/opt/app/competent-search-thing'",
		"setcap not found",
		"verification failed",
	} {
		require.Contains(t, s, want)
	}
	require.True(t, isASCII(s), "the grant script must be ASCII")
}

func TestGrantScriptEscapesExe(t *testing.T) {
	s := grantScript("/tmp/a'b/app")
	require.Contains(t, s, `'/tmp/a'\''b/app'`, "single quote not escaped")
}

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"plain":  "'plain'",
		"a'b":    `'a'\''b'`,
		"":       "''",
		"/x/y z": "'/x/y z'",
	}
	for in, want := range cases {
		require.Equal(t, want, shellSingleQuote(in), in)
	}
}

func TestLastLine(t *testing.T) {
	require.Equal(t, "b", lastLine("a\nb\n\n"))
	require.Empty(t, lastLine(""))
	require.Len(t, lastLine(strings.Repeat("x", 500)), 200)
}

func TestEscalateError(t *testing.T) {
	// 126 = dismissed / not authorized.
	require.Contains(t, escalateError(fakeExit(t, 126), "irrelevant").Error(), "dismissed")
	// 127 = auth failure; the stderr tail is surfaced.
	require.Contains(t, escalateError(fakeExit(t, 127), "polkit says no").Error(), "polkit says no")
	// Script's own non-zero exit.
	err := escalateError(fakeExit(t, 3), "setcap not found")
	require.Contains(t, err.Error(), "exit 3")
	require.Contains(t, err.Error(), "setcap not found")
	// A non-exec error (e.g. context deadline) passes through.
	base := errors.New("context deadline exceeded")
	require.Equal(t, base.Error(), escalateError(base, "").Error())
}

// --- production seams (safe to exercise on the runner) ------------

func TestNewFillsProductionSeams(t *testing.T) {
	m := New(Config{Backend: "auto", Enabled: true, ConfigDir: t.TempDir()})
	require.NotEmpty(t, m.goos)
	require.NotNil(t, m.probe)
	require.NotNil(t, m.resolve)
	require.NotNil(t, m.escalate)
	require.NotNil(t, m.reExec)
	require.NotNil(t, m.writeScript)
	require.NotNil(t, m.getenv)
	require.NotNil(t, m.logf)
	require.NotNil(t, m.now)
	require.NotEmpty(t, m.args, "New must capture argv for re-exec")
}

func TestProbeReturnsAValidState(t *testing.T) {
	// The real probe: on an unprivileged runner it returns
	// StateNeedsCaps, but assert only that it is a valid state so the
	// test is deterministic across environments.
	require.Contains(t, []State{StateUnsupported, StateNeedsCaps, StateReady}, probeFanotifySupport())
}

func TestProdResolveResolvesTestBinary(t *testing.T) {
	exe, id, ok := prodResolve()
	if !ok {
		t.Skip("os.Executable unavailable in this environment")
	}
	require.True(t, filepath.IsAbs(exe), "resolved path not absolute")
	require.True(t, strings.HasPrefix(id, exe+"|"), "identity should start with the path")
}

func TestProdWriteScript(t *testing.T) {
	p, cleanup, err := prodWriteScript("/opt/app/bin")
	require.NoError(t, err)
	defer cleanup()
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(data), "cap_sys_admin,cap_dac_read_search+ep")
	cleanup()
	_, err = os.Stat(p)
	require.True(t, os.IsNotExist(err), "cleanup must remove the script")
}

// --- helpers ------------------------------------------------------

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// fakeExit produces a real *exec.ExitError with the given code by
// running /bin/sh -c "exit N" (POSIX; the tests run on Linux/macOS CI).
func fakeExit(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "expected an ExitError for code %d", code)
	return err
}
