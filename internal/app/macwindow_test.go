package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

func TestMacWindowOptionsForOffIsNil(t *testing.T) {
	// Flag off = nil whatever the theme: main.go then wires a nil Mac
	// field, byte-identical to the pre-flag wails.Run call.
	require.Nil(t, macWindowOptionsFor(false, "dark"))
	require.Nil(t, macWindowOptionsFor(false, "light"))
	require.Nil(t, macWindowOptionsFor(false, "mytheme"))
}

func TestMacWindowOptionsForDark(t *testing.T) {
	got := macWindowOptionsFor(true, "dark")
	require.Equal(t, &mac.Options{
		WindowIsTranslucent:  true,
		WebviewIsTransparent: true,
		Appearance:           mac.AppearanceType("NSAppearanceNameVibrantDark"),
	}, got)
}

func TestMacWindowOptionsForLight(t *testing.T) {
	got := macWindowOptionsFor(true, "light")
	require.Equal(t, &mac.Options{
		WindowIsTranslucent:  true,
		WebviewIsTransparent: true,
		Appearance:           mac.NSAppearanceNameVibrantLight,
	}, got)
}

func TestMacWindowOptionsForCustomThemeUsesDarkMaterial(t *testing.T) {
	// Custom themes extend dark by default and unknown names resolve
	// to dark, so anything that is not the light builtin gets the
	// dark vibrant material.
	got := macWindowOptionsFor(true, "mytheme")
	require.Equal(t, appearanceVibrantDark, got.Appearance)
}

func TestMacWindowOptionsReadsConfig(t *testing.T) {
	dir := themeTestDir(t)

	// No config file: Load writes defaults (translucent off) -> nil.
	require.Nil(t, MacWindowOptions())

	writeConfigJSON(t, dir, `{"window": {"translucent": true}, "theme": "light"}`)
	got := MacWindowOptions()
	require.NotNil(t, got)
	require.True(t, got.WindowIsTranslucent)
	require.True(t, got.WebviewIsTransparent)
	require.Equal(t, mac.NSAppearanceNameVibrantLight, got.Appearance)

	writeConfigJSON(t, dir, `{"window": {"translucent": true}}`)
	require.Equal(t, appearanceVibrantDark, MacWindowOptions().Appearance,
		"the default dark theme gets the dark vibrant material")
}

func TestMacWindowOptionsConfigErrorIsNil(t *testing.T) {
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{not json`)
	require.Nil(t, MacWindowOptions(),
		"a corrupt config keeps the safe opaque default (the WindowTranslucent stance)")
	// Guard against config.Load having silently stopped returning
	// errors for corrupt files -- the test above would then assert
	// nothing.
	_, err := config.Load()
	require.Error(t, err)
}
