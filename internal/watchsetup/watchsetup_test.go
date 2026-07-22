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
	if res.Action != ActionSkipped || res.Reason != "not-linux" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 || r.reExecCalls != 0 {
		t.Fatal("nothing should run off Linux")
	}
}

func TestEnsureSkipsEnvDisabled(t *testing.T) {
	m, r := testManager(t)
	r.env[EnvDisable] = "1"
	if res := m.Ensure(); res.Reason != "env-disabled" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("env disable must short-circuit")
	}
}

func TestEnsureSkipsConfigDisabled(t *testing.T) {
	m, _ := testManager(t)
	m.enabled = false
	if res := m.Ensure(); res.Reason != "config-disabled" {
		t.Fatalf("got %+v", res)
	}
}

func TestEnsureSkipsExplicitBackends(t *testing.T) {
	for _, b := range []string{"inotify", "fsevents"} {
		m, r := testManager(t)
		m.backend = b
		res := m.Ensure()
		if res.Action != ActionSkipped || res.Reason != "backend:"+b {
			t.Fatalf("backend %s: got %+v", b, res)
		}
		if r.escalateCalls != 0 {
			t.Fatalf("backend %s should not escalate", b)
		}
	}
}

func TestEnsureProceedsForFanotifyBackend(t *testing.T) {
	// "fanotify" is strict and wants fanotify: escalation is even more
	// valuable there (a missing cap disables live watching outright).
	m, r := testManager(t)
	m.backend = "fanotify"
	if res := m.Ensure(); res.Action != ActionGranted {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 1 {
		t.Fatal("fanotify backend must attempt the grant")
	}
}

func TestEnsureSkipsHeadless(t *testing.T) {
	m, r := testManager(t)
	r.env = map[string]string{} // no DISPLAY, no WAYLAND_DISPLAY
	if res := m.Ensure(); res.Reason != "headless" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("no prompt without a graphical session")
	}
}

func TestEnsureWaylandCountsAsGraphical(t *testing.T) {
	m, r := testManager(t)
	r.env = map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	if res := m.Ensure(); res.Action != ActionGranted {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 1 {
		t.Fatal("Wayland session should allow the prompt")
	}
}

func TestEnsureOptimalWhenReady(t *testing.T) {
	m, r := testManager(t)
	r.state = StateReady
	// A stale marker from a past decline must be tidied when caps appear.
	m.writeMarker(r.resolveID, "declined earlier")
	if !markerExists(t, m) {
		t.Fatal("precondition: marker written")
	}
	res := m.Ensure()
	if res.Action != ActionOptimal || res.Reason != "ready" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 || r.reExecCalls != 0 {
		t.Fatal("no work when already optimal")
	}
	if markerExists(t, m) {
		t.Fatal("a stale marker should be cleared once caps are present")
	}
}

func TestEnsureSkipsUnsupported(t *testing.T) {
	m, r := testManager(t)
	r.state = StateUnsupported
	res := m.Ensure()
	if res.Action != ActionSkipped || res.Reason != "unsupported" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("setcap cannot help an unsupported kernel")
	}
	if !strings.Contains(r.logText(), "not available in this environment") {
		t.Fatalf("expected an honest unsupported log line, got %q", r.logText())
	}
}

// --- Ensure() grant path ------------------------------------------

