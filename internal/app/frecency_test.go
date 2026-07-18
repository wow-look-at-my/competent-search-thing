package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// waitRecorded polls until the store holds an entry for path.
func waitRecorded(t *testing.T, a *App, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		st := a.frecencyStore()
		if st == nil {
			return false
		}
		_, ok := st.Entries()[path]
		return ok
	}, 2*time.Second, 5*time.Millisecond, "expected %s to be recorded", path)
}

func TestStartFrecencyWiresBlend(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, nil, 10)
	fc := config.DefaultFrecency()
	fc.WeightNoise = 2.5
	a, _ := newTestApp(t, m, Options{Frecency: fc})
	a.Startup(context.Background())

	b := m.Blend()
	require.NotNil(t, b, "Startup must hand the Manager a blend")
	require.Same(t, a.frecencyStore(), b.Signals.Store)
	require.NotNil(t, b.Signals.Probe)
	require.Equal(t, 1.0, b.Signals.CwdWeight)
	require.Equal(t, 1.0, b.WeightFrecency)
	require.Equal(t, 1.0, b.WeightRecency)
	require.Equal(t, 2.5, b.WeightNoise)
	require.Equal(t, 3.0, b.TierJump)
}

func TestStartFrecencyDisabled(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, nil, 10)
	a, _ := newTestApp(t, m, Options{Frecency: config.FrecencyConfig{Disabled: true}})
	a.plat.procTree = func() frecency.ProcTree {
		t.Fatal("the process tree must never be consulted when frecency is disabled")
		return nil
	}
	a.Startup(context.Background())

	require.Nil(t, a.frecencyStore())
	require.Nil(t, m.Blend(), "a disabled config must leave the Manager blend-free")
	require.NoError(t, a.Open("/some/file.txt"))
	a.captureAppContext()
	require.Nil(t, a.frecencyStore(), "recordOpen and the cwd capture must stay no-ops")
}

// TestRecordOpenPaths pins WHERE the single recordOpen hook fires:
// the success paths of Open, Reveal, and the open_path plugin action
// -- and nowhere else (failures, URLs, config opens).
func TestRecordOpenPaths(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())

	// Open success records.
	require.NoError(t, a.Open("/docs/report.txt"))
	waitRecorded(t, a, "/docs/report.txt")

	// Reveal success records.
	require.NoError(t, a.Reveal("/docs/photo.png"))
	waitRecorded(t, a, "/docs/photo.png")

	// The open_path plugin action flows through Open and records.
	require.NoError(t, a.RunPluginAction("test", plugin.Action{Type: plugin.ActionOpenPath, Value: "/docs/plugin.txt"}))
	waitRecorded(t, a, "/docs/plugin.txt")

	// A FAILED open records nothing (the guard is synchronous, so no
	// wait is needed once the next success has round-tripped).
	a.plat.open = func(path string) error { return errors.New("boom") }
	require.Error(t, a.Open("/docs/failed.txt"))
	a.plat.open = func(path string) error { r.call("open:" + path); return nil }

	// open_url shares Open but is filtered by the absolute-path guard.
	require.NoError(t, a.RunPluginAction("test", plugin.Action{Type: plugin.ActionOpenURL, Value: "https://example.com/page"}))

	// Beacon: one more success proves the async recorder drained.
	require.NoError(t, a.Open("/docs/beacon.txt"))
	waitRecorded(t, a, "/docs/beacon.txt")
	entries := a.frecencyStore().Entries()
	require.NotContains(t, entries, "/docs/failed.txt")
	require.NotContains(t, entries, "https://example.com/page")
	require.Len(t, entries, 4)
}

// TestRecordOpenPersists: recorded opens land in
// <configDir>/frecency.json (the store persists by default).
func TestRecordOpenPersists(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())
	require.NoError(t, a.Open("/docs/persist.txt"))
	waitRecorded(t, a, "/docs/persist.txt")

	dir, err := config.Dir()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, frecencyFileName))
		return err == nil && len(data) > 0
	}, 2*time.Second, 5*time.Millisecond, "frecency.json must be written")
}

