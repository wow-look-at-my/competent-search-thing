package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Load-driven tests below run on the linux AND darwin CI jobs,
// and the v4 firmlink step diverges per OS: on macOS a default-shaped
// exclude list gains /System/Volumes/Data and a curated one gains an
// informational note; elsewhere the step is a pure stamp. These two
// helpers mirror that so one test body covers both jobs (the
// headless per-goos coverage lives in the TestMigrateV4* tests, which
// call migrateRootsFor directly).

// withFirmlink returns list plus the firmlink exclude when the
// process runs on darwin.
func withFirmlink(list []string) []string {
	if runtime.GOOS != "darwin" {
		return list
	}
	return append(append([]string{}, list...), "/System/Volumes/Data")
}

// plusFirmlinkNote returns n plus one on darwin, where the v4 step
// contributes one extra migration note.
func plusFirmlinkNote(n int) int {
	if runtime.GOOS != "darwin" {
		return n
	}
	return n + 1
}

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
		withFirmlink([]string{".git", "node_modules", ".cache",
			"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found",
			".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".nox", ".venv"}),
		c.Excludes, "missing system excludes, then the v3 noise excludes, are appended after the user's patterns")

	require.Len(t, c.MigrationNotes, plusFirmlinkNote(3), "the roots change, the system excludes, and the noise excludes are reported")
	require.Contains(t, c.MigrationNotes[0], "whole-filesystem default (/)")
	require.Contains(t, c.MigrationNotes[0], "edit roots in config.json to revert")
	require.Contains(t, c.MigrationNotes[1], "/proc")
	require.Contains(t, c.MigrationNotes[2], "high-churn exclude patterns added for the watch layer")
	require.Contains(t, c.MigrationNotes[2], "__pycache__")

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
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(1), "a curated exclude list gets the informational note(s) only")
	require.Contains(t, c.MigrationNotes[0], "your customized exclude list was left unchanged")

	// The version stamp alone is still persisted (the check must not
	// re-run on every load).
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
}

func TestMigrateAlreadyCurrentIsNoOp(t *testing.T) {
	setConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A current config may legitimately hold the home directory as its
	// one root (the user narrowed the scope back down); it must
	// survive, and so must a curated exclude list.
	raw := `{"roots": ["` + home + `"], "rootsVersion": 4, "excludes": [".git"]}`
	p := writeConfig(t, raw)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{home}, c.Roots, "a stamped home root is the user's choice, not a legacy default")
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
	// A config with no excludes at all never carried the base
	// patterns, so the v3 step only informs -- consistent with the v2
	// step, which also never forced the base name patterns on it.
	require.Equal(t, []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"}, c.Excludes,
		"only the system excludes are appended; base and noise patterns are not forced on an exclude-less config")
	require.Contains(t, c.MigrationNotes[len(c.MigrationNotes)-1], "left unchanged")
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
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(3))
	require.NotContains(t, c.MigrationNotes[1], "/proc", "an exclude already present is not announced as added")
	require.Contains(t, c.MigrationNotes[2], "left unchanged",
		"a curated list (no base patterns) never gains the noise patterns")
}

func TestMigrateV2DefaultShapedGainsNoiseExcludes(t *testing.T) {
	setConfigDir(t)
	// A config the v2 migration (or a v2-era fresh install) wrote:
	// whole-filesystem root, base + system excludes, stamped 2. The v3
	// step appends the noise patterns and announces them.
	p := writeConfig(t, `{
		"roots": ["/"],
		"rootsVersion": 2,
		"excludes": [".git", "node_modules", ".cache", "/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"]
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/"}, c.Roots, "the v3 step never touches roots")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	want := withFirmlink([]string{".git", "node_modules", ".cache", "/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found",
		".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".nox", ".venv"})
	require.Equal(t, want, c.Excludes, "the noise patterns are appended after everything the config already had")
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(1))
	require.Contains(t, c.MigrationNotes[0], "high-churn exclude patterns added for the watch layer")
	require.Contains(t, c.MigrationNotes[0], ".hg, .svn, __pycache__")
	require.Contains(t, c.MigrationNotes[0], "remove any of them in config.json to index those trees")

	// Persisted: the file carries the stamp and the appended patterns.
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
	require.Len(t, doc["excludes"], len(want))

	// Idempotent: a second load appends nothing and announces nothing.
	again, err := Load()
	require.NoError(t, err)
	require.Empty(t, again.MigrationNotes)
	require.Len(t, again.Excludes, len(want))
}

func TestMigrateV2PartialNoiseAppendsOnlyMissing(t *testing.T) {
	setConfigDir(t)
	// The user already added .hg themselves: only the missing noise
	// patterns are appended, and .hg is not announced (nor moved).
	writeConfig(t, `{
		"roots": ["/"],
		"rootsVersion": 2,
		"excludes": [".git", ".hg", "node_modules", ".cache"]
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t,
		withFirmlink([]string{".git", ".hg", "node_modules", ".cache",
			".svn", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".nox", ".venv"}),
		c.Excludes, "the user's .hg keeps its position; only the missing patterns are appended")
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(1))
	require.NotContains(t, c.MigrationNotes[0], ".hg,", "an already-present pattern is not announced as added")
	require.Contains(t, c.MigrationNotes[0], ".svn")
}

