package app

import (
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// appearanceVibrantDark is the dark vibrant NSAppearance name. Wails
// v2.13.0's mac options package defines a constant only for the LIGHT
// vibrant appearance; AppearanceType is a plain string handed
// verbatim to [NSAppearance appearanceNamed:] (WailsContext.m
// CreateWindow, verified in the pinned sources), and
// NSAppearanceNameVibrantDark is a valid AppKit appearance name
// (macOS 10.10+), so the literal fills the gap.
const appearanceVibrantDark = mac.AppearanceType("NSAppearanceNameVibrantDark")

// MacWindowOptions returns the wails Mac options main.go wires when
// config window.translucent opts in: the real macOS frosted glass.
// Wails v2.13.0 renders it as an NSVisualEffectView with
// BehindWindow blending under the webview (WindowIsTranslucent) plus
// drawsBackground=NO on the WKWebView (WebviewIsTransparent) -- the
// Spotlight look; there is no raw desktop passthrough path in wails
// (it never calls setOpaque:NO), and Spotlight itself is vibrancy
// anyway. The Appearance tracks the configured theme so the material
// matches the palette. Flag off -- or any config error -- returns
// nil: the exact pre-flag nil-Mac wails.Run call (the safe default).
// Standalone config read (the translucent.go pattern) because the
// answer is needed BEFORE wails.Run; options.Mac is read ONLY by the
// darwin frontend (verified v2.13.0), so main.go needs no GOOS
// branch and linux behavior cannot change.
func MacWindowOptions() *mac.Options {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return macWindowOptionsFor(cfg.Window.Translucent, cfg.Theme)
}

// macWindowOptionsFor is the pure decision half (headless-tested on
// both CI jobs): nil unless translucent; the light builtin theme gets
// the vibrant light material, everything else -- dark, custom themes
// (which extend dark by default), unknown names (resolution falls
// back to dark) -- the dark one.
func macWindowOptionsFor(translucent bool, themeName string) *mac.Options {
	if !translucent {
		return nil
	}
	appearance := appearanceVibrantDark
	if themeName == "light" {
		appearance = mac.NSAppearanceNameVibrantLight
	}
	return &mac.Options{
		WindowIsTranslucent:  true,
		WebviewIsTransparent: true,
		Appearance:           appearance,
	}
}
