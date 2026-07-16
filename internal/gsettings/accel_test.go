package gsettings

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

func mustParse(t *testing.T, spec string) platform.Hotkey {
	t.Helper()
	hk, err := platform.ParseHotkey(spec)
	require.NoError(t, err)
	return hk
}

func TestConvertHotkey(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"alt+space", "<Alt>space"},
		{"ctrl+alt+space", "<Control><Alt>space"},
		{"super+space", "<Super>space"},
		{"ctrl+shift+k", "<Control><Shift>k"},
		{"win+7", "<Super>7"},
		{"f5", "F5"},
		{"ctrl+f12", "<Control>F12"},
		{"alt+enter", "<Alt>Return"},
		{"alt+return", "<Alt>Return"},
		{"esc", "Escape"},
		{"escape", "Escape"},
		{"tab", "Tab"},
		{"up", "Up"},
		{"shift+down", "<Shift>Down"},
		{"left", "Left"},
		{"right", "Right"},
		{"a", "a"},
		{"meta+z", "<Super>z"},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := ConvertHotkey(mustParse(t, tc.spec))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConvertHotkeyHandBuiltAliases(t *testing.T) {
	// Hand-built Hotkeys may carry the input aliases and stray case.
	got, err := ConvertHotkey(platform.Hotkey{Mods: []platform.Mod{platform.ModAlt}, Key: "Enter"})
	require.NoError(t, err)
	require.Equal(t, "<Alt>Return", got)

	got, err = ConvertHotkey(platform.Hotkey{Key: " esc "})
	require.NoError(t, err)
	require.Equal(t, "Escape", got)
}

func TestConvertHotkeyDeduplicatesModifiers(t *testing.T) {
	hk := platform.Hotkey{
		Mods: []platform.Mod{platform.ModCtrl, platform.ModAlt, platform.ModCtrl},
		Key:  "space",
	}
	got, err := ConvertHotkey(hk)
	require.NoError(t, err)
	require.Equal(t, "<Control><Alt>space", got)
}

func TestConvertHotkeyErrors(t *testing.T) {
	_, err := ConvertHotkey(platform.Hotkey{Key: "hyper"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "hyper")

	_, err = ConvertHotkey(platform.Hotkey{Mods: []platform.Mod{platform.Mod(99)}, Key: "space"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "modifier")
}

func TestNormalizeAccel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"<Alt>space", "alt+space"},
		{"<Control><Alt>space", "alt+control+space"},
		{"<Alt><Control>space", "alt+control+space"}, // order-insensitive
		{"<Primary>a", "control+a"},
		{"<Ctrl>a", "control+a"},
		{"<ctl>A", "control+a"},
		{"<CONTROL>a", "control+a"},
		{"<Super>Space", "super+space"}, // key case-insensitive
		{"XF86Keyboard", "xf86keyboard"},
		{"<Shift><Super>space", "shift+super+space"},
		{"<Meta>space", "meta+space"}, // unknown mod kept distinct
		{" <Alt>F1 ", "alt+f1"},
		{"", ""},
		{"   ", ""},
		{"<Alt", ""},   // unterminated tag
		{"<Alt>", ""},  // no key
		{"<Alt> ", ""}, // no key after trimming
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeAccel(tc.in))
		})
	}
}

func TestSameAccel(t *testing.T) {
	require.True(t, sameAccel("<Primary><Alt>space", "<Alt><Control>space"))
	require.True(t, sameAccel("<Super>space", "<super>SPACE"))
	require.False(t, sameAccel("<Alt>space", "<Control><Alt>space"))
	require.False(t, sameAccel("", ""), "unparseable equals nothing")
	require.False(t, sameAccel("<Alt", "<Alt"))
}
