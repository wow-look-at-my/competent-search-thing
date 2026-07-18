package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

func TestPreviewWindowSize(t *testing.T) {
	t.Run("absent config means the classic size", func(t *testing.T) {
		t.Setenv(config.EnvConfigDir, t.TempDir())
		w, h, enabled := PreviewWindowSize()
		require.False(t, enabled)
		require.Equal(t, WindowWidth, w)
		require.Equal(t, WindowHeight, h)
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

	t.Run("disabled ignores the configured size", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"preview":{"enabled":false,"windowWidth":1440,"windowHeight":900}}`), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.False(t, enabled)
		require.Equal(t, WindowWidth, w)
		require.Equal(t, WindowHeight, h)
	})

	t.Run("corrupt config means the classic size", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"preview":{"enabled":true`), 0o644))
		w, h, enabled := PreviewWindowSize()
		require.False(t, enabled)
		require.Equal(t, WindowWidth, w)
		require.Equal(t, WindowHeight, h)
	})
}
