package platform

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseHotkey(t *testing.T) {
	tests := []struct {
		in   string
		want Hotkey
	}{
		{"alt+space", Hotkey{Mods: []Mod{ModAlt}, Key: "space"}},
		{"ctrl+shift+k", Hotkey{Mods: []Mod{ModCtrl, ModShift}, Key: "k"}},
		{"super+space", Hotkey{Mods: []Mod{ModSuper}, Key: "space"}},
		{"cmd+space", Hotkey{Mods: []Mod{ModSuper}, Key: "space"}},
		{"win+e", Hotkey{Mods: []Mod{ModSuper}, Key: "e"}},
		{"meta+Enter", Hotkey{Mods: []Mod{ModSuper}, Key: "return"}},
		{"CONTROL+OPTION+ESC", Hotkey{Mods: []Mod{ModCtrl, ModAlt}, Key: "escape"}},
		{" ctrl + shift + f11 ", Hotkey{Mods: []Mod{ModCtrl, ModShift}, Key: "f11"}},
		{"alt+alt+tab", Hotkey{Mods: []Mod{ModAlt}, Key: "tab"}},
		{"ctrl+7", Hotkey{Mods: []Mod{ModCtrl}, Key: "7"}},
		{"alt+up", Hotkey{Mods: []Mod{ModAlt}, Key: "up"}},
		{"shift+down", Hotkey{Mods: []Mod{ModShift}, Key: "down"}},
		{"ctrl+left", Hotkey{Mods: []Mod{ModCtrl}, Key: "left"}},
		{"ctrl+right", Hotkey{Mods: []Mod{ModCtrl}, Key: "right"}},
		{"f1", Hotkey{Key: "f1"}}, // bare key: allowed, the parser does not police taste
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseHotkey(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseHotkeyEveryLetterDigitAndFKey(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		hk, err := ParseHotkey("ctrl+" + string(c))
		require.NoError(t, err)
		require.Equal(t, string(c), hk.Key)
	}
	for c := '0'; c <= '9'; c++ {
		hk, err := ParseHotkey("alt+" + string(c))
		require.NoError(t, err)
		require.Equal(t, string(c), hk.Key)
	}
	for i := 1; i <= 12; i++ {
		hk, err := ParseHotkey(fmt.Sprintf("super+f%d", i))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("f%d", i), hk.Key)
	}
}

func TestParseHotkeyErrors(t *testing.T) {
	tests := []struct {
		in      string
		errPart string
	}{
		{"", "empty spec"},
		{"   ", "empty spec"},
		{"alt++space", "empty token"},
		{"alt+space+", "empty token"},
		{"+space", "empty token"},
		{"hyper+space", `unknown modifier "hyper"`},
		{"ctrl+s+k", `unknown modifier "s"`},
		{"alt+f13", `unknown key "f13"`},
		{"alt+spacebar", `unknown key "spacebar"`},
		{"altspace", `unknown key "altspace"`},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			_, err := ParseHotkey(tc.in)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errPart)
		})
	}
}

func TestHotkeyString(t *testing.T) {
	hk, err := ParseHotkey("Control+Win+Enter")
	require.NoError(t, err)
	require.Equal(t, "ctrl+super+return", hk.String())
	require.Equal(t, "f2", Hotkey{Key: "f2"}.String())
	require.Equal(t, "mod(99)", Mod(99).String(), "out-of-range Mod renders diagnostically")
}
