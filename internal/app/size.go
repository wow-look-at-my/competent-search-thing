package app

import "github.com/wow-look-at-my/competent-search-thing/internal/config"

// WindowSize reports the configured bar window size (window.width and
// window.height in config.json). main.go must know the answer BEFORE
// wails.Run builds the native window -- the size is fixed at
// construction (DisableResize) -- which is why this is a standalone
// config read like WindowTranslucent. Load repairs missing, zero, or
// too-small values (and returns repaired defaults even on error), so
// the result is always usable; the same two values are wired into
// Options.WindowWidth/WindowHeight so the positioning math agrees with
// the native window.
func WindowSize() (width, height int) {
	cfg, _ := config.Load()
	return cfg.Window.Width, cfg.Window.Height
}

// windowSize returns the bar window's size for the positioning math:
// the Options values, or the config defaults when unset (zero) -- so
// newTestApp and other bare-Options callers keep working without extra
// wiring.
func (a *App) windowSize() (width, height int) {
	width, height = a.opt.WindowWidth, a.opt.WindowHeight
	if width <= 0 {
		width = config.DefaultWindowWidth
	}
	if height <= 0 {
		height = config.DefaultWindowHeight
	}
	return width, height
}
