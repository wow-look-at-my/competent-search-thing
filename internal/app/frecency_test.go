package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	// The learned layers are on by default and would install their
	// own blend resolvers; their escape hatches keep this pin about
	// the frecency layer alone.
	a, _ := newTestApp(t, m, Options{
		Frecency: config.FrecencyConfig{Disabled: true},
		Priors:   config.PriorsConfig{Disabled: true},
		Arbiter:  config.ArbiterConfig{Disabled: true},
	})
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
	a.plat.open = func(path string, _ []string) error { return errors.New("boom") }
	require.Error(t, a.Open("/docs/failed.txt"))
	a.plat.open = func(path string, _ []string) error { r.call("open:" + path); return nil }

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

// TestRecordAppPickPaths pins WHERE the app-launch usage hook fires:
// the RunPluginAction run_command success path, for the two builtin
// app sources only -- and nowhere else (failures, external plugins'
// run_commands, other action types).
func TestRecordAppPickPaths(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())

	// A targeted !app launch (linux shape: desktop id) records under
	// its desktop-id key.
	require.NoError(t, a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"firefox"}, DesktopID: "firefox.desktop"}))
	waitRecorded(t, a, "app:firefox.desktop")

	// An apps-search launch (darwin shape: no desktop id, `open -a`
	// argv) records under the argv key.
	require.NoError(t, a.RunPluginAction("apps-search", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"open", "-a", "/Applications/Safari.app"}}))
	waitRecorded(t, a, "app:open -a /Applications/Safari.app")

	// An external plugin's run_command executes but records nothing.
	require.NoError(t, a.RunPluginAction("some-plugin", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"external-tool"}}))
	require.True(t, r.has("run:external-tool"))

	// A FAILED launch records nothing.
	a.plat.run = func(argv, _ []string) error { return errors.New("boom") }
	require.Error(t, a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"broken"}, DesktopID: "broken.desktop"}))
	a.plat.run = func(argv, _ []string) error { return nil }

	// Beacon: one more success proves the async recorder drained.
	require.NoError(t, a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"beacon"}, DesktopID: "beacon.desktop"}))
	waitRecorded(t, a, "app:beacon.desktop")
	entries := a.frecencyStore().Entries()
	require.NotContains(t, entries, "app:external-tool")
	require.NotContains(t, entries, "app:broken.desktop")
	require.Len(t, entries, 3)
}

// TestAppUsageReadsLiveStore: the registry's Options.AppUsage seam
// answers from the CURRENT store on every call, so a live
// search.frecency config change (applyFrecencyConfig swaps the store)
// applies without a registry reload -- and disabled frecency reads 0
// for every key (the cold pure-name app ordering).
func TestAppUsageReadsLiveStore(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())

	require.Zero(t, a.appUsage("app:code.desktop"), "an unrecorded key reads 0")
	require.NoError(t, a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"code"}, DesktopID: "code.desktop"}))
	waitRecorded(t, a, "app:code.desktop")
	require.Positive(t, a.appUsage("app:code.desktop"))

	// Disable live: the same getter now reads 0 through the swapped
	// (nil) store.
	a.applyFrecencyConfig(config.FrecencyConfig{Disabled: true})
	require.Zero(t, a.appUsage("app:code.desktop"), "disabled frecency = cold ordering")

	// Re-enable live: the store rebuilds over the SAME frecency.json,
	// so the learned usage comes back once the async load finishes.
	a.applyFrecencyConfig(config.DefaultFrecency())
	require.Eventually(t, func() bool {
		return a.appUsage("app:code.desktop") > 0
	}, 2*time.Second, 5*time.Millisecond, "the on-disk usage must survive a config round-trip")
}

// TestAppKeysStayOutOfFileRanking pins the namespace non-collision:
// app usage keys live in the same frecency store as file opens, but
// they start "app:" and are therefore never equal to the absolute
// paths the file-ranking blend looks up (Store.Boost is an exact-key
// lookup on both sides), so recorded app launches can never boost or
// penalize a file result.
func TestAppKeysStayOutOfFileRanking(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())

	require.NoError(t, a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"code"}, DesktopID: "code.desktop"}))
	waitRecorded(t, a, "app:code.desktop")
	require.NoError(t, a.Open("/docs/code"))
	waitRecorded(t, a, "/docs/code")

	st := a.frecencyStore()
	require.Positive(t, st.Boost("/docs/code"))
	require.Positive(t, st.Boost("app:code.desktop"))
	require.Zero(t, st.Boost("/app:code.desktop"), "exact-key lookups: no abs path can alias an app key")
	for key := range st.Entries() {
		if strings.HasPrefix(key, "app:") {
			require.False(t, filepath.IsAbs(key), "app keys must never be absolute paths")
		}
	}
}
