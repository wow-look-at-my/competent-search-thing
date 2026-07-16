package portal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

func TestTriggerStringFromParsedSpecs(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		// Modifiers, alone and stacked (incl. the super -> LOGO rename).
		{"alt+space", "ALT+space"},
		{"ctrl+alt+space", "CTRL+ALT+space"},
		{"super+space", "LOGO+space"},
		{"cmd+k", "LOGO+k"},
		{"ctrl+shift+a", "CTRL+SHIFT+a"},
		{"shift+f5", "SHIFT+F5"},
		{"ctrl+alt+shift+super+m", "CTRL+ALT+SHIFT+LOGO+m"},
		// Named keys use keysym case.
		{"enter", "Return"},
		{"esc", "Escape"},
		{"tab", "Tab"},
		{"space", "space"},
		{"up", "Up"},
		{"down", "Down"},
		{"left", "Left"},
		{"right", "Right"},
		// Function keys.
		{"f1", "F1"},
		{"f12", "F12"},
		// Letters and digits stay lowercase keysym names.
		{"z", "z"},
		{"7", "7"},
		{"ctrl+0", "CTRL+0"},
	}
	for _, tc := range cases {
		hk, err := platform.ParseHotkey(tc.spec)
		require.NoError(t, err, tc.spec)
		got, err := TriggerString(hk)
		require.NoError(t, err, tc.spec)
		require.Equal(t, tc.want, got, tc.spec)
	}
}

func TestTriggerStringHandBuilt(t *testing.T) {
	// ParseHotkey aliases and stray case/space are tolerated for
	// hand-built Hotkeys.
	got, err := TriggerString(platform.Hotkey{Key: "enter"})
	require.NoError(t, err)
	require.Equal(t, "Return", got)

	got, err = TriggerString(platform.Hotkey{Key: " Space "})
	require.NoError(t, err)
	require.Equal(t, "space", got)

	// Duplicate modifiers collapse.
	got, err = TriggerString(platform.Hotkey{
		Mods: []platform.Mod{platform.ModAlt, platform.ModAlt},
		Key:  "space",
	})
	require.NoError(t, err)
	require.Equal(t, "ALT+space", got)
}

func TestTriggerStringErrors(t *testing.T) {
	_, err := TriggerString(platform.Hotkey{})
	require.Error(t, err, "empty key must not map")

	_, err = TriggerString(platform.Hotkey{Key: "volume_up"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "volume_up")

	_, err = TriggerString(platform.Hotkey{Mods: []platform.Mod{platform.Mod(99)}, Key: "a"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "modifier")
}
