package app

import "github.com/wow-look-at-my/competent-search-thing/internal/config"

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
// boot). It stores the new effective size for the positioning math,
// updates the preview results-column width, and resizes the native
// window: the platform seam first (linux moves the non-resizable
// window's fixed-size floor on the GTK thread; see
// native.SetWindowSize), the Wails runtime call as the fallback
// (sufficient on darwin/windows). A pass before Startup just stores
// -- the construction size wins until then.
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
	if a.plat.setWindowSize != nil && a.plat.setWindowSize(w, h) {
		return nil
	}
	if a.rt.setSize != nil {
		a.rt.setSize(ctx, w, h)
	}
	return nil
}
