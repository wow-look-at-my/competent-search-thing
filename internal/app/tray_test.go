package app

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/tray"
)

// fakeTrayHandle records the App's tray lifecycle calls.
type fakeTrayHandle struct {
	mu       sync.Mutex
	startCtx context.Context
	starts   int
	closes   int
}

func (f *fakeTrayHandle) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCtx = ctx
	f.starts++
	return nil
}

func (f *fakeTrayHandle) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

func (f *fakeTrayHandle) snapshot() (starts, closes int, ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.closes, f.startCtx
}

func TestStartupStartsTrayOnLinuxAndShutdownClosesIt(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "linux"
	f := &fakeTrayHandle{}
	a.newTray = func() trayHandle { return f }

	a.Startup(context.Background())
	require.Eventually(t, func() bool {
		starts, _, _ := f.snapshot()
		return starts == 1
	}, 5*time.Second, time.Millisecond, "the tray starts asynchronously")

	// A second Startup never doubles the tray.
	a.Startup(context.Background())
	starts, _, startCtx := f.snapshot()
	require.Equal(t, 1, starts)
	require.NoError(t, startCtx.Err(), "the start context is live while the app runs")

	a.Shutdown(context.Background())
	_, closes, _ := f.snapshot()
	require.Equal(t, 1, closes, "Shutdown closes the tray")
	require.Error(t, startCtx.Err(), "Shutdown cancels a Start still waiting on the bus")
	a.Shutdown(context.Background())
	_, closes, _ = f.snapshot()
	require.Equal(t, 1, closes, "a second Shutdown finds no tray handle")
}

func TestTraySkippedWhenConfigDisabled(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{TrayDisabled: true})
	a.plat.goos = "linux"
	built := false
	a.newTray = func() trayHandle { built = true; return &fakeTrayHandle{} }

	a.Startup(context.Background())
	require.False(t, built, "tray.disabled skips the tray entirely")
}

func TestTraySkippedOffLinux(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		a, _ := newTestApp(t, nil, Options{})
		a.plat.goos = goos
		built := false
		a.newTray = func() trayHandle { built = true; return &fakeTrayHandle{} }
		a.Startup(context.Background())
		require.False(t, built, "no tray on %s (GNOME-first feature)", goos)
		a.Shutdown(context.Background())
	}
}

func TestBuildTrayReturnsRealTray(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	h := a.buildTray()
	require.NotNil(t, h)
	_, ok := h.(*tray.Tray)
	require.True(t, ok, "production seam value wraps internal/tray")
	require.NoError(t, h.Close(), "closing an unstarted tray is safe")
}

// TestTrayMenuShape pins the menu contract: labels, order, the
// separator, and identity fields.
func TestTrayMenuShape(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	opts := a.trayOptions()

	require.Equal(t, "competent-search-thing", opts.ID)
	require.Equal(t, "Competent Search", opts.Title)
	require.NotNil(t, opts.OnActivate)

	var labels []string
	for _, it := range opts.Menu {
		if it.Separator {
			labels = append(labels, "---")
			require.Nil(t, it.OnClick)
			continue
		}
		labels = append(labels, it.Label)
		require.NotNil(t, it.OnClick, "%s is clickable", it.Label)
	}
	require.Equal(t, []string{"Show/Hide", "Rescan now", "Open config", "---", "Quit"}, labels)
}

// trayMenuClick runs the OnClick of the menu item with the given
// label.
func trayMenuClick(t *testing.T, opts tray.Options, label string) {
	t.Helper()
	for _, it := range opts.Menu {
		if it.Label == label && !it.Separator {
			it.OnClick()
			return
		}
	}
	t.Fatalf("no menu item labeled %q", label)
}

func TestTrayShowHideTogglesTheBar(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.now = (&fakeClock{t: time.Unix(1000, 0), step: time.Second}).now
	a.Startup(context.Background())
	a.DomReady(context.Background())
	opts := a.trayOptions()

	trayMenuClick(t, opts, "Show/Hide")
	require.Len(t, r.emitted(eventShown), 1, "hidden bar: the toggle path shows it")

	trayMenuClick(t, opts, "Show/Hide")
	require.True(t, r.has("hide"), "visible bar: the toggle path hides it")

	// The icon activation (double/middle click) is the same toggle.
	opts.OnActivate()
	require.Len(t, r.emitted(eventShown), 2)
}

func TestTrayShowBeforeDomReadyIsDeferred(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.now = (&fakeClock{t: time.Unix(1000, 0), step: time.Second}).now
	a.Startup(context.Background())
	opts := a.trayOptions()

	trayMenuClick(t, opts, "Show/Hide")
	require.Empty(t, r.emitted(eventShown), "nothing renders before DomReady")

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "the deferred summon runs once the frontend is ready")
}

func TestTrayRescanWhileIndexStillBuilding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	opts := a.trayOptions()

	// No rescanner yet: the friendly error is logged, nothing hides,
	// nothing crashes.
	trayMenuClick(t, opts, "Rescan now")
	require.False(t, r.has("hide"), "tray rescan never touches bar visibility")
}

func TestTrayOpenConfigOpensWithoutHiding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	a.Startup(context.Background())
	a.DomReady(context.Background())
	opts := a.trayOptions()

	trayMenuClick(t, opts, "Open config")
	require.True(t, r.has("open:"+filepath.Join(dir, "config.json")))
	require.False(t, r.has("hide"), "tray open-config never hides the bar")
}

func TestTrayQuitUsesTheQuitSeam(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	opts := a.trayOptions()

	// Before Startup the quit builtin refuses (no runtime ctx); the
	// callback logs instead of crashing.
	trayMenuClick(t, opts, "Quit")
	require.False(t, r.has("quit"))

	a.Startup(context.Background())
	trayMenuClick(t, opts, "Quit")
	require.True(t, r.has("quit"))
}

func TestTrayTooltipReflectsHotkeyDescription(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	opts := a.trayOptions()

	require.Empty(t, opts.Tooltip(), "no proven summon path: no tooltip promise")

	a.mu.Lock()
	a.hotkeyDesc = "alt+space"
	a.mu.Unlock()
	require.Equal(t, "alt+space summons the searchbar", opts.Tooltip())
}

func TestRequestRescanErrorsWithoutRescanner(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.ErrorContains(t, a.requestRescan(), "index is still building",
		"the message points at the still-running build")
}
