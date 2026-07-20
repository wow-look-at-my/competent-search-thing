package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/theme"
)

// themeTestDir points config (and so the app's theme layer) at a fresh
// private dir and returns it. newTestApp already sets one; calling
// this after it re-points the env at a dir the test can inspect.
func themeTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	return dir
}

func writeConfigJSON(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644))
}

func TestGetThemeDefaultIsDark(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	themeTestDir(t)
	got := a.GetTheme()
	require.Equal(t, theme.Dark(), got)
	require.Len(t, got, len(theme.TokenNames), "the map always covers the full token set")
}

func TestGetThemeHonorsConfiguredTheme(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{"theme": "light"}`)
	got := a.GetTheme()
	require.Equal(t, "#f7f7f9", got["bg"], "the light builtin applies")
	require.Equal(t, "14px", got["font-size"], "light inherits metrics from dark")
}

func TestGetThemeResolvesUserThemeFile(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "themes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "themes", "mine.json"),
		[]byte(`{"extends": "dark", "tokens": {"accent": "#123456"}}`), 0o644))
	writeConfigJSON(t, dir, `{"theme": "mine"}`)
	got := a.GetTheme()
	require.Equal(t, "#123456", got["accent"])
	require.Equal(t, theme.Dark()["bg"], got["bg"])
}

func TestGetThemeBadThemeFallsBackToDark(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{"theme": "no-such-theme"}`)
	require.Equal(t, theme.Dark(), a.GetTheme())
	// A second identical failure exercises the log dedup path; a later
	// success resets it.
	require.Equal(t, theme.Dark(), a.GetTheme())
	writeConfigJSON(t, dir, `{"theme": "dark"}`)
	require.Equal(t, theme.Dark(), a.GetTheme())
}

func TestGetCustomCSS(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	require.Empty(t, a.GetCustomCSS(), "no themes dir yet")

	themes := filepath.Join(dir, "themes")
	require.NoError(t, os.MkdirAll(themes, 0o755))
	require.Empty(t, a.GetCustomCSS(), "no custom.css yet")

	css := "#bar { outline: 2px solid hotpink; }\n"
	require.NoError(t, os.WriteFile(filepath.Join(themes, "custom.css"), []byte(css), 0o644))
	require.Equal(t, css, a.GetCustomCSS())

	huge := strings.Repeat("x", 64*1024+1)
	require.NoError(t, os.WriteFile(filepath.Join(themes, "custom.css"), []byte(huge), 0o644))
	require.Empty(t, a.GetCustomCSS(), "oversized files are ignored")
}

func TestGetCustomCSSWithoutConfigDir(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	require.Empty(t, a.GetCustomCSS())
	require.Equal(t, theme.Dark(), a.GetTheme(), "GetTheme degrades to dark too")
}

func TestThemeHotReloadEmitsOnFileAndConfigChanges(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	a.Startup(context.Background())

	themes := filepath.Join(dir, "themes")
	require.DirExists(t, themes, "startup materializes the themes dir")

	// A theme file change surfaces as one (debounced) theme:changed.
	require.NoError(t, os.WriteFile(filepath.Join(themes, "mine.json"),
		[]byte(`{"tokens": {"bg": "#101010"}}`), 0o644))
	require.Eventually(t, func() bool {
		return len(r.emitted(eventThemeChanged)) >= 1
	}, 10*time.Second, 10*time.Millisecond, "theme file writes trigger a reload event")

	// So does editing config.json (the live theme switch path).
	before := len(r.emitted(eventThemeChanged))
	writeConfigJSON(t, dir, `{"theme": "light"}`)
	require.Eventually(t, func() bool {
		return len(r.emitted(eventThemeChanged)) > before
	}, 10*time.Second, 10*time.Millisecond, "config.json writes trigger a reload event")

	// custom.css edits count as well.
	before = len(r.emitted(eventThemeChanged))
	require.NoError(t, os.WriteFile(filepath.Join(themes, "custom.css"), []byte("/*x*/"), 0o644))
	require.Eventually(t, func() bool {
		return len(r.emitted(eventThemeChanged)) > before
	}, 10*time.Second, 10*time.Millisecond, "custom.css writes trigger a reload event")

	a.Shutdown(context.Background())
	a.watchMu.Lock()
	require.Nil(t, a.themeW, "shutdown tears the theme watcher down")
	a.watchMu.Unlock()
}