func TestMigrateV2CuratedExcludesStampOnly(t *testing.T) {
	setConfigDir(t)
	// The user curated the base patterns away (indexing .git was a
	// choice): nothing is appended, the stamp advances, and one
	// informational note points at the new defaults.
	p := writeConfig(t, `{
		"roots": ["/"],
		"rootsVersion": 2,
		"excludes": ["node_modules", ".cache", "/proc"]
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"node_modules", ".cache", "/proc"}, c.Excludes,
		"a curated exclude list is never extended")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(1))
	require.Contains(t, c.MigrationNotes[0], "new default exclude patterns exist")
	require.Contains(t, c.MigrationNotes[0], "__pycache__")
	require.Contains(t, c.MigrationNotes[0], "your customized exclude list was left unchanged")

	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"], "the stamp alone is persisted")
	require.Len(t, doc["excludes"], 3)
}

func TestMigrateV2ExplicitEmptyExcludesStampOnly(t *testing.T) {
	setConfigDir(t)
	// An explicitly empty list means "exclude nothing" and stays that
	// way; only the stamp advances (plus the informational note).
	p := writeConfig(t, `{"roots": ["/"], "rootsVersion": 2, "excludes": []}`)

	c, err := Load()
	require.NoError(t, err)
	require.Empty(t, c.Excludes, "an explicitly empty exclude list is respected")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Len(t, c.MigrationNotes, plusFirmlinkNote(1))
	require.Contains(t, c.MigrationNotes[0], "left unchanged")

	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
	require.Len(t, doc["excludes"], 0)
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

func TestFirmlinkExcludesFor(t *testing.T) {
	require.Equal(t, []string{"/System/Volumes/Data"}, firmlinkExcludesFor("darwin"))
	require.Nil(t, firmlinkExcludesFor("linux"), "the firmlink dedup is a macOS-only concept")
	require.Nil(t, firmlinkExcludesFor("windows"))
}

// The TestMigrateV4* tests drive migrateRootsFor directly with a
// pinned goos, so the darwin shape is covered headlessly on every CI
// job (the defaultRootsFor convention).

func TestMigrateV4DarwinDefaultShapedGainsFirmlinkExclude(t *testing.T) {
	before := append(append(baseExcludes(), noiseExcludes()...), systemExcludesFor("darwin")...)
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 3,
		Excludes:     append([]string{}, before...),
	}
	require.True(t, c.migrateRootsFor("darwin"), "a v3 config is rewritten")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, append(before, "/System/Volumes/Data"), c.Excludes,
		"the firmlink exclude is appended after everything the config already had")
	require.Len(t, c.MigrationNotes, 1)
	require.Contains(t, c.MigrationNotes[0], "macOS firmlink exclude added: /System/Volumes/Data")
	require.Contains(t, c.MigrationNotes[0], "remove it in config.json",
		"the note names the way back for someone who truly wants the Data volume indexed raw")

	require.False(t, c.migrateRootsFor("darwin"), "the migration is idempotent once stamped")
}

func TestMigrateV4DarwinCuratedStampOnly(t *testing.T) {
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 3,
		Excludes:     []string{"node_modules", "/proc"}, // base patterns curated away
	}
	require.True(t, c.migrateRootsFor("darwin"))
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, []string{"node_modules", "/proc"}, c.Excludes,
		"a curated exclude list is never extended")
	require.Len(t, c.MigrationNotes, 1)
	require.Contains(t, c.MigrationNotes[0], "/System/Volumes/Data")
	require.Contains(t, c.MigrationNotes[0], "your customized exclude list was left unchanged")
}

func TestMigrateV4NonDarwinStampOnly(t *testing.T) {
	before := append(append(baseExcludes(), noiseExcludes()...), systemExcludesFor("linux")...)
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 3,
		Excludes:     append([]string{}, before...),
	}
	require.True(t, c.migrateRootsFor("linux"), "the stamp still advances")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, before, c.Excludes, "no firmlink exclude exists off macOS")
	require.Empty(t, c.MigrationNotes, "nothing user-visible changed, so nothing is announced")
}

func TestMigrateV4AlreadyCurrentIsNoOp(t *testing.T) {
	c := &Config{Roots: []string{"/"}, RootsVersion: currentRootsVersion, Excludes: []string{".git"}}
	require.False(t, c.migrateRootsFor("darwin"))
	require.Empty(t, c.MigrationNotes)
	require.Equal(t, []string{".git"}, c.Excludes)
}

// TestMigrateLegacyDarwinFullChain pins the whole v0 -> v4 ladder on
// the darwin shape: roots move to the whole-filesystem default, then
// the system, noise, and firmlink excludes are appended in that
// order, each with its own note.
func TestMigrateLegacyDarwinFullChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &Config{
		Roots:    []string{home},
		Excludes: append([]string{}, baseExcludes()...),
	}
	require.True(t, c.migrateRootsFor("darwin"))
	require.Equal(t, []string{"/"}, c.Roots)
	require.Equal(t,
		append(append(append(baseExcludes(), systemExcludesFor("darwin")...), noiseExcludes()...), "/System/Volumes/Data"),
		c.Excludes)
	require.Len(t, c.MigrationNotes, 4)
	require.Contains(t, c.MigrationNotes[3], "macOS firmlink exclude added")
}

// TestMigrateV3StepDoesNotRerunOnV3Configs pins the version gate: a
// config already stamped 3 with a curated exclude list must NOT get
// the v3 informational note again when the v4 bump rewrites it.
func TestMigrateV3StepDoesNotRerunOnV3Configs(t *testing.T) {
	c := &Config{Roots: []string{"/"}, RootsVersion: 3, Excludes: []string{"node_modules"}}
	require.True(t, c.migrateRootsFor("linux"))
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Empty(t, c.MigrationNotes,
		"the v3 note fired when the config was stamped 3; the v4 bump must not repeat it")
}

func TestLegacyDefaultRootsFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	roots := legacyDefaultRoots()
	require.Len(t, roots, 1)
	require.True(t, filepath.IsAbs(roots[0]), "fallback root is the absolutized cwd")
}