// TestStartFrecencyCorruptState: a corrupt frecency.json starts the
// store empty (one logged line) and the layer keeps working.
func TestStartFrecencyCorruptState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, frecencyFileName), []byte("{not json"), 0o600))

	a, _ := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	// newTestApp already pointed the config dir elsewhere; repoint at
	// the dir holding the corrupt file.
	t.Setenv(config.EnvConfigDir, dir)
	a.Startup(context.Background())
	require.NotNil(t, a.frecencyStore())

	// The async load may still be replacing state; poll an open until
	// it sticks, proving the store runs on after the corrupt load.
	require.Eventually(t, func() bool {
		require.NoError(t, a.Open("/docs/after.txt"))
		_, ok := a.frecencyStore().Entries()["/docs/after.txt"]
		return ok
	}, 2*time.Second, 10*time.Millisecond)
}

// fakeProcTree scripts the frecency.ProcTree seam.
type fakeProcTree struct {
	cwds map[int]string
	kids map[int][]int
	fg   map[int]int
}

func (f fakeProcTree) Children(pid int) []int { return f.kids[pid] }
func (f fakeProcTree) Cwd(pid int) (string, error) {
	if c, ok := f.cwds[pid]; ok {
		return c, nil
	}
	return "", errors.New("unreadable")
}
func (f fakeProcTree) Foreground(pid int) (int, bool) {
	fg, ok := f.fg[pid]
	return fg, ok
}

// TestCaptureFrecencyCwd: the summon-time capture derives the focused
// app's working directory through the procTree seam and stashes it in
// the Manager's blend; losing focus clears it.
func TestCaptureFrecencyCwd(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, nil, 10)
	a, r := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	src := &fakeSource{r: r, focused: appctx.AppInfo{Name: "term", PID: 100}, focusedOK: true}
	a.plat.appSource = src
	a.plat.procTree = func() frecency.ProcTree {
		return fakeProcTree{
			cwds: map[int]string{210: "/work/proj"},
			kids: map[int][]int{100: {210}},
			fg:   map[int]int{},
		}
	}
	a.Startup(context.Background())
	require.Empty(t, m.Blend().Signals.Cwd)

	a.captureAppContext()
	require.Eventually(t, func() bool {
		return m.Blend().Signals.Cwd == "/work/proj"
	}, 2*time.Second, 5*time.Millisecond, "the derived cwd must reach the Manager's blend")

	// No focused app on the next summon: the stale boost clears.
	src.focusedOK = false
	src.focused = appctx.AppInfo{}
	a.captureAppContext()
	require.Eventually(t, func() bool {
		return m.Blend().Signals.Cwd == ""
	}, 2*time.Second, 5*time.Millisecond, "losing the focused app must clear the cwd boost")
}

// TestCaptureFrecencyCwdGates: no procTree source (non-linux) and a
// disabled cwd weight both skip the derivation entirely.
func TestCaptureFrecencyCwdGates(t *testing.T) {
	fc := config.DefaultFrecency()
	fc.WeightCwd = -1 // the documented off switch
	m := index.NewManager([]string{t.TempDir()}, nil, 10)
	a, r := newTestApp(t, m, Options{Frecency: fc})
	a.plat.appSource = &fakeSource{r: r, focused: appctx.AppInfo{PID: 100}, focusedOK: true}
	called := false
	a.plat.procTree = func() frecency.ProcTree { called = true; return fakeProcTree{} }
	a.Startup(context.Background())
	a.captureAppContext()
	require.False(t, called, "a non-positive cwd weight must skip the walk")

	// And the nil factory (windows/darwin) is simply inert.
	a.plat.procTree = nil
	a.captureAppContext()
	require.Empty(t, m.Blend().Signals.Cwd)
}

// TestSetFrecencyCwdSwapsFreshBlend: every cwd change hands the
// Manager a FRESH blend (the handed-over one is immutable by
// contract); an unchanged value swaps nothing.
func TestSetFrecencyCwdSwapsFreshBlend(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, nil, 10)
	a, _ := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())
	first := m.Blend()

	a.setFrecencyCwd("/work/x")
	second := m.Blend()
	require.NotSame(t, first, second)
	require.Equal(t, "/work/x", second.Signals.Cwd)
	require.Empty(t, first.Signals.Cwd, "the previously handed-over blend is never mutated")

	a.setFrecencyCwd("/work/x")
	require.Same(t, second, m.Blend(), "an unchanged cwd swaps nothing")
}
