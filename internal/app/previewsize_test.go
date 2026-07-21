package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

func TestPreviewWindowSize(t *testing.T) {
	t.Run("absent config means the pane on at the preview defaults", func(t *testing.T) {
		// The pane is ON by default (config v8): a fresh install gets
		// the preview-sized window with zero configuration.
		t.Setenv(config.EnvConfigDir, t.TempDir())
		w, h, enabled := PreviewWindowSize()
		require.True(t, enabled)
		require.Equal(t, config.DefaultPreviewWindowWidth, w)
		require.Equal(t, config.DefaultPreviewWindowHeight, h)
	})

	t.Run("enabled means the configured size", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"preview":{"enabled":true,"windowWidth":1440,"windowHeight":900}}`), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.True(t, enabled)
		require.Equal(t, 1440, w)
		require.Equal(t, 900, h)
	})

	t.Run("enabled with zero dimensions gets the defaults", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"preview":{"enabled":true}}`), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.True(t, enabled)
		require.Equal(t, config.DefaultPreviewWindowWidth, w, "Load normalizes the dimensions")
		require.Equal(t, config.DefaultPreviewWindowHeight, h)
	})

	t.Run("disabled ignores the preview size but honors window size", func(t *testing.T) {
		// The opt-out must carry the current rootsVersion stamp: an
		// unstamped false is pre-flip machine handwriting the v8
		// migration resets to on (pinned in internal/config).
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		raw := `{"rootsVersion":` + strconv.Itoa(config.CurrentRootsVersion()) +
			`,"window":{"width":900,"height":640},"preview":{"enabled":false,"windowWidth":1440,"windowHeight":900}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.False(t, enabled)
		require.Equal(t, 900, w)
		require.Equal(t, 640, h)
	})

	t.Run("corrupt config means the base defaults", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"preview":{"enabled":true`), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.False(t, enabled)
		require.Equal(t, config.DefaultWindowWidth, w)
		require.Equal(t, config.DefaultWindowHeight, h)
	})
}
