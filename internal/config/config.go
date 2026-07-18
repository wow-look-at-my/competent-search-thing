// Package config loads and saves the application's JSON configuration
// file. Loading never crashes the app: a missing file is created with
// defaults, and a corrupt file falls back to defaults while surfacing
// the parse error for the caller to log.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvConfigDir overrides the directory containing config.json (used by
// tests and portable installs). When set, the file lives directly at
// $COMPETENT_SEARCH_CONFIG_DIR/config.json; otherwise it lives at
// os.UserConfigDir()/competent-search-thing/config.json.
const EnvConfigDir = "COMPETENT_SEARCH_CONFIG_DIR"

const (
	appDirName = "competent-search-thing"
	fileName   = "config.json"

	// DefaultHotkey summons the searchbar.
	DefaultHotkey = "alt+space"
	// DefaultMaxResults caps one query's result list.
	DefaultMaxResults = 50
	// DefaultTheme is the builtin theme used when none is configured.
	DefaultTheme = "dark"
)

// Firefox frequent-sites defaults (see FrequentSitesConfig). The
// visit thresholds encode the feature's frequency rule: a page
// visited MORE THAN 10 times in the past 30 days (>= 11) AND at least
// once in the past 7 days.
const (
	DefaultFirefoxMinVisitsMonth = 11
	DefaultFirefoxMinVisitsWeek  = 1
	DefaultFirefoxRefreshMinutes = 10
	DefaultFirefoxMaxResults     = 6
)

// DefaultFirefoxTabsMaxResults caps one Open Tabs section (see
// OpenTabsConfig).
const DefaultFirefoxTabsMaxResults = 6

// Config is the on-disk configuration.
type Config struct {
	// Roots are the directories to index. The default is the whole
	// filesystem ("/" on Linux/macOS, the system drive on Windows).
	Roots []string `json:"roots"`
	// RootsVersion stamps which roots-defaults generation wrote this
	// config; 0 (or absent) marks a legacy home-directory-default
	// config, which Load migrates (see migrateRoots) and rewrites.
	RootsVersion int `json:"rootsVersion"`
	// Excludes are walk exclude patterns: a bare pattern matches base
	// names ("node_modules", "*.tmp"); a pattern with a separator
	// matches full paths. See internal/index.Excluder.
	Excludes []string `json:"excludes"`
	// Hotkey is the global summon hotkey (used by the platform phase).
	Hotkey string `json:"hotkey"`
	// RescanIntervalMinutes triggers periodic full rescans; 0 disables.
	RescanIntervalMinutes int `json:"rescanIntervalMinutes"`
	// MaxResults caps one query's result list.
	MaxResults int `json:"maxResults"`
	// Search configures the search engine behavior.
	Search SearchConfig `json:"search"`
	// Watcher configures the live-watch layer that keeps the index
	// fresh between rescans (see internal/watch).
	Watcher WatcherConfig `json:"watcher"`
	// Theme names the UI theme: a builtin ("dark", "light") or a user
	// theme file at <configDir>/themes/<name>.json (see internal/theme).
	// Unknown or invalid themes fall back to dark at resolve time.
	Theme string `json:"theme"`
	// Plugins configures the plugin system (see internal/plugin).
	Plugins PluginsConfig `json:"plugins"`
	// Bangs configures bang parsing (sigils and aliases).
	Bangs BangsConfig `json:"bangs"`
	// Tray configures the tray icon (see internal/tray).
	Tray TrayConfig `json:"tray"`
	// History configures the query history behind the bar's Up/Down
	// recall (see internal/history).
	History HistoryConfig `json:"history"`
	// Window configures the native window layer (read by main.go
	// before the Wails runtime starts).
	Window WindowConfig `json:"window"`
	// Firefox configures the Firefox history integration (see
	// internal/firefox).
	Firefox FirefoxConfig `json:"firefox"`

	// MigrationNotes describes, in human-readable lines, what the
	// roots migration changed on this Load (empty when nothing did).
	// Never serialized; the app logs each line loudly at startup.
	MigrationNotes []string `json:"-"`
}

