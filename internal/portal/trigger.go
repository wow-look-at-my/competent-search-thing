package portal

import (
	"fmt"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// modNames maps the repo's OS-neutral modifiers to the shortcuts-spec
// modifier identifiers: the XKB_MOD_NAME_* names from xkbcommon,
// uppercase, with LOGO standing for the Super/Windows key.
var modNames = map[platform.Mod]string{
	platform.ModCtrl:  "CTRL",
	platform.ModShift: "SHIFT",
	platform.ModAlt:   "ALT",
	platform.ModSuper: "LOGO",
}

// keysymNames maps platform.ParseHotkey's canonical key tokens (plus
// its enter/esc input aliases, for hand-built Hotkeys) to xkbcommon
// keysym identifiers -- xkbcommon-keysyms.h names without the XKB_KEY_
// prefix, in keysym case: letters/digits/"space" stay lowercase,
// named keys are capitalized ("Return", "Escape", "Tab", "Up", "F1").
var keysymNames = buildKeysymNames()

func buildKeysymNames() map[string]string {
	m := map[string]string{
		"space":  "space",
		"tab":    "Tab",
		"return": "Return",
		"enter":  "Return",
		"escape": "Escape",
		"esc":    "Escape",
		"up":     "Up",
		"down":   "Down",
		"left":   "Left",
		"right":  "Right",
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = string(c)
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = string(c)
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("f%d", i)] = fmt.Sprintf("F%d", i)
	}
	return m
}

// TriggerString converts an OS-neutral platform.Hotkey into the
// preferred_trigger syntax of the XDG shortcuts spec: uppercase
// modifiers and a keysym name joined by "+", e.g. alt+space ->
// "ALT+space", ctrl+alt+space -> "CTRL+ALT+space", super+k ->
// "LOGO+k", enter -> "Return". The spec documents no case folding or
// modifier ordering guarantees, so the canonical form is emitted:
// modifiers in Hotkey.Mods order, deduplicated. Anything without a
// spec mapping is an error naming the offender.
func TriggerString(hk platform.Hotkey) (string, error) {
	key, ok := keysymNames[strings.ToLower(strings.TrimSpace(hk.Key))]
	if !ok {
		return "", fmt.Errorf("portal: hotkey key %q has no shortcuts-spec keysym name", hk.Key)
	}
	parts := make([]string, 0, len(hk.Mods)+1)
	seen := make(map[platform.Mod]bool, len(hk.Mods))
	for _, m := range hk.Mods {
		if seen[m] {
			continue
		}
		seen[m] = true
		name, ok := modNames[m]
		if !ok {
			return "", fmt.Errorf("portal: hotkey modifier %v has no shortcuts-spec name", m)
		}
		parts = append(parts, name)
	}
	return strings.Join(append(parts, key), "+"), nil
}