func TestThemeWatchIrrelevantFilesDoNotEmit(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	dir := themeTestDir(t)
	a.Startup(context.Background())
	// A sibling file in the config dir (not config.json, not themes/)
	// must not trigger a reload.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644))
	require.Never(t, func() bool {
		return len(r.emitted(eventThemeChanged)) > 0
	}, 800*time.Millisecond, 20*time.Millisecond)
}

func TestStartThemeWatchAfterShutdownIsSkipped(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	themeTestDir(t)
	a.Shutdown(context.Background())
	a.startThemeWatch()
	a.watchMu.Lock()
	require.Nil(t, a.themeW, "a watcher started during shutdown is stopped, not installed")
	a.watchMu.Unlock()
}

func TestStartThemeWatchWithoutConfigDirDegrades(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	a.startThemeWatch() // logs and returns; no watcher, no crash
	a.watchMu.Lock()
	require.Nil(t, a.themeW)
	a.watchMu.Unlock()
}

// The darwin translucent bg-opacity substitution (tuneDarwinTranslucent
// in theme.go): the builtin 0.97 reads opaque over the
// NSVisualEffectView, so darwin+translucent gets a visibly frosted
// default -- while customized values and every other platform/flag
// combination stay byte-identical.

func TestGetThemeDarwinTranslucentTunesDefaultOpacity(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "darwin"
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{"window": {"translucent": true}}`)
	got := a.GetTheme()
	require.Equal(t, darwinTranslucentBgOpacity, got["bg-opacity"])
	// Only bg-opacity is touched; everything else stays the builtin.
	require.Equal(t, theme.Dark()["bg"], got["bg"])
	require.Equal(t, theme.Dark()["blur"], got["blur"])
}

func TestGetThemeDarwinTranslucentAppliesToLightDefault(t *testing.T) {
	// light OVERRIDES bg-opacity (0.98, vs dark's 0.97) -- still a
	// builtin default authored for opaque windows, so the darwin
	// tuning applies to it too (builtinDefaultBgOpacity checks both).
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "darwin"
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{"theme": "light", "window": {"translucent": true}}`)
	got := a.GetTheme()
	require.Equal(t, darwinTranslucentBgOpacity, got["bg-opacity"])
	require.Equal(t, "#f7f7f9", got["bg"], "still the light palette")
}

func TestGetThemeDarwinTranslucentKeepsCustomOpacity(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "darwin"
	dir := themeTestDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "themes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "themes", "mine.json"),
		[]byte(`{"extends": "dark", "tokens": {"bg-opacity": "0.5"}}`), 0o644))
	writeConfigJSON(t, dir, `{"theme": "mine", "window": {"translucent": true}}`)
	require.Equal(t, "0.5", a.GetTheme()["bg-opacity"],
		"a user-customized bg-opacity always wins")
}

func TestGetThemeOpacityUntouchedOffDarwinOrFlagOff(t *testing.T) {
	// linux + translucent: unchanged (no compositor blur there; the
	// linux translucent look stays exactly as measured in the README).
	a, _ := newTestApp(t, nil, Options{}) // goos pinned "linux"
	dir := themeTestDir(t)
	writeConfigJSON(t, dir, `{"window": {"translucent": true}}`)
	require.Equal(t, theme.Dark()["bg-opacity"], a.GetTheme()["bg-opacity"])

	// darwin + flag off: unchanged.
	b, _ := newTestApp(t, nil, Options{})
	b.plat.goos = "darwin"
	dir2 := themeTestDir(t)
	writeConfigJSON(t, dir2, `{}`)
	require.Equal(t, theme.Dark()["bg-opacity"], b.GetTheme()["bg-opacity"])
	require.Equal(t, theme.Dark(), b.GetTheme(), "flag off = the untouched builtin map")
}
