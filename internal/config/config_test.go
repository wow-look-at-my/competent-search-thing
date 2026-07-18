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
	// Honored by os.UserConfigDir on linux; darwin/windows ignore XDG
	// and resolve their native dir, so the expectation is derived from
	// os.UserConfigDir itself -- the documented contract is "under
	// os.UserConfigDir()", not "under XDG_CONFIG_HOME".
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base, err := os.UserConfigDir()
	require.NoError(t, err)
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, appDirName, "config.json"), p)
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

func TestDefaultRootsAreWholeFilesystem(t *testing.T) {
	// Default() no longer depends on the home directory at all: the
	// default scope is the whole filesystem (Everything-style).
	t.Setenv("HOME", "")
	c := Default()
	require.Equal(t, []string{"/"}, c.Roots, "linux/darwin default root is the filesystem root")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t,
		[]string{".git", "node_modules", ".cache", "/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"},
		c.Excludes, "defaults carry the system excludes on unix-likes")
}

func TestDirUsesEnvOverride(t *testing.T) {
	dir := setConfigDir(t)
	got, err := Dir()
	require.NoError(t, err)
	require.Equal(t, dir, got)

	// Path stays consistent with Dir.
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(got, "config.json"), p)
}

func TestDirDefaultsToUserConfigDir(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	// Same as TestPathDefaultsToUserConfigDir: XDG_CONFIG_HOME only
	// steers os.UserConfigDir on linux, so compare against the real
	// os.UserConfigDir value instead of the env var.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base, err := os.UserConfigDir()
	require.NoError(t, err)
	got, err := Dir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(base, appDirName), got)

	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(got, "config.json"), p)
}

func TestDirErrorWhenUnresolvable(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	_, err := Dir()
	require.Error(t, err)
}

func TestPluginsAndBangsRoundTrip(t *testing.T) {
	setConfigDir(t)
	in := Default()
	in.Plugins = PluginsConfig{
		Disabled: true,
		Entries: map[string]PluginEntry{
			"calc": {Disabled: true, Settings: json.RawMessage(`{"precision": 2, "mode": "deg"}`)},
			"ps":   {},
		},
	}
	in.Bangs = BangsConfig{
		Sigils:  []string{"!", "$"},
		Aliases: map[string]string{"add": "calc"},
	}
	require.NoError(t, Save(in))

	got, err := Load()
	require.NoError(t, err)
	require.True(t, got.Plugins.Disabled)
	require.Len(t, got.Plugins.Entries, 2)
	require.True(t, got.Plugins.Entries["calc"].Disabled)
	require.JSONEq(t, `{"precision": 2, "mode": "deg"}`, string(got.Plugins.Entries["calc"].Settings),
		"settings round-trip as an opaque JSON object")
	require.False(t, got.Plugins.Entries["ps"].Disabled)
	require.Nil(t, got.Plugins.Entries["ps"].Settings)
	require.Equal(t, []string{"!", "$"}, got.Bangs.Sigils)
	require.Equal(t, map[string]string{"add": "calc"}, got.Bangs.Aliases)
}

