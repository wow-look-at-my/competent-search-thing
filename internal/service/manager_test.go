package service

// The shared test harness (scripted runner + Manager builder) plus
// the OS-independent tests. The launchd backend's tests live in
// launchd_test.go, the systemd backend's in systemd_test.go, the
// Ensure decision matrix in ensure_test.go -- a split, not a
// convention: every file stays under the repo's length cap.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testExe    = "/opt/csearch/bin/competent-search-thing"
	guiSvc     = "gui/501/" + Label
	guiBrewSvc = "gui/501/" + brewLabel
)

// scriptedRunner answers launchctl/systemctl invocations from a
// canned per-argv table and records every call in order (the
// internal/gsettings scriptedRunner pattern). An invocation nothing
// was scripted for fails the test: the exact argv surface is part of
// the contract under test.
type scriptedRunner struct {
	t     *testing.T
	mu    sync.Mutex
	calls [][]string
	out   map[string][]string
	errs  map[string]error
}

func newScriptedRunner(t *testing.T) *scriptedRunner {
	return &scriptedRunner{t: t, out: map[string][]string{}, errs: map[string]error{}}
}

func argvKey(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

// on scripts one successful invocation; scripting the same argv again
// queues outputs (each call pops the next, the last stays sticky).
func (s *scriptedRunner) on(out, name string, args ...string) {
	k := argvKey(name, args)
	s.out[k] = append(s.out[k], out)
}

// fail scripts a failing invocation.
func (s *scriptedRunner) fail(err error, name string, args ...string) {
	s.errs[argvKey(name, args)] = err
}

func (s *scriptedRunner) run(_ context.Context, name string, args ...string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, append([]string{name}, args...))
	s.mu.Unlock()
	k := argvKey(name, args)
	if err, ok := s.errs[k]; ok {
		return "", err
	}
	if outs, ok := s.out[k]; ok {
		out := outs[0]
		if len(outs) > 1 {
			s.out[k] = outs[1:]
		}
		return out, nil
	}
	s.t.Fatalf("unexpected runner call: %q", append([]string{name}, args...))
	return "", nil
}

func (s *scriptedRunner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *scriptedRunner) allCalls() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.calls...)
}

// testManager builds a Manager over a temp home + scripted runner.
// OptOutFile and SystemUnitDirs stay zero (no marker, no deb probe);
// the ensure tests fill them per case.
func testManager(t *testing.T, goos string) (*Manager, *scriptedRunner) {
	t.Helper()
	r := newScriptedRunner(t)
	return &Manager{
		GOOS:   goos,
		Run:    r.run,
		Exe:    testExe,
		Home:   t.TempDir(),
		UID:    "501",
		Getenv: func(string) string { return "" },
	}, r
}

// errNotFound is the launchctl print failure for an unknown service.
var errNotFound = errors.New("Could not find service in domain")

// scriptNoBrewAgent scripts the brew-ownership probe answering "no".
func scriptNoBrewAgent(r *scriptedRunner) {
	r.fail(errNotFound, "launchctl", "print", guiBrewSvc)
}

func TestUnsupportedOS(t *testing.T) {
	m, r := testManager(t, "windows")
	ctx := context.Background()

	_, err := m.Install(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	_, err = m.Uninstall(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	_, err = m.Status(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	err = m.Restart(ctx)
	require.ErrorContains(t, err, "not supported on windows")
	require.Zero(t, r.callCount())
}

func TestWriteIfChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	changed, err := writeIfChanged(path, []byte("one"), 0o644)
	require.NoError(t, err)
	require.True(t, changed)

	changed, err = writeIfChanged(path, []byte("one"), 0o644)
	require.NoError(t, err)
	require.False(t, changed, "identical content is not rewritten")

	changed, err = writeIfChanged(path, []byte("two"), 0o644)
	require.NoError(t, err)
	require.True(t, changed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "two", string(data))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temp files left behind")
}

func TestProductionRun(t *testing.T) {
	out, err := Run(context.Background(), "sh", "-c", "echo hi")
	require.NoError(t, err)
	require.Equal(t, "hi\n", out)

	_, err = Run(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom", "stderr is folded into the error")
	require.Contains(t, err.Error(), "exit status 3")
}

func TestNewManagerProduction(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("COMPETENT_SEARCH_CONFIG_DIR", cfgDir)
	m, err := NewManager()
	require.NoError(t, err)
	require.NotEmpty(t, m.GOOS)
	require.NotEmpty(t, m.Exe)
	require.True(t, filepath.IsAbs(m.Exe))
	require.NotEmpty(t, m.Home)
	require.NotEmpty(t, m.UID)
	require.NotNil(t, m.Run)
	require.NotNil(t, m.Getenv)
	require.Equal(t, OptOutPath(cfgDir), m.OptOutFile,
		"the opt-out marker lives in the config dir")
	require.NotEmpty(t, m.SystemUnitDirs, "the deb probe dirs are wired")
}

// --- opt-out marker ---------------------------------------------------

func TestOptOutWriteClearRoundTrip(t *testing.T) {
	m, _ := testManager(t, "linux")
	m.OptOutFile = OptOutPath(filepath.Join(m.Home, "cfg"))

	require.False(t, m.optedOut(), "no marker yet")
	require.NoError(t, m.WriteOptOut())
	require.True(t, m.optedOut())

	data, err := os.ReadFile(m.OptOutFile)
	require.NoError(t, err)
	require.Contains(t, string(data), "service install", "the marker explains how to undo itself")

	require.NoError(t, m.ClearOptOut())
	require.False(t, m.optedOut())
	require.NoError(t, m.ClearOptOut(), "clearing twice is a no-op")
}

func TestOptOutUnresolvedConfigDir(t *testing.T) {
	m, _ := testManager(t, "linux")
	require.Empty(t, m.OptOutFile)
	require.False(t, m.optedOut(), "no marker path = never opted out")
	require.Error(t, m.WriteOptOut(), "recording an opt-out needs a config dir")
	require.NoError(t, m.ClearOptOut(), "clearing without a path is a harmless no-op")
}

func TestOptOutPathShape(t *testing.T) {
	require.Equal(t, filepath.Join("/cfg", "service.optout"), OptOutPath("/cfg"))
}
