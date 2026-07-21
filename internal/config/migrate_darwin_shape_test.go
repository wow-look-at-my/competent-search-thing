package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.True(t, c.migrateRootsFor("darwin", nil), "a v3 config is rewritten")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t,
		append(append(before, "/System/Volumes/Data"), darwinNoiseExcludesFor("darwin")...),
		c.Excludes,
		"the firmlink exclude, then the v5 noise set, are appended after everything the config already had")
	require.Len(t, c.MigrationNotes, 5)
	require.Contains(t, c.MigrationNotes[0], "macOS firmlink exclude added: /System/Volumes/Data")
	require.Contains(t, c.MigrationNotes[0], "remove it in config.json",
		"the note names the way back for someone who truly wants the Data volume indexed raw")
	require.Contains(t, c.MigrationNotes[1], "macOS noise exclude patterns added")

	require.False(t, c.migrateRootsFor("darwin", nil), "the migration is idempotent once stamped")
}

func TestMigrateV4DarwinCuratedStampOnly(t *testing.T) {
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 3,
		Excludes:     []string{"node_modules", "/proc"}, // base patterns curated away
	}
	require.True(t, c.migrateRootsFor("darwin", nil))
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, []string{"node_modules", "/proc"}, c.Excludes,
		"a curated exclude list is never extended")
	require.Len(t, c.MigrationNotes, 5)
	require.Contains(t, c.MigrationNotes[0], "/System/Volumes/Data")
	require.Contains(t, c.MigrationNotes[0], "your customized exclude list was left unchanged")
	require.Contains(t, c.MigrationNotes[1], "your customized exclude list was left unchanged")
}

func TestMigrateV4NonDarwinStampOnly(t *testing.T) {
	before := append(append(baseExcludes(), noiseExcludes()...), systemExcludesFor("linux")...)
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 3,
		Excludes:     append([]string{}, before...),
	}
	require.True(t, c.migrateRootsFor("linux", nil), "the stamp still advances")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, before, c.Excludes, "no firmlink exclude exists off macOS")
	require.Len(t, c.MigrationNotes, 3, "the two v6 ranking-defaults notes plus the v8 preview note fire")
	require.Contains(t, c.MigrationNotes[0], "ranking telemetry is now always on")
	require.Contains(t, c.MigrationNotes[1], "on by default")
}

func TestMigrateV4AlreadyCurrentIsNoOp(t *testing.T) {
	c := &Config{Roots: []string{"/"}, RootsVersion: currentRootsVersion, Excludes: []string{".git"}}
	require.False(t, c.migrateRootsFor("darwin", nil))
	require.Empty(t, c.MigrationNotes)
	require.Equal(t, []string{".git"}, c.Excludes)
}

// TestMigrateLegacyDarwinFullChain pins the whole v0 -> v5 ladder on
// the darwin shape: roots move to the whole-filesystem default, then
// the system, noise, firmlink, and darwin-noise excludes are appended
// in that order, each with its own note.
func TestMigrateLegacyDarwinFullChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &Config{
		Roots:    []string{home},
		Excludes: append([]string{}, baseExcludes()...),
	}
	require.True(t, c.migrateRootsFor("darwin", nil))
	require.Equal(t, []string{"/"}, c.Roots)
	want := append(append(append(baseExcludes(), systemExcludesFor("darwin")...), noiseExcludes()...), "/System/Volumes/Data")
	want = append(want, darwinNoiseExcludesFor("darwin")...)
	require.Equal(t, want, c.Excludes)
	require.Len(t, c.MigrationNotes, 8)
	require.Contains(t, c.MigrationNotes[3], "macOS firmlink exclude added")
	require.Contains(t, c.MigrationNotes[4], "macOS noise exclude patterns added")
	require.Contains(t, c.MigrationNotes[5], "ranking telemetry is now always on")
	require.Contains(t, c.MigrationNotes[6], "on by default")
	require.Contains(t, c.MigrationNotes[7], "preview pane is now ON by default", "the v8 preview note lands last")
}

func TestDarwinNoiseExcludesFor(t *testing.T) {
	require.Equal(t,
		[]string{"Caches", "DerivedData", "_CodeSignature", "CodeResources", "/private/var/folders"},
		darwinNoiseExcludesFor("darwin"))
	require.Nil(t, darwinNoiseExcludesFor("linux"), "the darwin noise set is macOS-only")
	require.Nil(t, darwinNoiseExcludesFor("windows"))
}

