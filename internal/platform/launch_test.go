package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenCommands(t *testing.T) {
	require.Equal(t, [][]string{{"xdg-open", "/tmp/a b.txt"}}, OpenCommands("linux", "/tmp/a b.txt"))
	require.Equal(t, [][]string{{"open", "/tmp/x"}}, OpenCommands("darwin", "/tmp/x"))
	require.Equal(t, [][]string{{"rundll32", "url.dll,FileProtocolHandler", `C:\Users\x file.txt`}},
		OpenCommands("windows", `C:\Users\x file.txt`))
	require.Nil(t, OpenCommands("plan9", "/tmp/x"))
}

func TestRevealCommands(t *testing.T) {
	linux := RevealCommands("linux", "/tmp/dir/file.txt", "")
	require.Len(t, linux, 2)
	require.Equal(t, []string{
		"dbus-send", "--session", "--print-reply",
		"--dest=org.freedesktop.FileManager1",
		"/org/freedesktop/FileManager1",
		"org.freedesktop.FileManager1.ShowItems",
		"array:string:file:///tmp/dir/file.txt",
		"string:",
	}, linux[0], "--print-reply makes a missing file manager a detectable non-zero exit")
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, linux[1], "fallback opens the parent directory")

	require.Equal(t, [][]string{{"open", "-R", "/tmp/x"}}, RevealCommands("darwin", "/tmp/x", ""))
	require.Equal(t, [][]string{{"explorer", `/select,C:\x\y.txt`}}, RevealCommands("windows", `C:\x\y.txt`, ""))
	require.Nil(t, RevealCommands("plan9", "/tmp/x", ""))
}

func TestRevealCommandsCarryStartupID(t *testing.T) {
	linux := RevealCommands("linux", "/tmp/dir/file.txt", "sid_TIME7")
	require.Equal(t, "string:sid_TIME7", linux[0][7],
		"the minted startup id rides the ShowItems startup-id argument")
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, linux[1],
		"the fallback candidate is unchanged (the credential rides its environment instead)")
}

func TestFileURIEscaping(t *testing.T) {
	// Spaces are percent-encoded by net/url; commas additionally,
	// because dbus-send splits array:string: arguments on commas.
	cmds := RevealCommands("linux", "/tmp/weird name,with comma", "")
	require.Equal(t, "array:string:file:///tmp/weird%20name%2Cwith%20comma", cmds[0][6])
}

// logCapture collects Logf lines; reaper goroutines log concurrently.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) logf(format string, v ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, v...))
}

func (c *logCapture) joined() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// scriptedStarter fakes the Start seam: each call consumes the next
// script entry. startErr fails the spawn itself; waitErr is what the
// child's exit reports; a nil block channel means the wait returns
// immediately, otherwise it blocks until the channel closes.
type scriptEntry struct {
	startErr error
	waitErr  error
	block    chan struct{}
}

type scriptedStarter struct {
	calls  [][]string
	envs   [][]string
	script []scriptEntry
}

func (s *scriptedStarter) start(argv, extraEnv []string) (int, func() error, error) {
	i := len(s.calls)
	s.calls = append(s.calls, argv)
	s.envs = append(s.envs, extraEnv)
	if i >= len(s.script) {
		panic(fmt.Sprintf("scriptedStarter: unexpected call %d: %q", i, argv))
	}
	e := s.script[i]
	if e.startErr != nil {
		return 0, nil, e.startErr
	}
	return 1000 + i, func() error {
		if e.block != nil {
			<-e.block
		}
		return e.waitErr
	}, nil
}

func testLauncher(goos string, s *scriptedStarter, lc *logCapture) *Launcher {
	return &Launcher{GOOS: goos, Start: s.start, Grace: 200 * time.Millisecond, Logf: lc.logf}
}

