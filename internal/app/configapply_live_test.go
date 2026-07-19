package app

// Phase-B live-applier tests: every section of the apply table routes
// to real machinery now, exercised headlessly through the newTestApp
// seams (see configapply_test.go for the engine-shape tests).

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/sysstats"
)

func TestApplyHotkeyReRegistersNativeBackend(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	var mu sync.Mutex
	var specs []string
	a.plat.startHotkey = func(hk platform.Hotkey, _ func()) (func(), error) {
		mu.Lock()
		specs = append(specs, hk.String())
		mu.Unlock()
		return func() { r.call("stopHotkey") }, nil
	}
	a.Startup(context.Background())
	require.Equal(t, "alt+space", a.hotkeyDescription())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Hotkey = "ctrl+shift+k"
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "hotkey")
	require.True(t, r.has("stopHotkey"), "the old registration is released first")
	require.Equal(t, "ctrl+shift+k", a.hotkeyDescription(), "the new spec is active")
	mu.Lock()
	got := append([]string(nil), specs...)
	mu.Unlock()
	require.Equal(t, []string{"alt+space", "ctrl+shift+k"}, got)

	// Re-applying the SAME document changes nothing (the diff gate).
	res = a.applyConfig(&next, "test")
	require.NotContains(t, res.Applied, "hotkey")
}

func TestApplyHotkeyEmptySpecReleasesOnly(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.Startup(context.Background())
	require.Equal(t, "alt+space", a.hotkeyDescription())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Hotkey = ""
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "hotkey")
	require.True(t, r.has("stopHotkey"), "the registration is released")
	require.Empty(t, a.hotkeyDescription(), "no summon key is advertised while disabled")
}

func TestApplyHotkeyForceReachesGnomeBinding(t *testing.T) {
	// On a GNOME Wayland session (portal unavailable), the INITIAL
	// registration is sticky (force=false) while a config CHANGE
	// re-registers with force=true -- the one path allowed to rewrite
	// the installed accelerator.
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	var mu sync.Mutex
	var forces []bool
	a.plat.ensureGnomeBinding = func(_ context.Context, hk platform.Hotkey, command string, force bool) (gsettings.Applied, error) {
		mu.Lock()
		forces = append(forces, force)
		mu.Unlock()
		accel, err := gsettings.ConvertHotkey(hk)
		if err != nil {
			return gsettings.Applied{}, err
		}
		return verifiedApplied(gsettings.Applied{Binding: accel, Requested: accel}, command), nil
	}
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Alt>space" },
		5*time.Second, 5*time.Millisecond, "the initial chain lands")
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Hotkey = "ctrl+alt+t"
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "hotkey")
	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Control><Alt>t" },
		5*time.Second, 5*time.Millisecond, "the re-registration chain lands")
	mu.Lock()
	got := append([]bool(nil), forces...)
	mu.Unlock()
	require.Equal(t, []bool{false, true}, got,
		"initial registration stays sticky; only the config change forces the rebind")
}

func TestApplyStatsDisableAndReEnable(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	fake := &fakeStatsSource{snap: sysstats.Snapshot{CPUPct: 5, CPUOK: true}}
	a.newStats = func() statsSource { return fake }
	a.Startup(context.Background())
	starts, startCtx, _ := fake.state()
	require.Equal(t, 1, starts)
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Stats.Disabled = true
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "stats")
	require.Error(t, startCtx.Err(), "the sampler context is cancelled on disable")
	require.Equal(t, sysstats.Snapshot{}, a.GetStats(), "the sampler is dropped")
	events := r.emitted(eventStatsUpdate)
	require.NotEmpty(t, events, "disabling emits one hide-the-row snapshot")
	snap, ok := events[len(events)-1].payload[0].(sysstats.Snapshot)
	require.True(t, ok)
	require.False(t, snap.Enabled, "the emitted snapshot reports the feature off")

	// Re-enable while the bar is visible: the sampler restarts and is
	// woken for the visible bar.
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()
	next2 := config.Default()
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "stats")
	starts, startCtx, visible := fake.state()
	require.Equal(t, 2, starts, "a fresh Start under a fresh context")
	require.NoError(t, startCtx.Err())
	require.Contains(t, visible, true, "the visible bar re-arms sampling")
}

func TestApplyTrayDisableAndReEnable(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "linux"
	f := &fakeTrayHandle{}
	a.newTray = func() trayHandle { return f }
	a.Startup(context.Background())
	require.Eventually(t, func() bool { s, _, _ := f.snapshot(); return s == 1 },
		5*time.Second, time.Millisecond)
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Tray.Disabled = true
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "tray")
	require.Eventually(t, func() bool { _, c, _ := f.snapshot(); return c == 1 },
		5*time.Second, time.Millisecond, "disabling closes the icon")
	_, _, startCtx := f.snapshot()
	require.Error(t, startCtx.Err(), "a Start still waiting on the bus is aborted")

	next2 := config.Default()
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "tray")
	require.Eventually(t, func() bool { s, _, _ := f.snapshot(); return s == 2 },
		5*time.Second, time.Millisecond, "re-enabling starts a fresh icon")
}

