package app

import (
	"context"
	"log"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// toggleGap rate-limits the hotkey toggle: X11 and Windows both
// deliver key autorepeat while the combination is held, which would
// otherwise flicker the bar. The same window classifies a toggle that
// arrives just after a Hide as the dismiss press that caused that
// hide through a side channel (grab FocusOut -> frontend blur; see
// toggle) -- it comfortably covers the gsettings backend's process
// spawn + IPC latency while staying below deliberate
// dismiss-then-resummon typing speed.
const toggleGap = 250 * time.Millisecond

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
	a.lastHide = a.plat.now()
	// A hide also cancels a show that has not executed yet (e.g. an
	// IPC hide racing a pre-DomReady summon): the ordered outcome is
	// hidden. A latched summon-into-config dies with it -- and so does
	// an in-flight drag-resize anchor (the next drag re-latches).
	a.pendingShow = false
	a.pendingConfig = false
	a.dragActive = false
	a.dragDispOK = false
	a.dragPosOK = false
	a.mu.Unlock()
	// The stats sampler goes idle with the bar (a flag flip, no IO;
	// runs even pre-Startup, where it is a nil-safe no-op).
	a.statsVisible(false)
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.hide(ctx)
}

// toggle is the global hotkey and IPC "toggle" callback: hide when
// visible, summon onto the cursor's display when hidden. Presses
// within toggleGap of the last accepted one are dropped (key
// autorepeat). Before the frontend is ready (DomReady) the summon is
// deferred: DomReady executes it once the bar can render. On the
// summon path the app context is captured FIRST: showing the bar
// steals focus, so the focused app must be read before the window
// appears.
//
// A toggle that finds the bar hidden BUT hidden only within the last
// toggleGap is dropped, not re-summoned: pressing the combo on an
// open bar can hide it through a side channel before this callback
// even runs -- activating an X11 grab (the app's own XGrabKey, or
// gsd's for a GNOME media-keys binding) delivers FocusOut to the
// focused bar, the frontend's blur handler calls Hide, and on the
// gsettings backend the toggle then arrives a whole process spawn +
// IPC round-trip later ("<exe> toggle"). Branching on the visible
// flag alone turned exactly those dismiss presses into re-summons
// (the bar flickered and stayed open). Treating a just-hidden bar as
// "this press already dismissed it" makes the combo dismiss
// deterministic regardless of which side of the race this callback
// lands on; a summon later than toggleGap after an Esc/blur hide is
// untouched.
func (a *App) toggle() {
	a.mu.Lock()
	now := a.plat.now()
	if now.Sub(a.lastToggle) < toggleGap {
		a.mu.Unlock()
		return
	}
	a.lastToggle = now
	if !a.domReady {
		a.pendingShow = true
		a.mu.Unlock()
		return
	}
	visible := a.visible
	justHidden := now.Sub(a.lastHide) < toggleGap
	a.mu.Unlock()
	if visible {
		a.Hide()
	} else if !justHidden {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
}

// startSpaceWatch arms the dismiss-on-Space-change behavior once, at
// Startup, on platforms with a Space concept (the seam is nil
// elsewhere). Decision (b) of the space-switch ghost fix: Spotlight
// itself dismisses on a Space switch, and the alternative -- fighting
// the compositor for key-window status during the transition
// animation -- is exactly the 1-frame ghost being fixed.
func (a *App) startSpaceWatch() {
	if a.plat.watchSpaceChanges == nil {
		return
	}
	if a.plat.watchSpaceChanges(a.spaceChanged) {
		log.Printf("panel: space-change dismiss armed")
	}
}

// spaceChanged is the active-Space-change callback: a visible bar is
// dismissed through the EXISTING Hide path -- stamping lastHide, so
// the toggle-gap dismiss semantics hold for a summon racing the
// switch -- while a hidden bar is left completely alone (in
// particular the pending-show latch stays untouched; only Hide on a
// VISIBLE bar may clear it).
func (a *App) spaceChanged() {
	a.mu.Lock()
	visible := a.visible
	a.mu.Unlock()
	if visible {
		a.Hide()
	}
}

// showIfHidden is the IPC "show" callback: show-only, never hides.
// Before the frontend is ready the show is deferred to DomReady; when
// the bar is already visible it is just re-shown (raised), without
// re-capturing app context or repositioning; when hidden it takes the
// same capture-context-then-show path toggle uses.
func (a *App) showIfHidden() {
	a.mu.Lock()
	if !a.domReady {
		a.pendingShow = true
		a.mu.Unlock()
		return
	}
	visible := a.visible
	ctx := a.ctx
	a.mu.Unlock()
	if visible {
		if ctx != nil {
			a.rt.show(ctx)
		}
		return
	}
	a.captureAppContext()
	a.showOnCursorDisplay()
}

// showOnCursorDisplay positions the bar on the display the cursor is
// on (falling back to centering when the platform cannot say), marks
// it visible, shows it, and tells the frontend so it can focus the
// input. A no-op before Startup. Every path first clamps the desired
// window size to the hosting display's usable area (clamp-to-screen,
// re-evaluated per summon so multi-monitor moves re-fit -- and
// re-grow -- the window; the config value itself is never touched).
// On a Wayland session the cursor-display positioning is skipped
// entirely: the app is a native Wayland client there, gtk_window_move
// and friends are silent no-ops, and the compositor owns placement --
// centering is requested best-effort and the situation is logged
// once; the clamp still applies, through the toolkit's own work-area
// probe (the one source Wayland has).
func (a *App) showOnCursorDisplay() {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	if a.session().Kind == platform.SessionWayland {
		a.waylandPlaceOnce.Do(func() {
			log.Printf("hotkey: wayland session: window placement is decided by the compositor")
		})
		a.clampForFallbackShow(ctx)
		a.rt.center(ctx)
	} else if !a.positionOnCursorDisplay(ctx) {
		a.clampForFallbackShow(ctx)
		a.rt.center(ctx)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()
	// Wake the stats sampler BEFORE the window maps: the kick's
	// baseline sample is then already in flight while the show
	// happens, and every show path (hotkey toggle, IPC show, the
	// DomReady deferred show) funnels through here.
	a.statsVisible(true)
	a.rt.show(ctx)
	a.emitEvent(eventShown)
}

// clampForFallbackShow applies the clamp-to-screen rule on the show
// paths that have no picked display (the Wayland show and the
// center fallback): the toolkit work-area probe decides, and when
// even that is unavailable the size is left alone -- there is nothing
// to clamp against.
func (a *App) clampForFallbackShow(ctx context.Context) {
	if a.plat.windowWorkArea == nil {
		return
	}
	area, ok := a.plat.windowWorkArea()
	if !ok {
		return
	}
	w, h := a.clampWindowSize(area, true)
	a.applySizeIfChanged(ctx, w, h)
}

// positionOnCursorDisplay implements the positioning flow; false means
// the caller should center instead. The window size is the configured
// (desired) size clamped to the summon display's usable area, resized
// natively when that differs from what the window currently has --
// the clamp-to-screen rule, re-evaluated against the display the bar
// appears on. Wails' WindowSetPosition is
// relative to the monitor the window is CURRENTLY on (verified in the
// v2.13.0 sources, all platforms), so absolute target coordinates are
// translated against that monitor (platform.WailsPosition) on Linux
// and Windows -- WindowGetPosition IS absolute there -- while macOS
// moves the window natively (its Cocoa coordinate flip cannot be
// expressed as a translation). Successful placements are remembered
// (notePlacement) as the drag-resize anchor.
func (a *App) positionOnCursorDisplay(ctx context.Context) bool {
	cx, cy, displays, ok := a.plat.cursorInfo()
	if !ok {
		return false
	}
	target, ok := platform.PickDisplay(displays, cx, cy)
	if !ok {
		return false
	}
	winW, winH := a.clampWindowSize(target.UsableRect(), true)
	a.applySizeIfChanged(ctx, winW, winH)
	x, y := platform.BarPosition(target, winW, winH)
	if a.plat.goos == "darwin" {
		if !a.plat.moveWindow(x, y) {
			return false
		}
		a.notePlacement(x, y)
		return true
	}
	wx, wy := a.rt.getPos(ctx)
	cur, ok := platform.DisplayForWindow(displays, wx, wy, winW, winH)
	if !ok {
		return false
	}
	rx, ry := platform.WailsPosition(a.plat.goos, cur, x, y)
	a.rt.setPos(ctx, rx, ry)
	a.notePlacement(x, y)
	return true
}

// notePlacement records the last absolute top-left position the app
// gave the window -- the drag-resize anchor (reading the position
// back is not an option on darwin, and the app is the only thing
// that ever moves this frameless window).
func (a *App) notePlacement(x, y int) {
	a.mu.Lock()
	a.placedX, a.placedY = x, y
	a.placedOK = true
	a.mu.Unlock()
}