func TestEnsureGrantsAndReExecs(t *testing.T) {
	m, r := testManager(t)
	res := m.Ensure()
	if res.Action != ActionGranted {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 1 {
		t.Fatalf("escalate calls = %d", r.escalateCalls)
	}
	if r.escalatePath != "/tmp/grant.sh" {
		t.Fatalf("escalate path = %q", r.escalatePath)
	}
	if r.cleanups != 1 {
		t.Fatal("the script temp dir must be cleaned up")
	}
	if r.reExecCalls != 1 {
		t.Fatalf("reExec calls = %d", r.reExecCalls)
	}
	if r.reExecExe != r.resolveExe {
		t.Fatalf("re-exec should target the resolved binary, got %q", r.reExecExe)
	}
	if len(r.reExecArgv) != 1 || r.reExecArgv[0] != "/opt/app/competent-search-thing" {
		t.Fatalf("re-exec argv = %v", r.reExecArgv)
	}
	if !hasEnv(r.reExecEnv, envAttempted+"=1") {
		t.Fatalf("re-exec env must carry the loop guard, got %v", r.reExecEnv)
	}
	if !hasEnv(r.reExecEnv, "HOME=/home/u") {
		t.Fatal("re-exec env must preserve the inherited environment")
	}
	if markerExists(t, m) {
		t.Fatal("a successful grant must leave no decline marker")
	}
}

func TestEnsureDeclinedWritesMarkerNoReExec(t *testing.T) {
	m, r := testManager(t)
	r.escalateErr = errors.New("the request was dismissed or not authorized")
	res := m.Ensure()
	if res.Action != ActionAttemptFailed || res.Reason != "escalation" {
		t.Fatalf("got %+v", res)
	}
	if r.reExecCalls != 0 {
		t.Fatal("a declined grant must not re-exec")
	}
	if r.cleanups != 1 {
		t.Fatal("the script temp dir must still be cleaned up")
	}
	if !markerExists(t, m) {
		t.Fatal("a decline must be remembered")
	}
	reason, cur := m.markerCurrent(r.resolveID)
	if !cur || !strings.Contains(reason, "dismissed") {
		t.Fatalf("marker reason = %q cur=%v", reason, cur)
	}
	if !strings.Contains(r.logText(), "setup-watch") {
		t.Fatalf("the decline log should point at the retry command, got %q", r.logText())
	}
}

func TestEnsureRespectsMarker(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker(r.resolveID, "declined before")
	res := m.Ensure()
	if res.Action != ActionSkipped || res.Reason != "marker" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("a current marker must suppress the prompt")
	}
}

func TestEnsureMarkerStaleAfterUpgrade(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker("/opt/app/competent-search-thing|OLD|OLD", "declined on the old build")
	// r.resolveID is the NEW identity: the marker is stale, so the
	// prompt runs again.
	res := m.Ensure()
	if res.Action != ActionGranted {
		t.Fatalf("an upgraded binary should re-offer, got %+v", res)
	}
	if r.escalateCalls != 1 {
		t.Fatal("stale marker must not block a fresh attempt")
	}
}

func TestEnsureAttemptedButIneffective(t *testing.T) {
	m, r := testManager(t)
	r.env[envAttempted] = "1" // re-exec'd child, caps still missing
	res := m.Ensure()
	if res.Action != ActionAttemptFailed || res.Reason != "ineffective" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 || r.reExecCalls != 0 {
		t.Fatal("no second escalation/re-exec (that is the loop guard)")
	}
	if !markerExists(t, m) {
		t.Fatal("an ineffective grant must be remembered")
	}
	if !strings.Contains(r.logText(), "cannot store file capabilities") {
		t.Fatalf("expected an honest ineffective log, got %q", r.logText())
	}
}

func TestEnsureUnresolvedExe(t *testing.T) {
	m, r := testManager(t)
	r.resolveOK = false
	res := m.Ensure()
	if res.Action != ActionSkipped || res.Reason != "unresolved-exe" {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("cannot grant without a resolved target")
	}
}

