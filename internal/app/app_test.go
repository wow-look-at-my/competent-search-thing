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
	a := New(nil)
	require.Nil(t, a.ctx)
}

func TestStartupSavesContext(t *testing.T) {
	a := New(nil)
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	a.Startup(ctx)
	require.Equal(t, ctx, a.ctx)
}

func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	a := New(index.NewManager(nil, nil, 0))
	for _, q := range []string{"", "   ", "\t \n"} {
		got := a.Search(q)
		require.NotNil(t, got)
		require.Empty(t, got)
	}
}

func TestSearchWithoutManagerReturnsEmpty(t *testing.T) {
	a := New(nil)
	got := a.Search("hello")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestSearchQueriesManager(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/notes", "shopping-list.txt", false))
	require.NoError(t, m.Add("/notes", "projects", true))
	a := New(m)

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
	a := New(m)
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return m.LiveCount() == 2 },
		5*time.Second, 10*time.Millisecond, "background build fills the index")

	// A second Startup (e.g. context refresh) must not rebuild.
	a.Startup(context.Background())
	require.Len(t, a.Search("hello"), 1)
}

func TestBuildIndexLogsAndSurvivesFailure(t *testing.T) {
	// A malformed exclude pattern makes BuildFromDisk fail; buildIndex
	// must swallow it (log only), never panic.
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a := New(m)
	a.buildIndex()
	require.Equal(t, 0, m.LiveCount())
}

func TestLogProgress(t *testing.T) {
	// Smoke coverage for both branches; output goes to the log.
	logProgress(10, false)
	logProgress(10, true)
}

func TestOpenValidatesAndStubs(t *testing.T) {
	a := New(nil)
	err := a.Open("")
	require.Error(t, err)
	err = a.Open("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))
}

func TestRevealValidatesAndStubs(t *testing.T) {
	a := New(nil)
	err := a.Reveal("")
	require.Error(t, err)
	err = a.Reveal("/tmp")
	require.True(t, errors.Is(err, ErrNotImplemented))
}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a := New(nil)
	// Hide before Startup must be a safe no-op. The branch that calls
	// runtime.WindowHide needs a real Wails context and cannot run in
	// headless unit tests, so it stays uncovered by design.
	a.Hide()
}
