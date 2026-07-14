//go:build darwin

package native

import (
	"golang.design/x/hotkey"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// macMods maps neutral modifiers to Carbon modifier masks.
var macMods = map[platform.Mod]hotkey.Modifier{
	platform.ModCtrl:  hotkey.ModCtrl,
	platform.ModShift: hotkey.ModShift,
	platform.ModAlt:   hotkey.ModOption,
	platform.ModSuper: hotkey.ModCmd,
}

// macKeys maps canonical key tokens to Carbon virtual keycodes
// (kVK_ANSI_*, HIToolbox Events.h). The letter and digit codes follow
// the ANSI keyboard LAYOUT, not the alphabet, so the table is spelled
// out explicitly using the library's constants.
var macKeys = map[string]hotkey.Key{
	"space": hotkey.KeySpace, "tab": hotkey.KeyTab,
	"return": hotkey.KeyReturn, "escape": hotkey.KeyEscape,
	"left": hotkey.KeyLeft, "up": hotkey.KeyUp,
	"right": hotkey.KeyRight, "down": hotkey.KeyDown,
	"a": hotkey.KeyA, "b": hotkey.KeyB, "c": hotkey.KeyC, "d": hotkey.KeyD,
	"e": hotkey.KeyE, "f": hotkey.KeyF, "g": hotkey.KeyG, "h": hotkey.KeyH,
	"i": hotkey.KeyI, "j": hotkey.KeyJ, "k": hotkey.KeyK, "l": hotkey.KeyL,
	"m": hotkey.KeyM, "n": hotkey.KeyN, "o": hotkey.KeyO, "p": hotkey.KeyP,
	"q": hotkey.KeyQ, "r": hotkey.KeyR, "s": hotkey.KeyS, "t": hotkey.KeyT,
	"u": hotkey.KeyU, "v": hotkey.KeyV, "w": hotkey.KeyW, "x": hotkey.KeyX,
	"y": hotkey.KeyY, "z": hotkey.KeyZ,
	"0": hotkey.Key0, "1": hotkey.Key1, "2": hotkey.Key2, "3": hotkey.Key3,
	"4": hotkey.Key4, "5": hotkey.Key5, "6": hotkey.Key6, "7": hotkey.Key7,
	"8": hotkey.Key8, "9": hotkey.Key9,
	"f1": hotkey.KeyF1, "f2": hotkey.KeyF2, "f3": hotkey.KeyF3,
	"f4": hotkey.KeyF4, "f5": hotkey.KeyF5, "f6": hotkey.KeyF6,
	"f7": hotkey.KeyF7, "f8": hotkey.KeyF8, "f9": hotkey.KeyF9,
	"f10": hotkey.KeyF10, "f11": hotkey.KeyF11, "f12": hotkey.KeyF12,
}

// StartHotkey registers hk as a global macOS hotkey and calls onDown on
// every press. The library delivers events through a CGEventTap wired
// to the main run loop (which Wails runs), so registering from a
// goroutine is fine once the app has started; the process must be
// trusted for Accessibility or Register returns an error, which the
// caller logs -- the app keeps running without a hotkey. Untested in
// CI (linux/amd64 only).
func StartHotkey(hk platform.Hotkey, onDown func()) (func(), error) {
	mods, key, err := mapSpec(hk, macMods, macKeys)
	if err != nil {
		return nil, err
	}
	return startLibHotkey(mods, key, onDown)
}