func TestLoadOldConfigWithoutNewSections(t *testing.T) {
	dir := setConfigDir(t)
	old := `{
		"roots": ["/data"],
		"excludes": [".git"],
		"hotkey": "alt+space",
		"rescanIntervalMinutes": 0,
		"maxResults": 50
	}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o644))

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, []string{"/data"}, c.Roots, "old fields still load")
	require.False(t, c.Plugins.Disabled)
	require.NotNil(t, c.Plugins.Entries, "missing plugins section normalized")
	require.Empty(t, c.Plugins.Entries)
	require.Equal(t, DefaultBangSigils(), c.Bangs.Sigils, "missing bangs section gets default sigils")
	require.NotNil(t, c.Bangs.Aliases)
	require.Empty(t, c.Bangs.Aliases)
}

func TestNormalizePluginsAndBangs(t *testing.T) {
	var c Config
	c.Normalize()
	require.NotNil(t, c.Plugins.Entries)
	require.Empty(t, c.Plugins.Entries)
	require.Equal(t, DefaultBangSigils(), c.Bangs.Sigils)
	require.NotNil(t, c.Bangs.Aliases)
	require.Empty(t, c.Bangs.Aliases)

	// Existing values survive normalization untouched.
	c2 := Config{
		Plugins: PluginsConfig{Entries: map[string]PluginEntry{"x": {Disabled: true}}},
		Bangs:   BangsConfig{Sigils: []string{"$"}, Aliases: map[string]string{"a": "b"}},
	}
	c2.Normalize()
	require.Equal(t, map[string]PluginEntry{"x": {Disabled: true}}, c2.Plugins.Entries)
	require.Equal(t, []string{"$"}, c2.Bangs.Sigils)
	require.Equal(t, map[string]string{"a": "b"}, c2.Bangs.Aliases)
}

func TestTrayConfig(t *testing.T) {
	setConfigDir(t)
	require.False(t, Default().Tray.Disabled, "the tray is on by default")

	// A config predating the tray block loads as enabled...
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.False(t, c.Tray.Disabled)

	// ...and an explicit opt-out round-trips.
	in := Default()
	in.Tray.Disabled = true
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.True(t, got.Tray.Disabled)
}

func TestWindowConfig(t *testing.T) {
	setConfigDir(t)
	require.False(t, Default().Window.Translucent, "the window is opaque by default")

	// A config predating the window block loads as opaque...
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.False(t, c.Window.Translucent)

	// ...and an explicit opt-in round-trips.
	in := Default()
	in.Window.Translucent = true
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.True(t, got.Window.Translucent)
}

func TestHistoryConfig(t *testing.T) {
	setConfigDir(t)
	require.False(t, Default().History.PersistDisabled, "history persistence is on by default")

	// A config predating the history block loads as persisting...
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.False(t, c.History.PersistDisabled)

	// ...and an explicit opt-out round-trips.
	in := Default()
	in.History.PersistDisabled = true
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.True(t, got.History.PersistDisabled)
}

func TestDefaultBangSigilsReturnsFreshSlice(t *testing.T) {
	a := DefaultBangSigils()
	a[0] = "X"
	require.Equal(t, []string{"!", "/", "@"}, DefaultBangSigils())
}

func TestFirefoxConfig(t *testing.T) {
	setConfigDir(t)
	require.Equal(t, FirefoxConfig{
		FrequentSites: FrequentSitesConfig{
			MinVisitsMonth: 11,
			MinVisitsWeek:  1,
			RefreshMinutes: 10,
			MaxResults:     6,
		},
		OpenTabs: OpenTabsConfig{MaxResults: 6},
	}, Default().Firefox, "the defaults encode 'more than 10 in 30 days AND once in 7'")

	// A config predating the firefox block normalizes to the defaults.
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.Equal(t, DefaultFirefox(), c.Firefox)

	// Zero and negative knobs are repaired; real values survive.
	c = Config{Firefox: FirefoxConfig{
		FrequentSites: FrequentSitesConfig{
			MinVisitsMonth: -3,
			MinVisitsWeek:  0,
			RefreshMinutes: 42,
			MaxResults:     9,
			ProfileDir:     "/custom/profile",
		},
		OpenTabs: OpenTabsConfig{MaxResults: -1, ProfileDir: "/tabs/profile"},
	}}
	c.Normalize()
	require.Equal(t, DefaultFirefoxMinVisitsMonth, c.Firefox.FrequentSites.MinVisitsMonth)
	require.Equal(t, DefaultFirefoxMinVisitsWeek, c.Firefox.FrequentSites.MinVisitsWeek)
	require.Equal(t, 42, c.Firefox.FrequentSites.RefreshMinutes)
	require.Equal(t, 9, c.Firefox.FrequentSites.MaxResults)
	require.Equal(t, "/custom/profile", c.Firefox.FrequentSites.ProfileDir, "the override is never touched")
	require.Equal(t, DefaultFirefoxTabsMaxResults, c.Firefox.OpenTabs.MaxResults)
	require.Equal(t, "/tabs/profile", c.Firefox.OpenTabs.ProfileDir, "the override is never touched")

	// Real openTabs values survive Normalize.
	c = Config{Firefox: FirefoxConfig{OpenTabs: OpenTabsConfig{MaxResults: 12}}}
	c.Normalize()
	require.Equal(t, 12, c.Firefox.OpenTabs.MaxResults)

	// The block round-trips through Save/Load.
	in := Default()
	in.Firefox.FrequentSites.MinVisitsMonth = 20
	in.Firefox.FrequentSites.ProfileDir = "/custom/profile"
	in.Firefox.OpenTabs.MaxResults = 3
	in.Firefox.OpenTabs.ProfileDir = "/tabs/profile"
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.Equal(t, 20, got.Firefox.FrequentSites.MinVisitsMonth)
	require.Equal(t, "/custom/profile", got.Firefox.FrequentSites.ProfileDir)
	require.Equal(t, 3, got.Firefox.OpenTabs.MaxResults)
	require.Equal(t, "/tabs/profile", got.Firefox.OpenTabs.ProfileDir)
}

func TestFrecencyConfig(t *testing.T) {
	setConfigDir(t)
	require.Equal(t, FrecencyConfig{
		HalfLifeDays:   14,
		WeightFrecency: 1.0,
		WeightRecency:  1.0,
		WeightCwd:      1.0,
		WeightNoise:    1.0,
		TierJumpCount:  3.0,
	}, Default().Search.Frecency)

	// A config predating the frecency block normalizes to the defaults.
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.Equal(t, DefaultFrecency(), c.Search.Frecency)

	// Exact zeros are repaired; NEGATIVE values are the documented
	// per-signal off switch and pass through untouched -- except the
	// half-life, which has no off meaning and repairs on <= 0.
	c = Config{Search: SearchConfig{Frecency: FrecencyConfig{
		Disabled:       true,
		HalfLifeDays:   -2,
		WeightFrecency: 0,
		WeightRecency:  -1,
		WeightCwd:      2.5,
		WeightNoise:    0,
		TierJumpCount:  -1,
	}}}
	c.Normalize()
	fr := c.Search.Frecency
	require.True(t, fr.Disabled, "the kill switch is never touched")
	require.Equal(t, float64(DefaultFrecencyHalfLifeDays), fr.HalfLifeDays)
	require.Equal(t, DefaultFrecencyWeight, fr.WeightFrecency)
	require.Equal(t, -1.0, fr.WeightRecency, "negative weight = off, preserved")
	require.Equal(t, 2.5, fr.WeightCwd)
	require.Equal(t, DefaultFrecencyWeight, fr.WeightNoise)
	require.Equal(t, -1.0, fr.TierJumpCount, "negative tier jump = off, preserved")

	// The block round-trips through Save/Load.
	in := Default()
	in.Search.Frecency.HalfLifeDays = 7
	in.Search.Frecency.TierJumpCount = 5
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.Equal(t, 7.0, got.Search.Frecency.HalfLifeDays)
	require.Equal(t, 5.0, got.Search.Frecency.TierJumpCount)
}
