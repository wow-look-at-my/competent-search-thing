package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeConfig materializes raw JSON as the config file and returns its
// path (setConfigDir must already have run).
func writeConfig(t *testing.T, raw string) string {
	t.Helper()
	p, err := Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte(raw), 0o644))
	return p
}

// readRawConfig parses the on-disk config file into a generic map so
// tests can assert the persisted JSON independent of struct decoding.
func readRawConfig(t *testing.T, p string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

func TestMigrateLegacyDefaultRootsUpgrade(t *testing.T) {
	setConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := writeConfig(t, `{
		"roots": ["`+home+`"],
		"excludes": [".git", "node_modules", ".cache"],
		"hotkey": "alt+space"
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/"}, c.Roots, "legacy home-default roots upgrade to the whole filesystem")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t,
		[]string{".git", "node_modules", ".cache", "/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"},
		c.Excludes, "missing system excludes are appended after the user's patterns")

	require.Len(t, c.MigrationNotes, 2, "both the roots change and the excludes change are reported")
	require.Contains(t, c.MigrationNotes[0], "whole-filesystem default (/)")
	require.Contains(t, c.MigrationNotes[0], "edit roots in config.json to revert")
	require.Contains(t, c.MigrationNotes[1], "/proc")

	// The migration is persisted: the file now carries the stamp and
	// the upgraded values, so the next load is a no-op.
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
	require.Equal(t, []any{"/"}, doc["roots"])

	again, err := Load()
	require.NoError(t, err)
	require.Empty(t, again.MigrationNotes, "the second load has nothing left to migrate")
}

func TestMigrateCustomRootsUntouched(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{
		"roots": ["/data", "/srv/media"],
		"excludes": [".git"]
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/data", "/srv/media"}, c.Roots, "customized roots are never touched")
	require.Equal(t, []string{".git"}, c.Excludes, "customized excludes are never touched")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Empty(t, c.MigrationNotes, "nothing user-visible changed, so nothing is announced")

	// The version stamp alone is still persisted (the check must not
	// re-run on every load).
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
}

func TestMigrateAlreadyCurrentIsNoOp(t *testing.T) {
	setConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A v2 config may legitimately hold the home directory as its one
	// root (the user narrowed the scope back down); it must survive.
	raw := `{"roots": ["` + home + `"], "rootsVersion": 2, "excludes": [".git"]}`
	p := writeConfig(t, raw)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{home}, c.Roots, "a v2 home root is the user's choice, not a legacy default")
	require.Equal(t, []string{".git"}, c.Excludes)
	require.Empty(t, c.MigrationNotes)

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, raw, string(data), "an already-current file is not rewritten")
}

func TestMigrateEmptyRootsGetNewDefaults(t *testing.T) {
	setConfigDir(t)
	// A config that never chose roots always meant "the default"; the
	// default is now the whole filesystem, and that change is loud.
	writeConfig(t, `{"theme": "dark"}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/"}, c.Roots)
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.NotEmpty(t, c.MigrationNotes)
	require.Contains(t, c.MigrationNotes[0], "whole-filesystem default")
}

func TestMigrateMergePreservesUserExcludes(t *testing.T) {
	setConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, `{
		"roots": ["`+home+`"],
		"excludes": ["*.tmp", "/proc", "secrets"]
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t,
		[]string{"*.tmp", "/proc", "secrets", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"},
		c.Excludes, "user patterns keep their order; only missing system ones are appended")
	require.Len(t, c.MigrationNotes, 2)
	require.NotContains(t, c.MigrationNotes[1], "/proc", "an exclude already present is not announced as added")
}

func TestMigrateCorruptFileYieldsCurrentDefaults(t *testing.T) {
	dir := setConfigDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{broken"), 0o644))
	c, err := Load()
	require.Error(t, err)
	require.Equal(t, Default(), c)
	require.Equal(t, currentRootsVersion, c.RootsVersion, "corrupt files fall back to v2 defaults")
	require.Empty(t, c.MigrationNotes)
}

func TestFreshInstallWritesCurrentDefaults(t *testing.T) {
	setConfigDir(t)
	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/"}, c.Roots)
	require.Empty(t, c.MigrationNotes, "a fresh install is not a migration")

	p, err := Path()
	require.NoError(t, err)
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
	require.Equal(t, []any{"/"}, doc["roots"])
}

func TestDefaultRootsFor(t *testing.T) {
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	cases := []struct {
		name   string
		goos   string
		getenv func(string) string
		want   []string
	}{
		{"linux", "linux", env(nil), []string{"/"}},
		{"darwin", "darwin", env(nil), []string{"/"}},
		{"windows system drive", "windows", env(map[string]string{"SystemDrive": "D:"}), []string{`D:\`}},
		{"windows drive with slash", "windows", env(map[string]string{"SystemDrive": `C:\`}), []string{`C:\`}},
		{"windows fallback", "windows", env(nil), []string{`C:\`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, defaultRootsFor(tc.goos, tc.getenv))
		})
	}
}

func TestSystemExcludesFor(t *testing.T) {
	require.Nil(t, systemExcludesFor("windows"), "windows has no unix system trees")
	for _, goos := range []string{"linux", "darwin"} {
		got := systemExcludesFor(goos)
		require.Equal(t, []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"}, got, goos)
		for _, p := range got[:len(got)-1] {
			require.True(t, strings.HasPrefix(p, "/"), "system tree patterns are full-path patterns: %s", p)
		}
	}
}

func TestLegacyDefaultRootsFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	roots := legacyDefaultRoots()
	require.Len(t, roots, 1)
	require.True(t, filepath.IsAbs(roots[0]), "fallback root is the absolutized cwd")
}
