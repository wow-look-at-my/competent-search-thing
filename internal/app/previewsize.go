package app

import "github.com/wow-look-at-my/competent-search-thing/internal/config"

// PreviewWindowSize reports the window size the preview pane asks
// for. main.go must know it BEFORE wails.Run builds the native window
// (the size is fixed for the process lifetime), which is why this is
// a standalone config read like WindowTranslucent. enabled false --
// the pane off, or any config error -- means the configured base size
// (window.width/height; the WindowSize read, defaults 780x550). The
// returned dimensions are always positive (config.Load normalizes
// both the window and preview sections, even on error).
func PreviewWindowSize() (w, h int, enabled bool) {
	cfg, err := config.Load()
	if err != nil || !cfg.Preview.Enabled {
		return cfg.Window.Width, cfg.Window.Height, false
	}
	return cfg.Preview.WindowWidth, cfg.Preview.WindowHeight, true
}
