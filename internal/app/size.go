package app

import (
	"context"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// WindowSize reports the configured bar window size (window.width and
// window.height in config.json). main.go must know the answer BEFORE
// wails.Run builds the native window (the construction size) -- which
// is why this is a standalone config read like WindowTranslucent. Load
// repairs missing, zero, or too-small values (and returns repaired
// defaults even on error), so the result is always usable; the same
// two values are wired into Options.WindowWidth/WindowHeight, seeding
// the App's LIVE size state so the positioning math agrees with the
// native window (the config window-size applier moves both together
// at runtime).
func WindowSize() (width, height int) {
	cfg, _ := config.Load()
	return cfg.Window.Width, cfg.Window.Height
}

// windowSize returns the bar window's size for the positioning math:
// the live values (seeded from Options, swapped by the window-size
// applier), or the config defaults when unset (zero) -- so newTestApp
// and other bare-Options callers keep working without extra wiring.
func (a *App) windowSize() (width, height int) {
	a.mu.Lock()
	width, height = a.winW, a.winH
	a.mu.Unlock()
	if width <= 0 {
		width = config.DefaultWindowWidth
	}
	if height <= 0 {
		height = config.DefaultWindowHeight
	}
	return width, height
}

// resultsWidth returns the live window.width GetPreviewConfig reports
// as the preview pane's results-column width, with the same zero
// fallback as windowSize.
func (a *App) resultsWidth() int {
	a.mu.Lock()
	rw := a.resultsW
	a.mu.Unlock()
	if rw <= 0 {
		rw = config.DefaultWindowWidth
	}
	return rw
}

// applyWindowSize is the config live-apply path for the bar window's
// size (the "window-size" group: the window row and the preview row
// both feed it, because preview.enabled widens the window to
// preview.windowWidth/Height exactly like PreviewWindowSize does at
// boot). It stores the new DESIRED size for the positioning math,
// updates the preview results-column width, and resizes the native
// window to the desired size clamped to the current display's usable
// area (clamp-to-screen: what is SHOWN always fits the screen, while
// the config keeps the hand-set value for a bigger monitor later).
// A pass before Startup just stores -- the construction size wins
// until then.
func (a *App) applyWindowSize(next *config.Config) error {
	w, h := next.Window.Width, next.Window.Height
	if next.Preview.Enabled {
		w, h = next.Preview.WindowWidth, next.Preview.WindowHeight
	}
	a.mu.Lock()
	a.winW, a.winH = w, h
	a.resultsW = next.Window.Width
	a.mu.Unlock()
	ctx := a.runtimeCtx()
	if ctx == nil {
		return nil
	}
	area, ok := a.currentDisplayArea(ctx)
	ew, eh := a.clampWindowSize(area, ok)
	a.applySizeIfChanged(ctx, ew, eh)
	return nil
}

// clampWindowSize returns the desired window size limited to area
// (when known) and to the config floors -- the one clamp rule every
// sizing path shares.
func (a *App) clampWindowSize(area platform.Rect, ok bool) (int, int) {
	w, h := a.windowSize()
	if !ok {
		return w, h
	}
	return platform.ClampSize(area, w, h, config.MinWindowWidth, config.MinWindowHeight)
}

// applySizeIfChanged resizes the native window to w x h unless that
// is already the applied size: the platform seam first (linux moves
// the non-resizable window's fixed-size floor on the GTK thread; see
// native.SetWindowSize), the Wails runtime call as the fallback
// (sufficient on darwin/windows). The dedup keeps summons -- which
// re-clamp on every show -- from issuing redundant native calls, and
// bounds a drag's per-frame cost to real changes.
func (a *App) applySizeIfChanged(ctx context.Context, w, h int) {
	a.mu.Lock()
	if a.appliedW == w && a.appliedH == h {
		a.mu.Unlock()
		return
	}
	a.appliedW, a.appliedH = w, h
	a.mu.Unlock()
	if a.plat.setWindowSize != nil && a.plat.setWindowSize(w, h) {
		return
	}
	if ctx != nil && a.rt.setSize != nil {
		a.rt.setSize(ctx, w, h)
	}
}

// appliedSize reports the size the native window currently has (the
// last clamped size applySizeIfChanged issued, or the construction
// size).
func (a *App) appliedSize() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.appliedW, a.appliedH
}

// currentDisplayArea resolves the usable area of the display the bar
// window is on right now, for the sizing paths that have no freshly
// picked summon display (the config applier, the drag): the display
// list when the platform has one (never probed on Wayland, where it
// is misleading), else the toolkit's own window work-area probe --
// the one source Wayland has. ok=false means "unknown, skip
// clamping".
func (a *App) currentDisplayArea(ctx context.Context) (platform.Rect, bool) {
	if a.session().Kind != platform.SessionWayland && a.plat.cursorInfo != nil {
		if cx, cy, displays, ok := a.plat.cursorInfo(); ok && len(displays) > 0 {
			// The display hosting the window wins; darwin cannot read
			// the window position back, so the cursor display decides
			// there (summons place the bar on it anyway).
			if a.plat.goos != "darwin" && ctx != nil && a.rt.getPos != nil {
				wx, wy := a.rt.getPos(ctx)
				w, h := a.appliedSize()
				if d, ok2 := platform.DisplayForWindow(displays, wx, wy, w, h); ok2 {
					return d.UsableRect(), true
				}
			}
			if d, ok2 := platform.PickDisplay(displays, cx, cy); ok2 {
				return d.UsableRect(), true
			}
		}
	}
	if a.plat.windowWorkArea != nil {
		if r, ok := a.plat.windowWorkArea(); ok {
			return r, true
		}
	}
	return platform.Rect{}, false
}