func TestApplyHistoryPersistFlipKeepsRecall(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.AddHistory("one")
	a.AddHistory("two")
	seedBaseline(a, config.Default())
	dir, err := config.Dir()
	require.NoError(t, err)
	histPath := filepath.Join(dir, historyFileName)

	next := config.Default()
	next.History.PersistDisabled = true
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "history")
	require.Equal(t, []string{"one", "two"}, a.GetHistory(), "in-session recall survives the flip")

	// Entries added while persistence is off never touch the disk.
	a.AddHistory("memory-only")
	data, err := os.ReadFile(histPath)
	require.NoError(t, err)
	require.NotContains(t, string(data), "memory-only")

	// Flipping persistence back on replays the in-memory entries over
	// the on-disk ones -- nothing is lost on either side.
	next2 := config.Default()
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "history")
	require.Equal(t, []string{"one", "two", "memory-only"}, a.GetHistory())
	data, err = os.ReadFile(histPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "memory-only", "the replay persisted the memory-only entry")
}

func TestApplyFrecencyDisableAndRebuild(t *testing.T) {
	mgr := index.NewManager(nil, nil, 0)
	a, _ := newTestApp(t, mgr, Options{})
	a.Startup(context.Background())
	require.NotNil(t, a.frecencyStore(), "the default configuration builds the store")
	require.NotNil(t, mgr.Blend())
	// Learn one open so the rebuild's disk survival is observable.
	require.NoError(t, a.frecencyStore().RecordOpen("/tmp/learned"))
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Search.Frecency.Disabled = true
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "search.frecency")
	require.Nil(t, a.frecencyStore(), "disabled clears the store")
	require.Nil(t, mgr.Blend(), "the Manager is back on the pre-blend ranking")

	next2 := config.Default()
	next2.Search.Frecency.WeightFrecency = 2.5
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "search.frecency")
	st := a.frecencyStore()
	require.NotNil(t, st)
	require.NotNil(t, mgr.Blend())
	require.InDelta(t, 2.5, mgr.Blend().WeightFrecency, 1e-9, "the new weights reach the blend")
	require.Eventually(t, func() bool { return st.Boost("/tmp/learned") > 0 },
		5*time.Second, 5*time.Millisecond,
		"frecency.json survived the disable/enable cycle (the async load re-reads it)")
}

func TestApplyWindowSizeResizesLive(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550, ResultsWidth: 780})
	a.Startup(context.Background())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Window.Width, next.Window.Height = 900, 600
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "window")
	w, h := a.windowSize()
	require.Equal(t, [2]int{900, 600}, [2]int{w, h}, "the positioning math follows the live size")
	require.True(t, r.has("setWindowSize:900x600"), "the native resize seam is consulted first")
	require.True(t, r.has("setSize:900x600"), "the runtime resize is the fallback when it declines")
	require.Equal(t, 900, a.GetPreviewConfig().ResultsWidth, "the results column follows window.width")

	// Enabling the preview pane widens the window to the preview size
	// while the results column keeps the flag-off width.
	next2 := config.Default()
	next2.Window.Width, next2.Window.Height = 900, 600
	next2.Preview.Enabled = true
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "preview")
	w, h = a.windowSize()
	require.Equal(t, [2]int{next2.Preview.WindowWidth, next2.Preview.WindowHeight}, [2]int{w, h},
		"preview.enabled selects the preview window size, like PreviewWindowSize at boot")
	require.Equal(t, 900, a.GetPreviewConfig().ResultsWidth)
}

func TestApplyWindowSizePreStartupStoresOnly(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	seedBaseline(a, config.Default())
	next := config.Default()
	next.Window.Width = 640
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "window")
	w, _ := a.windowSize()
	require.Equal(t, 640, w)
	require.False(t, r.has("setWindowSize:640x550"), "no native call without a runtime context")
	require.Empty(t, r.callNames(), "no runtime call either")
}

func TestApplyTranslucentIsNextLaunch(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Window.Translucent = true
	res := a.applyConfig(&next, "test")
	require.Equal(t, []string{"window.translucent"}, res.NextLaunch,
		"the ONE ruled next-launch knob is reported by name")
	require.Empty(t, res.Applied, "a translucent-only change applies nothing live")
	require.Empty(t, res.Pending, "and pretends nothing")
	require.Contains(t, buf.String(),
		"config: window.translucent takes effect at next launch (window visual is set at creation)")

	// The word "restart" never appears anywhere in the report path.
	require.NotContains(t, strings.ToLower(buf.String()), "restart required")

	// Flipping it back reports the same honest note; a pass without a
	// translucent change stays silent.
	next2 := config.Default()
	res = a.applyConfig(&next2, "test")
	require.Equal(t, []string{"window.translucent"}, res.NextLaunch)
	res = a.applyConfig(&next2, "test")
	require.Empty(t, res.NextLaunch)
}