// PluginsConfig configures the plugin system. The zero value means
// "plugins enabled, nothing overridden".
type PluginsConfig struct {
	// Disabled turns the whole plugin system off.
	Disabled bool `json:"disabled"`
	// Entries holds per-plugin overrides keyed by plugin id (builtin
	// provider ids work here too).
	Entries map[string]PluginEntry `json:"entries"`
}

// PluginEntry is one plugin's configuration.
type PluginEntry struct {
	// Disabled turns this one plugin off.
	Disabled bool `json:"disabled"`
	// Settings is an opaque JSON object forwarded verbatim to the
	// plugin in every request.
	Settings json.RawMessage `json:"settings,omitempty"`
}

// SearchConfig configures the search engine. The zero value means the
// default behavior: fuzzy matching on.
type SearchConfig struct {
	// FuzzyDisabled true turns the fuzzy (subsequence) name-match tier
	// off, leaving exact/prefix/substring matching only. The zero
	// value -- the default -- keeps fuzzy matching on (matching the
	// tray.disabled convention). Exact, prefix, and substring matches
	// always rank above fuzzy ones either way.
	FuzzyDisabled bool `json:"fuzzyDisabled"`
}

// WatcherConfig configures the live-watch layer (see internal/watch):
// the bounded hot set of per-directory watches and the always-on
// reconcile sweeps that converge everything the hot set does not
// cover. The zero value means all defaults -- automatic watch budget,
// the built-in sweep cadence, nothing watch-excluded -- matching the
// tray.disabled convention.
type WatcherConfig struct {
	// MaxWatches bounds the hot set of live per-directory watches.
	// 0 (the default) resolves the budget automatically: half of the
	// kernel's per-user inotify watch allowance, capped at 65536.
	// Negative means explicitly unlimited (watch every indexed
	// directory, the pre-budget behavior); positive values are taken
	// as-is. Irrelevant while the fanotify whole-filesystem backend is
	// active (it needs no per-directory watches).
	MaxWatches int `json:"maxWatches"`
	// SweepMinutes is the interval between reconcile sweep passes --
	// the convergence bound for directories without a live watch.
	// 0 (the default) selects the built-in 20-minute cadence;
	// negative values are repaired to 0 by Normalize.
	SweepMinutes int `json:"sweepMinutes"`
	// SweepDisabled true turns the sweep tier off entirely. The zero
	// value -- the default -- keeps sweeps on (the tray.disabled
	// convention). With sweeps off, directories without a live watch
	// converge only at full rescans (!rescan or
	// rescanIntervalMinutes); the app logs a loud warning saying so.
	SweepDisabled bool `json:"sweepDisabled"`
	// WatchExcludes are exclude patterns (the excludes syntax, see
	// internal/index.Excluder) applied ONLY to live watching: matching
	// directories never get a per-directory watch, but they are still
	// indexed and still swept, so changes inside them appear within
	// one sweep interval instead of ~1s. Use it to keep high-churn
	// trees you still want searchable from consuming watch budget.
	WatchExcludes []string `json:"watchExcludes,omitempty"`
}

// BangsConfig configures the bang system.
type BangsConfig struct {
	// Sigils are the characters that may start a bang query; empty
	// means the defaults (see DefaultBangSigils).
	Sigils []string `json:"sigils"`
	// Aliases map extra names onto registered bangs.
	Aliases map[string]string `json:"aliases"`
}

// TrayConfig configures the tray icon (StatusNotifierItem). The zero
// value -- the default -- means enabled: the icon degrades away by
// itself on sessions without a StatusNotifierItem host, so only users
// who actively dislike it need the switch.
type TrayConfig struct {
	// Disabled turns the tray icon off.
	Disabled bool `json:"disabled"`
}

// WindowConfig configures the native window layer.
type WindowConfig struct {
	// Translucent true requests a per-pixel-alpha (RGBA) window so
	// the area outside the bar's rounded corners is truly see-through
	// instead of a squared-off opaque fill. It needs a running
	// compositor; on an X11 session without one the corners render
	// solid black, which is why the zero value -- the default --
	// keeps the window opaque (current behavior).
	Translucent bool `json:"translucent"`
}

