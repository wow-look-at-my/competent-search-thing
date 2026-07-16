package gsettings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// accelMods maps the repo's OS-neutral modifiers to GTK accelerator
// modifier tags (the syntax gtk_accelerator_parse reads and GNOME's
// own keybinding defaults use).
var accelMods = map[platform.Mod]string{
	platform.ModCtrl:  "<Control>",
	platform.ModShift: "<Shift>",
	platform.ModAlt:   "<Alt>",
	platform.ModSuper: "<Super>",
}

// accelKeys maps platform.ParseHotkey's canonical key tokens (plus the
// enter/esc input aliases, for hand-built Hotkeys) to GDK key names as
// gdk_keyval_from_name wants them: character keys by their lowercase
// name ("space", "a", "0"), named keys capitalized ("Return",
// "Escape", "Tab", "Up", "F1").
var accelKeys = buildAccelKeys()

func buildAccelKeys() map[string]string {
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

// ConvertHotkey converts an OS-neutral platform.Hotkey into a GNOME
// (GTK) accelerator string: modifier tags in Hotkey.Mods order,
// deduplicated, followed by the GDK key name. Examples: alt+space ->
// "<Alt>space", ctrl+alt+space -> "<Control><Alt>space", super+k ->
// "<Super>k", f5 -> "F5". Anything without a GNOME mapping is an
// error naming the offender.
func ConvertHotkey(hk platform.Hotkey) (string, error) {
	key, ok := accelKeys[strings.ToLower(strings.TrimSpace(hk.Key))]
	if !ok {
		return "", fmt.Errorf("gsettings: hotkey key %q has no GNOME accelerator name", hk.Key)
	}
	var b strings.Builder
	seen := make(map[platform.Mod]bool, len(hk.Mods))
	for _, m := range hk.Mods {
		if seen[m] {
			continue
		}
		seen[m] = true
		tag, ok := accelMods[m]
		if !ok {
			return "", fmt.Errorf("gsettings: hotkey modifier %v has no GNOME accelerator name", m)
		}
		b.WriteString(tag)
	}
	b.WriteString(key)
	return b.String(), nil
}

// normalizeAccel reduces a GNOME accelerator string to a canonical
// comparable form for conflict detection: <Primary>/<Ctrl>/<Ctl> and
// <Control> all mean control, modifier order is irrelevant, modifier
// and key names compare case-insensitively (gtk_accelerator_parse is
// documented as liberal about all three). Unknown modifier tags are
// kept as distinct set members rather than dropped, so "<Meta>space"
// never collides with "<Alt>space". The empty string is returned for
// anything that is not a plausible accelerator (empty, unterminated
// "<...", no key part) -- callers skip those.
func normalizeAccel(s string) string {
	s = strings.TrimSpace(s)
	var mods []string
	for strings.HasPrefix(s, "<") {
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return "" // malformed: unterminated modifier tag
		}
		name := strings.ToLower(s[1:end])
		s = s[end+1:]
		switch name {
		case "primary", "ctrl", "ctl":
			name = "control"
		}
		mods = append(mods, name)
	}
	key := strings.ToLower(strings.TrimSpace(s))
	if key == "" {
		return ""
	}
	sort.Strings(mods)
	return strings.Join(append(mods, key), "+")
}

// sameAccel reports whether two accelerator strings denote the same
// combination under normalizeAccel; unparseable inputs are equal to
// nothing.
func sameAccel(a, b string) bool {
	na, nb := normalizeAccel(a), normalizeAccel(b)
	return na != "" && na == nb
}