func TestLauncherOpenSuccessLogsSpawn(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{{}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	require.NoError(t, l.Open("/tmp/x"))
	require.Equal(t, [][]string{{"xdg-open", "/tmp/x"}}, s.calls)
	require.Contains(t, lc.joined(), `open: exec ["xdg-open" "/tmp/x"]`,
		"every spawn logs its exact argv")

	require.Error(t, l.Open(""), "empty path is rejected")
	require.Error(t, l.Open("   "), "blank path is rejected")
	require.Len(t, s.calls, 1, "nothing ran for invalid paths")
}

func TestLauncherOpenFastFailureReturnsAndLogs(t *testing.T) {
	boom := errors.New("exit status 3; stderr: no method available for opening the file")
	s := &scriptedStarter{script: []scriptEntry{{waitErr: boom}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	err := l.Open("/tmp/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "xdg-open")
	require.Contains(t, err.Error(), "no method available",
		"the child's stderr reaches the caller")
	require.Contains(t, lc.joined(), "no method available", "and the log")
}

func TestLauncherOpenStillRunningAtGraceIsSuccess(t *testing.T) {
	block := make(chan struct{})
	s := &scriptedStarter{script: []scriptEntry{{block: block, waitErr: errors.New("exit status 9")}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	l.Grace = 30 * time.Millisecond
	require.NoError(t, l.Open("/tmp/x"),
		"a handler still running when the grace window closes counts as success")
	require.NotContains(t, lc.joined(), "exit status 9", "no failure logged yet")
	close(block) // the child now fails, long after the window
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "exit status 9") &&
			strings.Contains(lc.joined(), "grace window")
	}, 2*time.Second, 10*time.Millisecond, "the background reaper logs the late failure")
}

func TestLauncherRevealFallsBackOnStartFailure(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{
		{startErr: errors.New("exec: not found")},
		{},
	}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	require.NoError(t, l.Reveal("/tmp/dir/f.txt"))
	require.Len(t, s.calls, 2, "dbus-send failed to start, xdg-open fallback ran")
	require.Equal(t, "dbus-send", s.calls[0][0])
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, s.calls[1])
	require.Contains(t, lc.joined(), "failed to start")
}

func TestLauncherRevealFallsBackOnFastExit(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{
		{waitErr: errors.New("exit status 1; stderr: Failed to open connection")},
		{},
	}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	require.NoError(t, l.Reveal("/tmp/dir/f.txt"))
	require.Len(t, s.calls, 2,
		"dbus-send started but exited non-zero inside the grace window; the fallback ran")
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, s.calls[1])
	require.Contains(t, lc.joined(), "Failed to open connection")
}

func TestLauncherAllCandidatesFail(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{
		{startErr: errors.New("exec: not found")},
		{startErr: errors.New("exec: not found")},
	}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	err := l.Reveal("/tmp/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "dbus-send")
	require.Contains(t, err.Error(), "xdg-open")
	require.Error(t, l.Reveal(""), "empty path is rejected")
}

func TestLauncherUnsupportedGOOS(t *testing.T) {
	s := &scriptedStarter{}
	l := testLauncher("plan9", s, &logCapture{})
	err := l.Open("/tmp/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan9")
	require.Error(t, l.Reveal("/tmp/x"))
	require.Empty(t, s.calls)
}

func TestLauncherNilLogfIsSafe(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{{}}}
	l := &Launcher{GOOS: "linux", Start: s.start, Grace: 50 * time.Millisecond}
	require.NoError(t, l.Open("/tmp/x"))
}

func requireSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
}

func TestStartObservedCapturesStderrAndExitStatus(t *testing.T) {
	requireSh(t)
	l := NewLauncher()
	pid, wait, err := l.startObserved([]string{"sh", "-c", "echo boom goes stderr >&2; exit 3"}, nil)
	require.NoError(t, err)
	require.Positive(t, pid, "the child pid is reported")
	werr := wait()
	require.Error(t, werr)
	require.Contains(t, werr.Error(), "exit status 3")
	require.Contains(t, werr.Error(), "boom goes stderr")

	_, wait, err = l.startObserved([]string{"sh", "-c", "exit 0"}, nil)
	require.NoError(t, err)
	require.NoError(t, wait(), "a clean exit reports no error")
}