// HistoryConfig configures the query history (see internal/history).
type HistoryConfig struct {
	// PersistDisabled true keeps the history in memory only: nothing
	// is read from or written to <configDir>/history.json, while
	// in-session Up/Down recall keeps working. The zero value keeps
	// persistence on (matching the tray.disabled convention).
	PersistDisabled bool `json:"persistDisabled"`
}

// FirefoxConfig configures the Firefox integrations.
type FirefoxConfig struct {
	// FrequentSites configures the frequently-visited-sites result
	// section (the builtin firefox-frequent provider; disable it via
	// plugins.entries["firefox-frequent"].disabled).
	FrequentSites FrequentSitesConfig `json:"frequentSites"`
	// OpenTabs configures the open-tabs result section (the builtin
	// firefox-tabs provider; disable it via
	// plugins.entries["firefox-tabs"].disabled).
	OpenTabs OpenTabsConfig `json:"openTabs"`
}

// FrequentSitesConfig tunes which history entries count as "frequent"
// and how they are served. Zero or negative numeric values are
// repaired to the defaults by Normalize.
type FrequentSitesConfig struct {
	// MinVisitsMonth is the minimum number of visits in the past 30
	// days (default 11, i.e. "more than 10 times").
	MinVisitsMonth int `json:"minVisitsMonth"`
	// MinVisitsWeek is the minimum number of visits in the past 7 days
	// (default 1).
	MinVisitsWeek int `json:"minVisitsWeek"`
	// RefreshMinutes is how old the cached site list may get before a
	// query kicks a background re-read of the history (default 10).
	RefreshMinutes int `json:"refreshMinutes"`
	// MaxResults caps one frequent-sites response (default 6).
	MaxResults int `json:"maxResults"`
	// ProfileDir, when non-empty, bypasses profile discovery and reads
	// this Firefox profile directory's places.sqlite directly.
	ProfileDir string `json:"profileDir"`
}

// OpenTabsConfig tunes the open-Firefox-tabs result section. Zero or
// negative numeric values are repaired to the defaults by Normalize.
// The freshness cadence is fixed: the section re-reads the session
// snapshot when its mtime changes or after ~15s, matching how often
// Firefox rewrites it.
type OpenTabsConfig struct {
	// MaxResults caps one Open Tabs response (default 6).
	MaxResults int `json:"maxResults"`
	// ProfileDir, when non-empty, bypasses profile discovery and reads
	// this Firefox profile directory's session snapshot directly (same
	// semantics as frequentSites.profileDir; empty = the shared
	// discovery).
	ProfileDir string `json:"profileDir"`
}

// DefaultFirefox returns the default Firefox integration config.
func DefaultFirefox() FirefoxConfig {
	return FirefoxConfig{
		FrequentSites: FrequentSitesConfig{
			MinVisitsMonth: DefaultFirefoxMinVisitsMonth,
			MinVisitsWeek:  DefaultFirefoxMinVisitsWeek,
			RefreshMinutes: DefaultFirefoxRefreshMinutes,
			MaxResults:     DefaultFirefoxMaxResults,
		},
		OpenTabs: OpenTabsConfig{
			MaxResults: DefaultFirefoxTabsMaxResults,
		},
	}
}

// DefaultBangSigils returns the default bang sigil set. It returns a
// fresh slice on every call so callers may modify it safely.
func DefaultBangSigils() []string { return []string{"!", "/", "@"} }

// Default returns the default configuration: index the whole
// filesystem, Everything-style ("/" on Linux/macOS, the system drive
// on Windows), skip the virtual/volatile system trees plus the usual
// noise (see migrate.go), no periodic rescan.
func Default() Config {
	return Config{
		Roots:                 defaultRoots(),
		RootsVersion:          currentRootsVersion,
		Excludes:              defaultExcludes(),
		Hotkey:                DefaultHotkey,
		RescanIntervalMinutes: 0,
		MaxResults:            DefaultMaxResults,
		Theme:                 DefaultTheme,
		Plugins:               PluginsConfig{Entries: map[string]PluginEntry{}},
		Bangs:                 BangsConfig{Sigils: DefaultBangSigils(), Aliases: map[string]string{}},
		Firefox:               DefaultFirefox(),
	}
}

