package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

func TestNewHasNoContext(t *testing.T) {
	a := New(nil, 0)
	require.Nil(t, a.ctx)
}

func TestStartupSavesContext(t *testing.T) {
	a := New(nil, 0)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	a.Startup(ctx)
	require.Equal(t, ctx, a.ctx)
}

func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	a := New(index.NewManager(nil, nil, 0), 0)
	for _, q := range []string{"", "   ", "\t \n"} {
		got := a.Search(q)
		require.NotNil(t, got)
		require.Empty(t, got)
	}
}

func TestSearchWithoutManagerReturnsEmpty(t *testing.T) {
	a := New(nil, 0)
	got := a.Search("hello")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestSearchQueriesManager(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/notes", "shopping-list.txt", false))
	require.NoError(t, m.Add("/notes", "projects", true))
	a := New(m, 0)

	got := a.Search("  shopping  ") // query is trimmed
	require.Len(t, got, 1)
	require.Equal(t, Result{Path: "/notes/shopping-list.txt", Name: "shopping-list.txt", IsDir: false}, got[0])

	miss := a.Search("no-such-entry")
	require.NotNil(t, miss, "no-match result is empty but non-nil")
	require.Empty(t, miss)
}

func TestStartupKicksOffIndexBuild(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world.md"), []byte("x"), 0o644))

	m := index.NewManager([]string{dir}, nil, 0)
	a := New(m, 0)
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return m.LiveCount() == 2 },
		5*time.Second, 10*time.Millisecond, "background build fills the index")

	// A second Startup (e.g. context refresh) must not rebuild.
	a.Startup(context.Background())
	require.Len(t, a.Search("hello"), 1)
}

// watchUp reports whether the live-update layer has been installed.
func watchUp(a *App) bool {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	return a.watcher != nil && a.rescanner != nil
}

func TestStartupBringsUpWatcherAndAppliesEvents(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a := New(m, 0)
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond, "watch layer comes up after the initial build")

	// End to end: a file created NOW reaches Search via fsnotify.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live-created.txt"), []byte("x"), 0o644))
	require.Eventually(t, func() bool { return len(a.Search("live-created")) == 1 },
		20*time.Second, 10*time.Millisecond, "live update flows through to Search")

	a.Shutdown(context.Background())
	a.Shutdown(context.Background()) // idempotent
	require.False(t, watchUp(a), "shutdown tears the watch layer down")
}

func TestShutdownBeforeBuildFinishesSkipsWatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a := New(m, 0)
	a.Shutdown(context.Background()) // before Startup: sets the flag, stops nothing
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return m.LiveCount() == 1 },
		20*time.Second, 10*time.Millisecond, "build still completes")
	require.Never(t, func() bool { return watchUp(a) },
		400*time.Millisecond, 20*time.Millisecond, "watch layer never starts after Shutdown")
}

func TestStartWatchToleratesBadExcluder(t *testing.T) {
	// A malformed exclude pattern cannot panic startWatch; the watcher
	// runs with a nil Excluder (excludes nothing).
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a := New(m, 0)
	a.startWatch()
	require.True(t, watchUp(a))
	a.Shutdown(context.Background())
}

func TestBuildIndexLogsAndSurvivesFailure(t *testing.T) {
	// A malformed exclude pattern makes BuildFromDisk fail; buildIndex
	// must swallow it (log only), never panic.
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a := New(m, 0)
	a.buildIndex()
	require.Equal(t, 0, m.LiveCount())
}

func TestLogProgress(t *testing.T) {
	// Smoke coverage for both branches; output goes to the log.
	logProgress(10, false)
	logProgress(10, true)
}

func TestOpenValidatesAndStubs(t *testing.T) {
	a := New(nil, 0)
	err := a.Open("")
	require.Error(t, err)
	err = a.Open("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))
}

func TestRevealValidatesAndStubs(t *testing.T) {
	a := New(nil, 0)
	err := a.Reveal("")
	require.Error(t, err)
	err = a.Reveal("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))
}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a := New(nil, 0)
	// Hide before Startup must be a safe no-op. The branch that calls
	// runtime.WindowHide needs a real Wails context and cannot run in
	// headless unit tests, so it stays uncovered by design.
	a.Hide()
}
