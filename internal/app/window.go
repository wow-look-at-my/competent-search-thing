package app

import (
	"context"
	goruntime "runtime"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform/native"
)

// toggleGap rate-limits the hotkey toggle: X11 and Windows both
// deliver key autorepeat while the combination is held, which would
// otherwise flicker the bar.
const toggleGap = 250 * time.Millisecond

// runtimeSeams carries the Wails runtime calls the App makes. Calling
// any of the real functions without a genuine Wails context aborts the
// process, so every call site first checks runtimeCtx() != nil, and
// unit tests replace the whole struct with fakes.
type runtimeSeams struct {
	show   func(ctx context.Context)
	hide   func(ctx context.Context)
	center func(ctx context.Context)
	setPos func(ctx context.Context, x, y int)
	getPos func(ctx context.Context) (int, int)
	emit   func(ctx context.Context, name string, data ...interface{})
}

func defaultRuntimeSeams() runtimeSeams {
	return runtimeSeams{
		show:   runtime.WindowShow,
		hide:   runtime.WindowHide,
		center: runtime.WindowCenter,
		setPos: runtime.WindowSetPosition,
		getPos: runtime.WindowGetPosition,
		emit:   runtime.EventsEmit,
	}
}

// platformSeams carries the platform-layer hooks (hotkey, displays,
// open/reveal) plus the ambient bits (GOOS, clock) tests pin down.
type platformSeams struct {
	goos        string
	now         func() time.Time
	startHotkey func(hk platform.Hotkey, onDown func()) (stop func(), err error)
	cursorInfo  func() (cx, cy int, ds []platform.Display, ok bool)
	moveWindow  func(x, y int) bool
	open        func(path string) error
	reveal      func(path string) error
}

func defaultPlatformSeams() platformSeams {
	launcher := platform.NewLauncher()
	return platformSeams{
		goos:        goruntime.GOOS,
		now:         time.Now,
		startHotkey: native.StartHotkey,
		cursorInfo:  native.CursorDisplays,
		moveWindow:  native.MoveWindow,
		open:        launcher.Open,
		reveal:      launcher.Reveal,
	}
}

// runtimeCtx returns the Wails context, or nil before Startup.
func (a *App) runtimeCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

// emitEvent forwards an event to the frontend; a no-op before Startup
// (unit tests, early build progress), so emitting is always safe.
func (a *App) emitEvent(name string, payload ...interface{}) {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.emit(ctx, name, payload...)
}

// Hide hides the searchbar window. It is a no-op before Startup has
// run: calling Wails runtime functions without the runtime context
// would abort the process.
func (a *App) Hide() {
	a.mu.Lock()
	a.visible = false
	a.mu.Unlock()
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.hide(ctx)
}

// toggle is the global hotkey callback: hide when visible, summon onto
// the cursor's display when hidden. Presses within toggleGap of the
// last accepted one are dropped (key autorepeat).
func (a *App) toggle() {
	a.mu.Lock()
	now := a.plat.now()
	if now.Sub(a.lastToggle) < toggleGap {
		a.mu.Unlock()
		return
	}
	a.lastToggle = now
	visible := a.visible
	a.mu.Unlock()
	if visible {
		a.Hide()
	} else {
		a.showOnCursorDisplay()
	}
}

// showOnCursorDisplay positions the bar on the display the cursor is
// on (falling back to centering when the platform cannot say), marks
// it visible, shows it, and tells the frontend so it can focus the
// input. A no-op before Startup.
func (a *App) showOnCursorDisplay() {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	if !a.positionOnCursorDisplay(ctx) {
		a.rt.center(ctx)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()
	a.rt.show(ctx)
	a.emitEvent(eventShown)
}

// positionOnCursorDisplay implements the positioning flow; false means
// the caller should center instead. Wails' WindowSetPosition is
// relative to the monitor the window is CURRENTLY on (verified in the
// v2.13.0 sources, all platforms), so absolute target coordinates are
// translated against that monitor (platform.WailsPosition) on Linux
// and Windows -- WindowGetPosition IS absolute there -- while macOS
// moves the window natively (its Cocoa coordinate flip cannot be
// expressed as a translation).
func (a *App) positionOnCursorDisplay(ctx context.Context) bool {
	cx, cy, displays, ok := a.plat.cursorInfo()
	if !ok {
		return false
	}
	target, ok := platform.PickDisplay(displays, cx, cy)
	if !ok {
		return false
	}
	x, y := platform.BarPosition(target, WindowWidth, WindowHeight)
	if a.plat.goos == "darwin" {
		return a.plat.moveWindow(x, y)
	}
	wx, wy := a.rt.getPos(ctx)
	cur, ok := platform.DisplayForWindow(displays, wx, wy, WindowWidth, WindowHeight)
	if !ok {
		return false
	}
	rx, ry := platform.WailsPosition(a.plat.goos, cur, x, y)
	a.rt.setPos(ctx, rx, ry)
	return true
}
