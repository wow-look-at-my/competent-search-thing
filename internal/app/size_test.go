package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

func TestWindowSize(t *testing.T) {
	t.Run("absent config means defaults", func(t *testing.T) {
		t.Setenv(config.EnvConfigDir, t.TempDir())
		w, h := WindowSize()
		require.Equal(t, config.DefaultWindowWidth, w)
		require.Equal(t, config.DefaultWindowHeight, h)
	})

	t.Run("configured size wins", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"window":{"width":900,"height":640}}`), 0o644))
		w, h := WindowSize()
		require.Equal(t, 900, w)
		require.Equal(t, 640, h)
	})

	t.Run("too-small values clamp up to the floors", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"window":{"width":10,"height":10}}`), 0o644))
		w, h := WindowSize()
		require.Equal(t, config.MinWindowWidth, w)
		require.Equal(t, config.MinWindowHeight, h)
	})

	t.Run("corrupt config means defaults", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"window":{"width":900`), 0o644))
		w, h := WindowSize()
		require.Equal(t, config.DefaultWindowWidth, w)
		require.Equal(t, config.DefaultWindowHeight, h)
	})
}

func TestAppWindowSizeFallsBackWhenUnset(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	w, h := a.windowSize()
	require.Equal(t, config.DefaultWindowWidth, w)
	require.Equal(t, config.DefaultWindowHeight, h)

	a, _ = newTestApp(t, nil, Options{WindowWidth: 1000, WindowHeight: 700})
	w, h = a.windowSize()
	require.Equal(t, 1000, w)
	require.Equal(t, 700, h)
}
