package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// fakeClock hands out timestamps advancing by step per call, making
// the toggle rate limit deterministic.
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

// testDisplays is a two-monitor layout: primary at the origin (with a
// 40px taskbar strip in Work) and a larger monitor left of it.
func testDisplays() []platform.Display {
	return []platform.Display{
		{Rect: platform.Rect{X: 0, Y: 0, W: 1920, H: 1080}, Work: platform.Rect{X: 0, Y: 40, W: 1920, H: 1040}, Primary: true},
		{Rect: platform.Rect{X: -2560, Y: 0, W: 2560, H: 1440}, Work: platform.Rect{X: -2560, Y: 0, W: 2560, H: 1440}},
	}
}

func TestStartupRegistersHotkeyOnce(t *testing.T) {
	a, r := newTestApp(nil, Options{Hotkey: "alt+space"})
	a.Startup(context.Background())
	a.Startup(context.Background()) // context refresh must not double-register
	require.Equal(t, []string{"startHotkey"}, r.callNames())

	a.Shutdown(context.Background())
	a.Shutdown(context.Background()) // stop exactly once
	require.Equal(t, []string{"startHotkey", "stopHotkey"}, r.callNames())
}

func TestStartupSkipsEmptyHotkey(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.Startup(context.Background())
	require.False(t, r.has("startHotkey"))
}

func TestStartupToleratesBadHotkeySpec(t *testing.T) {
	a, r := newTestApp(nil, Options{Hotkey: "hyper+q"})
	a.Startup(context.Background())
	require.False(t, r.has("startHotkey"), "unparseable spec never reaches registration")
	a.Shutdown(context.Background())
	require.False(t, r.has("stopHotkey"))
}

func TestStartupToleratesRegistrationFailure(t *testing.T) {
	a, r := newTestApp(nil, Options{Hotkey: "alt+space"})
	a.plat.startHotkey = func(platform.Hotkey, func()) (func(), error) {
		return nil, errors.New("no X server")
	}
	a.Startup(context.Background()) // logs and continues
	a.Shutdown(context.Background())
	require.False(t, r.has("stopHotkey"), "nothing to stop after a failed registration")
}

func TestHotkeyToggleShowsThenHides(t *testing.T) {
	a, r := newTestApp(nil, Options{Hotkey: "ctrl+shift+k"})
	a.plat.now = (&fakeClock{step: time.Second}).now
	var onDown func()
	a.plat.startHotkey = func(hk platform.Hotkey, cb func()) (func(), error) {
		require.Equal(t, "ctrl+shift+k", hk.String(), "parsed spec reaches the platform layer")
		onDown = cb
		return func() {}, nil
	}
	a.Startup(context.Background())
	require.NotNil(t, onDown)

	onDown() // hidden -> show (cursor unknown: centers)
	require.True(t, r.has("center"))
	require.True(t, r.has("show"))
	require.Len(t, r.emitted(eventShown), 1)

	onDown() // visible -> hide
	require.True(t, r.has("hide"))
}

func TestToggleRateLimitSwallowsAutorepeat(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	// Every call sees the same (nonzero) instant, so only the first
	// press clears the gap against the zero-valued lastToggle.
	a.plat.now = (&fakeClock{t: time.Unix(1000, 0), step: 0}).now
	a.Startup(context.Background())

	a.toggle()
	a.toggle() // autorepeat within toggleGap: dropped
	a.toggle()
	require.Len(t, r.emitted(eventShown), 1, "only the first press acted")
	require.False(t, r.has("hide"))
}

func TestShowPositionsOnCursorDisplay(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = -1000, 500 // cursor on the left monitor
	r.displays = testDisplays()
	r.winX, r.winY = 100, 100 // window currently on the primary
	a.Startup(context.Background())

	a.showOnCursorDisplay()

	// BarPosition on the left monitor: x = -2560+(2560-680)/2 = -1620,
	// y = 0 + 1440/3 - 460/3 = 327. The window sits on the primary
	// (origin 0,0), so the wails-relative coordinates are identical.
	require.Equal(t, []int{-1620}, r.setPosX)
	require.Equal(t, []int{327}, r.setPosY)
	require.False(t, r.has("center"), "successful positioning skips centering")
	require.True(t, r.has("show"))
	require.Len(t, r.emitted(eventShown), 1)
}

func TestShowTranslatesAgainstCurrentMonitor(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540 // cursor on the primary
	r.displays = testDisplays()
	r.winX, r.winY = -2000, 200 // window currently on the LEFT monitor
	a.Startup(context.Background())

	a.showOnCursorDisplay()

	// Absolute target on the primary: x = (1920-680)/2 = 620,
	// y = 1080/3 - 460/3 = 207. WindowSetPosition is relative to the
	// window's current monitor (origin -2560,0), so x becomes
	// 620 - (-2560) = 3180.
	require.Equal(t, []int{3180}, r.setPosX)
	require.Equal(t, []int{207}, r.setPosY)
}

func TestShowUsesWorkAreaOriginOnWindows(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.goos = "windows"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540
	r.displays = testDisplays()
	r.winX, r.winY = 100, 100 // on the primary, whose work area starts at y=40
	a.Startup(context.Background())

	a.showOnCursorDisplay()

	require.Equal(t, []int{620}, r.setPosX)
	require.Equal(t, []int{207 - 40}, r.setPosY, "windows translates against rcWork")
}

func TestShowCentersWhenCursorUnknown(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.Startup(context.Background())
	a.showOnCursorDisplay() // cursorOK defaults to false

	require.True(t, r.has("center"))
	require.False(t, r.has("setPos"))
	require.True(t, r.has("show"), "the bar still appears, centered")
	require.Len(t, r.emitted(eventShown), 1)
}

func TestShowCentersOnEmptyDisplayList(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	r.cursorOK = true // cursor known but no display data
	a.Startup(context.Background())
	a.showOnCursorDisplay()
	require.True(t, r.has("center"))
	require.False(t, r.has("setPos"))
}

func TestShowDarwinMovesNatively(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.goos = "darwin"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540
	r.displays = testDisplays()
	r.moveOK = true
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	require.True(t, r.has("moveWindow"))
	require.False(t, r.has("setPos"), "darwin never uses WindowSetPosition")
	require.False(t, r.has("center"))

	// A failed native move falls back to centering.
	r.moveOK = false
	a.showOnCursorDisplay()
	require.True(t, r.has("center"))
}

func TestShowBeforeStartupIsNoOp(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.showOnCursorDisplay()
	require.Empty(t, r.callNames())
	require.Empty(t, r.emits)
}

func TestHideTracksVisibility(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	a.Hide() // e.g. the frontend reacting to Escape or blur
	a.toggle()
	require.Len(t, r.emitted(eventShown), 2, "after Hide, toggle shows again instead of hiding")
}
