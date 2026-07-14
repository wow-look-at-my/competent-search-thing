package platform

import (
	"errors"
	"fmt"
	"strings"
)

// Mod is an OS-neutral hotkey modifier. The native subpackage maps it
// to the platform constant (X11 ControlMask/Mod1/Mod4, Win32
// MOD_CONTROL/MOD_ALT/MOD_WIN, Carbon controlKey/optionKey/cmdKey).
type Mod uint8

// The four modifiers every supported platform has.
const (
	ModCtrl Mod = iota
	ModShift
	ModAlt
	ModSuper
)

// String returns the canonical spelling used in config strings.
func (m Mod) String() string {
	switch m {
	case ModCtrl:
		return "ctrl"
	case ModShift:
		return "shift"
	case ModAlt:
		return "alt"
	case ModSuper:
		return "super"
	}
	return fmt.Sprintf("mod(%d)", uint8(m))
}

// Hotkey is a parsed, OS-neutral hotkey: zero or more modifiers plus
// one key, identified by its canonical token (see ParseHotkey).
type Hotkey struct {
	Mods []Mod
	Key  string
}

// String renders the canonical "mod+mod+key" form.
func (h Hotkey) String() string {
	parts := make([]string, 0, len(h.Mods)+1)
	for _, m := range h.Mods {
		parts = append(parts, m.String())
	}
	return strings.Join(append(parts, h.Key), "+")
}

// modAliases maps accepted modifier spellings to modifiers.
var modAliases = map[string]Mod{
	"ctrl":    ModCtrl,
	"control": ModCtrl,
	"shift":   ModShift,
	"alt":     ModAlt,
	"option":  ModAlt,
	"super":   ModSuper,
	"win":     ModSuper,
	"cmd":     ModSuper,
	"meta":    ModSuper,
}

// keyAliases maps accepted key spellings to canonical key tokens:
// "space", "tab", "return", "escape", "a".."z", "0".."9", "f1".."f12",
// "up", "down", "left", "right".
var keyAliases = buildKeyAliases()

func buildKeyAliases() map[string]string {
	m := map[string]string{
		"space":  "space",
		"tab":    "tab",
		"enter":  "return",
		"return": "return",
		"esc":    "escape",
		"escape": "escape",
		"up":     "up",
		"down":   "down",
		"left":   "left",
		"right":  "right",
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = string(c)
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = string(c)
	}
	for i := 1; i <= 12; i++ {
		f := fmt.Sprintf("f%d", i)
		m[f] = f
	}
	return m
}

// ParseHotkey parses a config hotkey string like "alt+space",
// "ctrl+shift+k" or "cmd + Space" into an OS-neutral Hotkey. Parsing is
// case-insensitive and ignores whitespace; tokens are separated by "+".
// Every token but the last must be a modifier (ctrl/control, shift,
// alt/option, super/win/cmd/meta); the last must be a key (space, tab,
// enter/return, esc/escape, a-z, 0-9, f1-f12, up/down/left/right).
// Repeated modifiers are deduplicated. Unknown tokens are an error that
// names the offender.
func ParseHotkey(s string) (Hotkey, error) {
	norm := strings.ToLower(strings.Join(strings.Fields(s), ""))
	if norm == "" {
		return Hotkey{}, errors.New("hotkey: empty spec")
	}
	parts := strings.Split(norm, "+")
	var hk Hotkey
	seen := map[Mod]bool{}
	for i, p := range parts {
		if p == "" {
			return Hotkey{}, fmt.Errorf("hotkey: empty token in %q", s)
		}
		if i == len(parts)-1 {
			key, ok := keyAliases[p]
			if !ok {
				return Hotkey{}, fmt.Errorf("hotkey: unknown key %q in %q", p, s)
			}
			hk.Key = key
			break
		}
		mod, ok := modAliases[p]
		if !ok {
			return Hotkey{}, fmt.Errorf("hotkey: unknown modifier %q in %q", p, s)
		}
		if !seen[mod] {
			seen[mod] = true
			hk.Mods = append(hk.Mods, mod)
		}
	}
	return hk, nil
}
