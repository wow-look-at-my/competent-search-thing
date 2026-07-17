package app

import "github.com/wow-look-at-my/competent-search-thing/internal/config"

// WindowTranslucent reports whether config.json opts into the
// per-pixel-alpha window (window.translucent). main.go must know the
// answer BEFORE wails.Run builds the native window -- the RGBA visual
// and the zero-alpha background can only be requested at construction
// -- which is why this is a standalone config read instead of App
// state. Any config error means the safe default: an opaque window,
// exactly the pre-flag behavior.
func WindowTranslucent() bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.Window.Translucent
}
