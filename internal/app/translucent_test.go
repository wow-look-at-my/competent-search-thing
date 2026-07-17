package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

func TestWindowTranslucent(t *testing.T) {
	t.Run("absent config means opaque", func(t *testing.T) {
		t.Setenv(config.EnvConfigDir, t.TempDir())
		require.False(t, WindowTranslucent())
	})

	t.Run("configured true means translucent", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"window":{"translucent":true}}`), 0o644))
		require.True(t, WindowTranslucent())
	})

	t.Run("corrupt config means opaque", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(config.EnvConfigDir, dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"),
			[]byte(`{"window":{"translucent":true`), 0o644))
		require.False(t, WindowTranslucent())
	})
}