// The TestMigrateV5* tests drive migrateRootsFor directly with a
// pinned goos (the TestMigrateV4* convention). A v4-stamped config is
// the interesting starting point: users of a v4-era build already
// carry the firmlink exclude and stamp 4, and the v5 step must fire
// for them WITHOUT repeating the v4 step -- the reason this is a new
// version rather than an extension of v4.
func TestMigrateV5DarwinDefaultShapedGainsNoiseExcludes(t *testing.T) {
	before := append(append(append(baseExcludes(), noiseExcludes()...),
		systemExcludesFor("darwin")...), firmlinkExcludesFor("darwin")...)
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 4,
		Excludes:     append([]string{}, before...),
	}
	require.True(t, c.migrateRootsFor("darwin", nil), "a v4 config is rewritten")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, append(before, darwinNoiseExcludesFor("darwin")...), c.Excludes,
		"the darwin noise set is appended after everything the config already had")
	require.Len(t, c.MigrationNotes, 4, "the v4 firmlink note must NOT repeat")
	require.Contains(t, c.MigrationNotes[0], "macOS noise exclude patterns added: Caches, DerivedData, _CodeSignature, CodeResources, /private/var/folders")
	require.Contains(t, c.MigrationNotes[0], "remove any of them in config.json")

	require.False(t, c.migrateRootsFor("darwin", nil), "the migration is idempotent once stamped")
}

func TestMigrateV5DarwinPartialAppendsOnlyMissing(t *testing.T) {
	// The user already excludes DerivedData themselves: only the
	// missing patterns are appended and DerivedData is not announced.
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 4,
		Excludes:     append(baseExcludes(), "DerivedData"),
	}
	require.True(t, c.migrateRootsFor("darwin", nil))
	require.Equal(t,
		append(append(baseExcludes(), "DerivedData"),
			"Caches", "_CodeSignature", "CodeResources", "/private/var/folders"),
		c.Excludes, "the user's DerivedData keeps its position; only missing patterns append")
	require.Len(t, c.MigrationNotes, 4)
	require.NotContains(t, c.MigrationNotes[0], "DerivedData,",
		"an already-present pattern is not announced as added")
}

func TestMigrateV5DarwinCuratedStampOnly(t *testing.T) {
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 4,
		Excludes:     []string{"node_modules"}, // base patterns curated away
	}
	require.True(t, c.migrateRootsFor("darwin", nil))
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, []string{"node_modules"}, c.Excludes,
		"a curated exclude list is never extended")
	require.Len(t, c.MigrationNotes, 4)
	require.Contains(t, c.MigrationNotes[0], "macOS cache/derived/temp noise set")
	require.Contains(t, c.MigrationNotes[0], "your customized exclude list was left unchanged")
}

func TestMigrateV5NonDarwinStampOnly(t *testing.T) {
	before := append(append(baseExcludes(), noiseExcludes()...), systemExcludesFor("linux")...)
	c := &Config{
		Roots:        []string{"/"},
		RootsVersion: 4,
		Excludes:     append([]string{}, before...),
	}
	require.True(t, c.migrateRootsFor("linux", nil), "the stamp still advances")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, before, c.Excludes, "no darwin noise set exists off macOS")
	require.Len(t, c.MigrationNotes, 3, "the two v6 ranking-defaults notes plus the v8 preview note fire")
}

// TestMigrateV3StepDoesNotRerunOnV3Configs pins the version gate: a
// config already stamped 3 with a curated exclude list must NOT get
// the v3 informational note again when the v4 bump rewrites it.
func TestMigrateV3StepDoesNotRerunOnV3Configs(t *testing.T) {
	c := &Config{Roots: []string{"/"}, RootsVersion: 3, Excludes: []string{"node_modules"}}
	require.True(t, c.migrateRootsFor("linux", nil))
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Len(t, c.MigrationNotes, 3,
		"the v3 note fired when the config was stamped 3; the v6 ranking notes and the v8 preview note are new")
	require.Contains(t, c.MigrationNotes[0], "ranking telemetry is now always on")
	require.Contains(t, c.MigrationNotes[1], "on by default")
}

func TestLegacyDefaultRootsFallsBackWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	roots := legacyDefaultRoots()
	require.Len(t, roots, 1)
	require.True(t, filepath.IsAbs(roots[0]), "fallback root is the absolutized cwd")
}