func TestEnsureReExecFailureReported(t *testing.T) {
	m, r := testManager(t)
	r.reExecErr = errors.New("exec: no such file")
	res := m.Ensure()
	if res.Action != ActionGranted || res.Reason != "reexec-failed" {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(r.logText(), "applies at the next launch") {
		t.Fatalf("expected a graceful re-exec-failure log, got %q", r.logText())
	}
}

func TestEnsureScriptWriteFailure(t *testing.T) {
	m, r := testManager(t)
	r.scriptErr = errors.New("read-only tmp")
	res := m.Ensure()
	if res.Action != ActionAttemptFailed {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 0 {
		t.Fatal("a script that could not be written must not be escalated")
	}
	if !markerExists(t, m) {
		t.Fatal("a write failure is remembered")
	}
}

// --- Attempt() (the forced CLI path) ------------------------------

func TestAttemptGrantsWithoutReExec(t *testing.T) {
	m, r := testManager(t)
	m.writeMarker(r.resolveID, "declined before") // forced path ignores it
	var out strings.Builder
	res := m.Attempt(context.Background(), &out)
	if res.Action != ActionGranted {
		t.Fatalf("got %+v", res)
	}
	if r.escalateCalls != 1 {
		t.Fatal("forced attempt must escalate regardless of the marker")
	}
	if r.reExecCalls != 0 {
		t.Fatal("the CLI path never re-execs")
	}
	if markerExists(t, m) {
		t.Fatal("a successful attempt clears the marker")
	}
	if !strings.Contains(out.String(), "Restart competent-search-thing") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAttemptReady(t *testing.T) {
	m, r := testManager(t)
	r.state = StateReady
	var out strings.Builder
	if res := m.Attempt(context.Background(), &out); res.Action != ActionOptimal {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(out.String(), "already enabled") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAttemptUnsupported(t *testing.T) {
	m, r := testManager(t)
	r.state = StateUnsupported
	var out strings.Builder
	if res := m.Attempt(context.Background(), &out); res.Reason != "unsupported" {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(out.String(), "not available") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAttemptNonLinux(t *testing.T) {
	m, _ := testManager(t)
	m.goos = "windows"
	var out strings.Builder
	if res := m.Attempt(context.Background(), &out); res.Reason != "not-linux" {
		t.Fatalf("got %+v", res)
	}
}

func TestAttemptDeclinedWritesMarker(t *testing.T) {
	m, r := testManager(t)
	r.escalateErr = errors.New("authentication failed")
	var out strings.Builder
	if res := m.Attempt(context.Background(), &out); res.Action != ActionAttemptFailed {
		t.Fatalf("got %+v", res)
	}
	if !markerExists(t, m) {
		t.Fatal("a forced-attempt failure is also remembered")
	}
	if !strings.Contains(out.String(), "did not complete") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestAttemptUnresolvedExe(t *testing.T) {
	m, r := testManager(t)
	r.resolveOK = false
	var out strings.Builder
	if res := m.Attempt(context.Background(), &out); res.Reason != "unresolved-exe" {
		t.Fatalf("got %+v", res)
	}
}

// --- marker round trips -------------------------------------------

func TestMarkerRoundTrip(t *testing.T) {
	m, _ := testManager(t)
	if _, cur := m.markerCurrent("id-a"); cur {
		t.Fatal("no marker yet")
	}
	m.writeMarker("id-a", "reason-a")
	reason, cur := m.markerCurrent("id-a")
	if !cur || reason != "reason-a" {
		t.Fatalf("reason=%q cur=%v", reason, cur)
	}
	if _, cur := m.markerCurrent("id-b"); cur {
		t.Fatal("a different identity must not match")
	}
	m.clearMarker()
	if _, cur := m.markerCurrent("id-a"); cur {
		t.Fatal("cleared marker should not match")
	}
}

func TestMarkerNoConfigDir(t *testing.T) {
	m, _ := testManager(t)
	m.configDir = ""
	if m.markerPath() != "" {
		t.Fatal("no config dir means no marker path")
	}
	m.writeMarker("id", "reason") // must not panic
	if _, cur := m.markerCurrent("id"); cur {
		t.Fatal("no persistence without a config dir")
	}
	m.clearMarker() // must not panic
}

func TestMarkerCorruptFile(t *testing.T) {
	m, _ := testManager(t)
	if err := os.WriteFile(m.markerPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, cur := m.markerCurrent("id"); cur {
		t.Fatal("a corrupt marker is treated as absent")
	}
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
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q:\n%s", want, s)
		}
	}
	if !isASCII(s) {
		t.Fatal("the grant script must be ASCII")
	}
}

func TestGrantScriptEscapesExe(t *testing.T) {
	s := grantScript("/tmp/a'b/app")
	if !strings.Contains(s, `'/tmp/a'\''b/app'`) {
		t.Fatalf("single quote not escaped:\n%s", s)
	}
}

func TestShellSingleQuote(t *testing.T) {
	cases := map[string]string{
		"plain":  "'plain'",
		"a'b":    `'a'\''b'`,
		"":       "''",
		"/x/y z": "'/x/y z'",
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Fatalf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLastLine(t *testing.T) {
	if got := lastLine("a\nb\n\n"); got != "b" {
		t.Fatalf("got %q", got)
	}
	if got := lastLine(""); got != "" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := lastLine(long); len(got) != 200 {
		t.Fatalf("length %d, want 200", len(got))
	}
}

func TestEscalateError(t *testing.T) {
	// 126 = dismissed / not authorized.
	if err := escalateError(fakeExit(t, 126), "irrelevant"); !strings.Contains(err.Error(), "dismissed") {
		t.Fatalf("126: %v", err)
	}
	// 127 = auth failure; the stderr tail is surfaced.
	if err := escalateError(fakeExit(t, 127), "polkit says no"); !strings.Contains(err.Error(), "polkit says no") {
		t.Fatalf("127: %v", err)
	}
	// Script's own non-zero exit.
	if err := escalateError(fakeExit(t, 3), "setcap not found"); !strings.Contains(err.Error(), "exit 3") || !strings.Contains(err.Error(), "setcap not found") {
		t.Fatalf("3: %v", err)
	}
	// A non-exec error (e.g. context deadline) passes through.
	base := errors.New("context deadline exceeded")
	if err := escalateError(base, ""); err.Error() != base.Error() {
		t.Fatalf("passthrough: %v", err)
	}
}

// --- production seams (safe to exercise on the runner) ------------

func TestNewFillsProductionSeams(t *testing.T) {
	m := New(Config{Backend: "auto", Enabled: true, ConfigDir: t.TempDir()})
	if m.goos == "" || m.probe == nil || m.resolve == nil || m.escalate == nil ||
		m.reExec == nil || m.writeScript == nil || m.getenv == nil || m.logf == nil || m.now == nil {
		t.Fatal("New must fill every seam")
	}
	if len(m.args) == 0 {
		t.Fatal("New must capture argv for re-exec")
	}
}

func TestProbeReturnsAValidState(t *testing.T) {
	// The real probe: on an unprivileged runner it returns
	// StateNeedsCaps, but assert only that it is a valid state so the
	// test is deterministic across environments.
	switch probeFanotifySupport() {
	case StateUnsupported, StateNeedsCaps, StateReady:
	default:
		t.Fatal("invalid state")
	}
}

func TestProdResolveResolvesTestBinary(t *testing.T) {
	exe, id, ok := prodResolve()
	if !ok {
		t.Skip("os.Executable unavailable in this environment")
	}
	if !filepath.IsAbs(exe) {
		t.Fatalf("resolved path not absolute: %q", exe)
	}
	if !strings.HasPrefix(id, exe+"|") {
		t.Fatalf("identity %q should start with the path", id)
	}
}

func TestProdWriteScript(t *testing.T) {
	p, cleanup, err := prodWriteScript("/opt/app/bin")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "cap_sys_admin,cap_dac_read_search+ep") {
		t.Fatalf("script missing the setcap line:\n%s", data)
	}
	cleanup()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("cleanup must remove the script")
	}
}

// --- helpers ------------------------------------------------------

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

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
	if !errors.As(err, &ee) {
		t.Fatalf("expected an ExitError for code %d, got %v", code, err)
	}
	return err
}