// Dir returns the directory holding the configuration (config.json,
// the plugins/ subdirectory, and the themes/ directory with user theme
// JSON files and the custom.css escape hatch, see internal/theme),
// consistent with Path.
func Dir() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: resolving user config dir: %w", err)
	}
	return filepath.Join(base, appDirName), nil
}

// Path returns the resolved location of the config file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the config file. A missing file is created with defaults
// (mkdir -p included). A file stamped with an older rootsVersion is
// migrated to the current defaults (see migrateRoots) and rewritten
// once, with the changes reported in the returned Config's
// MigrationNotes. On any error --
// unresolvable path, unreadable or corrupt file, failed default write
// or migration rewrite -- Load still returns a usable config alongside
// the error; callers log the error and keep going, they never crash.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		c := Default()
		if werr := Save(c); werr != nil {
			return c, werr
		}
		return c, nil
	}
	if err != nil {
		return Default(), fmt.Errorf("config: reading %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Default(), fmt.Errorf("config: parsing %s: %w", p, err)
	}
	migrated := c.migrateRoots()
	c.Normalize()
	if migrated {
		if werr := Save(c); werr != nil {
			return c, fmt.Errorf("config: persisting the roots migration: %w", werr)
		}
	}
	return c, nil
}

// Save writes the config file, creating the directory as needed.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("config: creating %s: %w", filepath.Dir(p), err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("config: writing %s: %w", p, err)
	}
	return nil
}

// Normalize repairs missing or nonsensical fields in place: empty roots
// fall back to the default root, relative roots are absolutized,
// zero/negative knobs get their defaults (the firefox.frequentSites
// and firefox.openTabs numbers included; a negative
// watcher.sweepMinutes becomes 0 = the built-in cadence), an empty
// theme name gets the default theme, nil
// plugin entries and bang aliases become empty maps, and an empty
// sigil list gets the default sigils. Excludes are left as the user
// wrote them (an explicitly empty list means "exclude nothing"), and
// so are watcher.watchExcludes and watcher.maxWatches (negative =
// explicitly unlimited).
func (c *Config) Normalize() {
	if len(c.Roots) == 0 {
		c.Roots = Default().Roots
	}
	roots := c.Roots[:0]
	for _, r := range c.Roots {
		if r == "" {
			continue
		}
		if abs, err := filepath.Abs(r); err == nil {
			r = abs
		}
		roots = append(roots, r)
	}
	if len(roots) == 0 {
		roots = Default().Roots
	}
	c.Roots = roots
	if c.Hotkey == "" {
		c.Hotkey = DefaultHotkey
	}
	if c.RescanIntervalMinutes < 0 {
		c.RescanIntervalMinutes = 0
	}
	if c.Watcher.SweepMinutes < 0 {
		c.Watcher.SweepMinutes = 0
	}
	if c.MaxResults <= 0 {
		c.MaxResults = DefaultMaxResults
	}
	if c.Theme == "" {
		c.Theme = DefaultTheme
	}
	if c.Plugins.Entries == nil {
		c.Plugins.Entries = map[string]PluginEntry{}
	}
	if len(c.Bangs.Sigils) == 0 {
		c.Bangs.Sigils = DefaultBangSigils()
	}
	if c.Bangs.Aliases == nil {
		c.Bangs.Aliases = map[string]string{}
	}
	fs := &c.Firefox.FrequentSites
	if fs.MinVisitsMonth <= 0 {
		fs.MinVisitsMonth = DefaultFirefoxMinVisitsMonth
	}
	if fs.MinVisitsWeek <= 0 {
		fs.MinVisitsWeek = DefaultFirefoxMinVisitsWeek
	}
	if fs.RefreshMinutes <= 0 {
		fs.RefreshMinutes = DefaultFirefoxRefreshMinutes
	}
	if fs.MaxResults <= 0 {
		fs.MaxResults = DefaultFirefoxMaxResults
	}
	if c.Firefox.OpenTabs.MaxResults <= 0 {
		c.Firefox.OpenTabs.MaxResults = DefaultFirefoxTabsMaxResults
	}
}
