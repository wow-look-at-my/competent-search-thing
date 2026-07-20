package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// smallDisplay is a laptop-ish 800x600 screen whose work area loses a
// 20px strip to a panel.
func smallDisplay() []platform.Display {
	return []platform.Display{{
		Rect:    platform.Rect{X: 0, Y: 0, W: 800, H: 600},
		Work:    platform.Rect{X: 0, Y: 20, W: 800, H: 580},
		Primary: true,
	}}
}

// The user's live bug: a window sized past the screen (hand-set
// values, or the preview mount on a small display) must RENDER
// clamped to the display's usable area -- while the configured value
// survives for a bigger monitor later.
func TestSummonClampsToDisplayWorkArea(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 5000, WindowHeight: 5000})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 400, 300
	r.displays = smallDisplay()
	a.Startup(context.Background())

	a.showOnCursorDisplay()

	require.True(t, r.has("setWindowSize:800x580"), "the shown size is clamped to the work area")
	require.True(t, r.has("setSize:800x580"), "runtime fallback carries the same clamp")
	w, h := a.windowSize()
	require.Equal(t, [2]int{5000, 5000}, [2]int{w, h}, "the DESIRED size is never clamped away")
	// BarPosition with the clamped 800x580: x = (800-800)/2 = 0,
	// y = 600/3 - 580/3 clamped into the display -> 7.
	require.Equal(t, []int{0}, r.setPosX)
	require.Equal(t, []int{7}, r.setPosY)
}

func TestSummonReclampsPerDisplay(t *testing.T) {
	big := platform.Display{Rect: platform.Rect{X: 800, Y: 0, W: 2560, H: 1440}, Work: platform.Rect{X: 800, Y: 0, W: 2560, H: 1440}}
	a, r := newTestApp(t, nil, Options{WindowWidth: 5000, WindowHeight: 5000})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 400, 300 // on the small display
	r.displays = append(smallDisplay(), big)
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	require.True(t, r.has("setWindowSize:800x580"))

	// The cursor moves to the big display: the next summon re-clamps
	// against IT -- the window re-grows toward the desired size.
	r.mu.Lock()
	r.cursorX, r.cursorY = 2000, 700
	r.mu.Unlock()
	a.showOnCursorDisplay()
	require.True(t, r.has("setWindowSize:2560x1440"), "re-evaluated per summon: the bigger display re-grows the window")
}

func TestSummonSkipsResizeWhenSizeFits(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 400, 300
	r.displays = testDisplays()
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	a.showOnCursorDisplay()
	for _, c := range r.callNames() {
		require.NotContains(t, c, "setWindowSize", "a fitting size issues no native resize call")
		require.NotContains(t, c, "setSize", "nor a runtime one")
	}
}

func TestApplierClampsPreviewMountGrowth(t *testing.T) {
	// The reported repro: enabling the preview pane on a small screen
	// grew the window past the display. The live applier must clamp
	// the applied size while storing the configured one.
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550, ResultsWidth: 780})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 400, 300
	r.displays = smallDisplay()
	a.Startup(context.Background())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Preview.Enabled = true // 1600x800 preview defaults
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "preview")
	require.True(t, r.has("setWindowSize:800x580"), "the preview growth renders clamped to the screen")
	w, h := a.windowSize()
	require.Equal(t, [2]int{1600, 800}, [2]int{w, h}, "the desired (configured) size is retained")
}

func TestWaylandShowClampsViaWorkAreaProbe(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 5000, WindowHeight: 5000})
	a.plat.detectSession = func() platform.Session { return platform.Session{Kind: platform.SessionWayland} }
	a.plat.windowWorkArea = func() (platform.Rect, bool) {
		return platform.Rect{X: 0, Y: 0, W: 800, H: 600}, true
	}
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	require.True(t, r.has("setWindowSize:800x600"), "wayland clamps through the toolkit work-area probe")
	require.True(t, r.has("center"), "placement stays compositor-owned")
	require.False(t, r.has("setPos"))
}

func TestClampFloorsWinOverTinyDisplay(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 100, 50
	r.displays = []platform.Display{{Rect: platform.Rect{X: 0, Y: 0, W: 200, H: 100}}}
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	require.True(t, r.has("setWindowSize:320x240"),
		"a pathological display never yields a sub-floor window (it overflows instead)")
}

func TestShowWithoutAnyAreaInfoLeavesSizeAlone(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 5000, WindowHeight: 5000})
	a.Startup(context.Background())
	a.showOnCursorDisplay() // cursorOK false, work-area probe false
	require.True(t, r.has("center"))
	for _, c := range r.callNames() {
		require.NotContains(t, c, "setWindowSize", "nothing to clamp against, nothing resized")
	}
}
