package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/schemas"
)

// The schema sidecar: Startup writes the embedded config schema next
// to config.json so the "$schema": "./config.schema.json" reference
// resolves against a schema matching the running binary.
func TestStartupWritesSchemaSidecar(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir) // after newTestApp, which re-points it
	a.Startup(context.Background())

	got, err := os.ReadFile(filepath.Join(dir, "config.schema.json"))
	require.NoError(t, err, "Startup writes the sidecar")
	require.Equal(t, string(schemas.ConfigSchemaJSON), string(got),
		"the sidecar is exactly the embedded schema")
}

func TestSchemaSidecarRefreshAndSkip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	path := filepath.Join(dir, "config.schema.json")

	// A stale sidecar (an older binary's schema) is refreshed.
	require.NoError(t, os.WriteFile(path, []byte(`{"old": true}`), 0o644))
	wrote, err := writeSchemaSidecar()
	require.NoError(t, err)
	require.True(t, wrote, "differing bytes are rewritten")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(schemas.ConfigSchemaJSON), string(got))

	// Matching bytes are left alone (no write, mtime untouched) so a
	// boot never churns the config directory.
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	wrote, err = writeSchemaSidecar()
	require.NoError(t, err)
	require.False(t, wrote, "byte-equal sidecar is skipped")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.WithinDuration(t, old, fi.ModTime(), 2*time.Second, "the file was not rewritten")
}

func TestSchemaSidecarUnresolvableDirDegrades(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// config.Dir now fails; startSchemaSidecar must log and run on.
	a.startSchemaSidecar()
}