func TestSaveConfigReportsNextLaunch(t *testing.T) {
	a, _, _, _ := externalTestApp(t)
	res := a.SaveConfig(`{"window": {"translucent": true}}`)
	require.True(t, res.OK, "error: %s", res.Error)
	require.Equal(t, []string{"window.translucent"}, res.NextLaunch)
	require.Empty(t, res.Pending, "nothing is ever pending a restart")
}

func TestApplyPreviewRebuildsDispatcher(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	require.Nil(t, a.previewDispatcher(), "preview starts disabled")
	require.False(t, a.GetPreviewConfig().Enabled)
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Preview.Enabled = true
	next.Preview.Kagi.APIKey = "k"
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "preview")
	require.NotNil(t, a.previewDispatcher(), "enabling builds the dispatcher")
	info := a.GetPreviewConfig()
	require.True(t, info.Enabled, "GetPreviewConfig answers from the live configuration")
	require.True(t, info.KagiConfigured)
	require.False(t, info.OpenAIConfigured)

	next2 := config.Default()
	res = a.applyConfig(&next2, "test")
	require.Contains(t, res.Applied, "preview")
	require.Nil(t, a.previewDispatcher(), "disabling tears it down")
	require.False(t, a.GetPreviewConfig().Enabled)
}

func TestApplyRootsReindexesLive(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dirA, "alpha-file.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, "bravo-file.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dirA}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return watchUp(a) && len(a.Search("alpha-file")) == 1 },
		20*time.Second, 10*time.Millisecond, "the initial scope is indexed and watched")
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Roots = []string{dirB}
	next.Excludes = nil
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "roots")
	require.Contains(t, res.Applied, "excludes")
	require.Empty(t, res.Errors)
	require.Equal(t, []string{dirB}, m.Roots())
	require.True(t, watchUp(a), "the watch trio is rebuilt")
	require.Eventually(t, func() bool {
		return len(a.Search("bravo-file")) == 1 && len(a.Search("alpha-file")) == 0
	}, 20*time.Second, 10*time.Millisecond, "the background rescan converges the index to the new scope")

	// The rebuilt watcher watches the NEW root live.
	require.NoError(t, os.WriteFile(filepath.Join(dirB, "bravo-live.txt"), []byte("x"), 0o644))
	require.Eventually(t, func() bool { return len(a.Search("bravo-live")) == 1 },
		20*time.Second, 10*time.Millisecond, "live events flow from the new root")
}

func TestApplyExcludesRevivesFailedBuild(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "revived.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, []string{"["}, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Startup(context.Background())
	require.Eventually(t, func() bool {
		a.watchMu.Lock()
		defer a.watchMu.Unlock()
		return a.buildFinished
	}, 20*time.Second, 10*time.Millisecond, "the bad exclude pattern fails the initial build")
	require.False(t, watchUp(a), "the failed build never started the watch layer")
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Roots = []string{dir}
	next.Excludes = nil
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "roots")
	require.Eventually(t, func() bool { return watchUp(a) && len(a.Search("revived")) == 1 },
		20*time.Second, 10*time.Millisecond,
		"fixing the excludes in the editor revives the index without a restart")
}

func TestApplyWatcherKnobsRebuildTheTrio(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond)
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Roots = []string{dir}
	next.Excludes = nil
	next.Watcher.MaxWatches = 7
	next.RescanIntervalMinutes = 45
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "watcher")
	require.Contains(t, res.Applied, "rescanIntervalMinutes")
	require.True(t, watchUp(a))
	out := buf.String()
	require.Contains(t, out, "(budget 7)", "the new maxWatches reaches the rebuilt watcher")
	require.Contains(t, out, "full rescan interval 45m0s", "the new rescan interval reaches the rebuilt rescanner")
}

func TestRestartIndexLayerDuringInitialBuildStoresAndFlags(t *testing.T) {
	// White-box: the trio is down and a build is marked in flight, so
	// the applier must only store the new values and arm the
	// rescan-on-watch-up flag -- never double-build.
	m := index.NewManager([]string{t.TempDir()}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.watchMu.Lock()
	_, cancel := context.WithCancel(context.Background())
	a.buildCancel = cancel
	a.buildFinished = false
	a.watchMu.Unlock()
	t.Cleanup(cancel)

	next := config.Default()
	newRoot := t.TempDir()
	next.Roots = []string{newRoot}
	require.NoError(t, a.restartIndexLayer(&next))
	require.Equal(t, []string{newRoot}, m.Roots(), "the new scope is stored for the in-flight build's completion path")
	a.watchMu.Lock()
	flagged := a.rescanOnWatchUp
	cfg := a.watchCfg
	a.watchMu.Unlock()
	require.True(t, flagged, "one rescan is requested once the watch layer comes up")
	require.Equal(t, watchConfigFrom(&next), cfg)
	require.False(t, watchUp(a), "no trio was built")
}
