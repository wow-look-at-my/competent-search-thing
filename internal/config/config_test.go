package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// setConfigDir points the package at a private config dir for the test.
func setConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	return dir
}

func TestPathUsesEnvOverride(t *testing.T) {
	dir := setConfigDir(t)
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "config.json"), p)
}

func TestDirIsParentOfConfigFile(t *testing.T) {
	dir := setConfigDir(t)
	got, err := Dir()
	require.NoError(t, err)
	require.Equal(t, dir, got)

	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	_, err = Dir()
	require.Error(t, err, "Dir surfaces Path failures")
}

func TestPathDefaultsToUserConfigDir(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), appDirName, "config.json"), p)
}

func TestLoadWritesDefaultWhenMissing(t *testing.T) {
	dir := setConfigDir(t)
	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, Default(), c)

	// The default file was materialized on disk and parses back.
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	var onDisk Config
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, Default(), onDisk)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	setConfigDir(t)
	in := Config{
		Roots:                 []string{"/data/projects", "relative/dir"},
		Excludes:              []string{".git", "*.o"},
		Hotkey:                "ctrl+shift+p",
		RescanIntervalMinutes: 30,
		MaxResults:            120,
		Theme:                 "light",
	}
	require.NoError(t, Save(in))

	got, err := Load()
	require.NoError(t, err)
	require.Equal(t, in.Excludes, got.Excludes)
	require.Equal(t, in.Hotkey, got.Hotkey)
	require.Equal(t, in.RescanIntervalMinutes, got.RescanIntervalMinutes)
	require.Equal(t, in.MaxResults, got.MaxResults)
	require.Equal(t, in.Theme, got.Theme)
	// Roots are normalized on load: absolute stays, relative becomes
	// absolute.
	require.Equal(t, "/data/projects", got.Roots[0])
	require.True(t, filepath.IsAbs(got.Roots[1]))
}

func TestLoadCorruptFileReturnsDefaultsAndError(t *testing.T) {
	dir := setConfigDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o644))
	c, err := Load()
	require.Error(t, err)
	require.Equal(t, Default(), c, "corrupt file still yields usable defaults")
}

func TestLoadUnreadablePathReturnsDefaultsAndError(t *testing.T) {
	dir := setConfigDir(t)
	// A directory named config.json makes ReadFile fail with a
	// non-NotExist error.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "config.json"), 0o755))
	c, err := Load()
	require.Error(t, err)
	require.Equal(t, Default(), c)
}

func TestLoadWhenConfigDirUnresolvable(t *testing.T) {
	// With no env override, no XDG dir, and no HOME, Path() fails and
	// Load falls back to defaults plus the error.
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	c, err := Load()
	require.Error(t, err)
	require.NotEmpty(t, c.Roots, "defaults remain usable without HOME")
	require.Equal(t, DefaultMaxResults, c.MaxResults)
}

func TestSaveErrorWhenDirBlocked(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))
	// MkdirAll must fail: a plain file sits where the dir should go.
	t.Setenv(EnvConfigDir, filepath.Join(blocker, "sub"))
	require.Error(t, Save(Default()))

	// Load also fails on this path (ENOTDIR while reading) but still
	// hands back defaults.
	c, err := Load()
	require.Error(t, err)
	require.Equal(t, Default(), c)
}

func TestLoadDefaultWriteFailure(t *testing.T) {
	// A dangling symlink makes ReadFile fail with ErrNotExist (so Load
	// takes the write-the-default-file path) while MkdirAll inside
	// Save cannot create the directory. Load must surface the write
	// error yet still return usable defaults.
	base := t.TempDir()
	gone := filepath.Join(base, "gone")
	require.NoError(t, os.Symlink(filepath.Join(base, "no-such-target"), gone))
	t.Setenv(EnvConfigDir, gone)

	c, err := Load()
	require.Error(t, err)
	require.Equal(t, Default(), c)
}

func TestNormalize(t *testing.T) {
	c := Config{
		Roots:                 []string{"", "rel", "/abs"},
		Hotkey:                "",
		RescanIntervalMinutes: -5,
		MaxResults:            0,
	}
	c.Normalize()
	require.Len(t, c.Roots, 2)
	require.True(t, filepath.IsAbs(c.Roots[0]))
	require.Equal(t, "/abs", c.Roots[1])
	require.Equal(t, DefaultHotkey, c.Hotkey)
	require.Equal(t, 0, c.RescanIntervalMinutes)
	require.Equal(t, DefaultMaxResults, c.MaxResults)
	require.Equal(t, DefaultTheme, c.Theme, "empty theme falls back to dark")
	require.Nil(t, c.Excludes, "excludes are not defaulted on normalize")

	keep := Config{Theme: "light"}
	keep.Normalize()
	require.Equal(t, "light", keep.Theme, "a configured theme is preserved")

	var empty Config
	empty.Normalize()
	require.Equal(t, Default().Roots, empty.Roots, "no roots falls back to default root")

	allEmpty := Config{Roots: []string{""}}
	allEmpty.Normalize()
	require.Equal(t, Default().Roots, allEmpty.Roots, "only-empty roots fall back to default root")
}

func TestDefaultFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	c := Default()
	require.Len(t, c.Roots, 1)
	require.True(t, filepath.IsAbs(c.Roots[0]), "fallback root is absolutized cwd")
}