func TestStartObservedDeliversExtraEnv(t *testing.T) {
	requireSh(t)
	l := NewLauncher()
	// The child proves the env entry arrived by echoing it to stderr
	// and failing, which folds the stderr into the wait error.
	_, wait, err := l.startObserved(
		[]string{"sh", "-c", "echo got:$COMPETENT_SEARCH_TEST_TOKEN >&2; exit 3"},
		[]string{"COMPETENT_SEARCH_TEST_TOKEN=tok123"})
	require.NoError(t, err)
	werr := wait()
	require.Error(t, werr)
	require.Contains(t, werr.Error(), "got:tok123", "extraEnv reaches the child process")
}

func TestRunDetachedReaperLogsFailure(t *testing.T) {
	requireSh(t)
	lc := &logCapture{}
	l := NewLauncher()
	l.Logf = lc.logf
	require.NoError(t, l.Run([]string{"sh", "-c", "echo bad launch >&2; exit 5"}, nil),
		"Run stays fire-and-forget: a started process is a success")
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "exit status 5") &&
			strings.Contains(lc.joined(), "bad launch")
	}, 3*time.Second, 20*time.Millisecond, "the reaper logs the non-zero exit with stderr")
	require.Contains(t, lc.joined(), "run: exec", "the spawn itself was logged")
}

func TestNewLauncherStartsRealProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/true equivalent")
	}
	l := NewLauncher()
	require.Equal(t, runtime.GOOS, l.GOOS)
	require.Equal(t, DefaultGrace, l.Grace)
	require.NoError(t, l.Run([]string{"true"}, nil), "runDetached starts a real command")
	require.Error(t, l.Run([]string{"definitely-not-a-binary-xyz"}, nil))
}

func TestLauncherOpenEnvThreadsEnvToEveryCandidate(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{{}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	env := []string{"DESKTOP_STARTUP_ID=abc", "XDG_ACTIVATION_TOKEN=abc"}
	require.NoError(t, l.OpenEnv("/tmp/x", env))
	require.Equal(t, [][]string{env}, s.envs, "the credential env rides the candidate spawn")

	s2 := &scriptedStarter{script: []scriptEntry{{startErr: errors.New("gone")}, {}}}
	l2 := testLauncher("linux", s2, lc)
	require.NoError(t, l2.RevealEnv("/tmp/d/f", env, "abc"))
	require.Equal(t, [][]string{env, env}, s2.envs, "every candidate gets the env, fallback included")
	require.Equal(t, "string:abc", s2.calls[0][7])
}

func TestLauncherLaunch(t *testing.T) {
	s := &scriptedStarter{script: []scriptEntry{{}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	pid, err := l.Launch([]string{"gedit", "/tmp/x"}, []string{"DESKTOP_STARTUP_ID=i"})
	require.NoError(t, err)
	require.Equal(t, 1000, pid, "the child pid comes back for the raise watcher")
	require.Equal(t, [][]string{{"gedit", "/tmp/x"}}, s.calls)
	require.Equal(t, [][]string{{"DESKTOP_STARTUP_ID=i"}}, s.envs)
	require.Contains(t, lc.joined(), `launch: exec ["gedit" "/tmp/x"]`)

	_, err = l.Launch(nil, nil)
	require.Error(t, err, "empty argv is rejected")
	_, err = l.Launch([]string{"  "}, nil)
	require.Error(t, err, "blank argv0 is rejected")
}

func TestLauncherLaunchFastFailure(t *testing.T) {
	boom := errors.New("exit status 127; stderr: not found")
	s := &scriptedStarter{script: []scriptEntry{{waitErr: boom}}}
	lc := &logCapture{}
	l := testLauncher("linux", s, lc)
	_, err := l.Launch([]string{"gedit"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")

	s2 := &scriptedStarter{script: []scriptEntry{{startErr: errors.New("exec: no gedit")}}}
	l2 := testLauncher("linux", s2, lc)
	pid, err := l2.Launch([]string{"gedit"}, nil)
	require.Error(t, err)
	require.Zero(t, pid)
}

func TestLauncherLaunchStillRunningAtGraceIsSuccess(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s := &scriptedStarter{script: []scriptEntry{{block: block}}}
	l := testLauncher("linux", s, &logCapture{})
	l.Grace = 20 * time.Millisecond
	pid, err := l.Launch([]string{"gedit"}, nil)
	require.NoError(t, err, "an application still starting at grace expiry is success")
	require.Equal(t, 1000, pid)
}
