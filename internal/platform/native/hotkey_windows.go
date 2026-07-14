//go:build windows

package native

import (
	"fmt"

	"golang.design/x/hotkey"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// winMods maps neutral modifiers to RegisterHotKey MOD_* values.
var winMods = map[platform.Mod]hotkey.Modifier{
	platform.ModCtrl:  hotkey.ModCtrl,
	platform.ModShift: hotkey.ModShift,
	platform.ModAlt:   hotkey.ModAlt,
	platform.ModSuper: hotkey.ModWin,
}

// winKeys maps canonical key tokens to Windows virtual-key codes. The
// codes are regular (letters VK_A.., digits VK_0.., function keys
// VK_F1..), so the table is generated arithmetically.
var winKeys = buildWinKeys()

func buildWinKeys() map[string]hotkey.Key {
	m := map[string]hotkey.Key{
		"space":  hotkey.KeySpace,  // VK_SPACE
		"tab":    hotkey.KeyTab,    // VK_TAB
		"return": hotkey.KeyReturn, // VK_RETURN
		"escape": hotkey.KeyEscape, // VK_ESCAPE
		"left":   hotkey.KeyLeft,
		"up":     hotkey.KeyUp,
		"right":  hotkey.KeyRight,
		"down":   hotkey.KeyDown,
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = hotkey.KeyA + hotkey.Key(c-'a') // VK_A .. VK_Z
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = hotkey.Key0 + hotkey.Key(c-'0') // VK_0 .. VK_9
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = hotkey.KeyF1 + hotkey.Key(i-1)
	}
	return m
}

// StartHotkey registers hk as a global Windows hotkey (RegisterHotKey)
// and calls onDown on every press. The returned stop function
// unregisters it.
func StartHotkey(hk platform.Hotkey, onDown func()) (func(), error) {
	mods, key, err := mapSpec(hk, winMods, winKeys)
	if err != nil {
		return nil, err
	}
	return startLibHotkey(mods, key, onDown)
}
