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

// Frecency ranking defaults (see FrecencyConfig). The weights share
// one scale: at 1.0 each, one recently recorded open, a
// just-touched file's full recency score, a direct child of the
// focused app's working directory, and the maximum location-noise
// penalty all weigh one blend unit.
const (
	DefaultFrecencyHalfLifeDays = 14
	DefaultFrecencyWeight       = 1.0
	DefaultFrecencyTierJump     = 3.0
)

// Window size defaults and floors (see WindowConfig). The defaults are
// ~15% larger than the original fixed 680x460 bar; the floors keep a
// hand-edited config from producing an unusably tiny window.
const (
	DefaultWindowWidth  = 780
	DefaultWindowHeight = 550
	MinWindowWidth      = 320
	MinWindowHeight     = 240
)

// Preview pane defaults (see PreviewConfig). The window grows to
// DefaultPreviewWindowWidth x DefaultPreviewWindowHeight when the pane
// is enabled; the size knobs bound what one preview may cost.
const (
	DefaultPreviewWindowWidth  = 1600
	DefaultPreviewWindowHeight = 800
	DefaultPreviewTextMaxKB    = 256
	DefaultPreviewImageMaxEdge = 800
	DefaultPreviewDirMax       = 200
	DefaultPreviewKagiMax      = 8
	DefaultPreviewOpenAIModel  = "gpt-5-mini"
	DefaultPreviewOpenAITokens = 1024
)

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
	// Stats configures the system-stats row (see internal/sysstats).
	Stats StatsConfig `json:"stats"`
	// Window configures the native window layer (read by main.go
	// before the Wails runtime starts).
	Window WindowConfig `json:"window"`
	// Firefox configures the Firefox history integration (see
	// internal/firefox).
	Firefox FirefoxConfig `json:"firefox"`
	// Rewrites are user-defined regex rewrite rules: a query matching
	// a rule's pattern yields one instant top result opening the
	// expanded URL (internal/plugin's rewrites source). Rules run in
	// config order; invalid patterns are logged at startup and
	// skipped.
	Rewrites []RewriteRule `json:"rewrites,omitempty"`
	// Preview configures the preview pane (see internal/preview).
	Preview PreviewConfig `json:"preview"`

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

// RewriteRule is one regex rewrite rule (config "rewrites"): pattern
// is a Go regexp (RE2) matched FULL-MATCH against the trimmed query
// unless the user anchors it (a leading ^ or trailing $ keeps it
// verbatim); replacement/title expand capture groups ($1, ${name},
// $$). The expanded replacement must be an absolute http(s) URL --
// open_url is the only action rewrites can produce.
type RewriteRule struct {
	// Name is the display name, shown as the result's subtitle.
	Name string `json:"name"`
	// Pattern is the rule's regular expression.
	Pattern string `json:"pattern"`
	// Replacement is the URL template.
	Replacement string `json:"replacement"`
	// Title optionally overrides the result title (same expansion);
	// empty means the expanded URL.
	Title string `json:"title,omitempty"`
	// Icon optionally overrides the "link" icon.
	Icon string `json:"icon,omitempty"`
	// Disabled turns the rule off without deleting it.
	Disabled bool `json:"disabled,omitempty"`
}

// SearchConfig configures the search engine. The zero value means the
// default behavior: fuzzy matching on, frecency ranking on.
type SearchConfig struct {
	// FuzzyDisabled true turns the fuzzy (subsequence) name-match tier
	// off, leaving exact/prefix/substring matching only. The zero
	// value -- the default -- keeps fuzzy matching on (matching the
	// tray.disabled convention). Exact, prefix, and substring matches
	// always rank above fuzzy ones either way.
	FuzzyDisabled bool `json:"fuzzyDisabled"`
	// Frecency configures the frecency/recency/noise ranking blend.
	Frecency FrecencyConfig `json:"frecency"`
}

// FrecencyConfig tunes the frecency ranking blend (see the README's
// "Ranking: frecency, recency and noise" and internal/index blend.go).
// Numeric convention: a ZERO value means "use the default" (Normalize
// repairs it, the repo-wide zero-value rule), and a NEGATIVE value
// explicitly disables that one signal -- the blend clamps negative
// weights to no contribution and a negative tierJumpCount turns tier
// jumping off. HalfLifeDays has no disable meaning, so any
// non-positive value is repaired to the default.
type FrecencyConfig struct {
	// Disabled true turns the whole blend off: no open counts are
	// recorded or loaded, no recency stats run, and result ordering
	// is exactly the pre-blend engine's.
	Disabled bool `json:"disabled"`
	// HalfLifeDays is how long a recorded open takes to count half as
	// much (default 14).
	HalfLifeDays float64 `json:"halfLifeDays"`
	// WeightFrecency scales the decayed open count (default 1.0).
	WeightFrecency float64 `json:"weightFrecency"`
	// WeightRecency scales the cold-start recency score in [0, 1] --
	// files never opened through the bar rank by how recently the
	// disk saw them touched (default 1.0).
	WeightRecency float64 `json:"weightRecency"`
	// WeightCwd scales the focused-app working-directory proximity
	// boost (default 1.0).
	WeightCwd float64 `json:"weightCwd"`
	// WeightNoise scales the location-noise penalty in [0, 1] --
	// cache/temp/vcs trees rank down (default 1.0).
	WeightNoise float64 `json:"weightNoise"`
	// TierJumpCount is the decayed-open-count threshold past which a
	// result competes one match tier up (default 3.0).
	TierJumpCount float64 `json:"tierJumpCount"`
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
	// Width is the bar window's width in pixels. Zero or negative
	// values (including configs predating the knob) get
	// DefaultWindowWidth; positive values below MinWindowWidth are
	// raised to that floor. See Normalize.
	Width int `json:"width"`
	// Height is the bar window's height in pixels; repaired against
	// DefaultWindowHeight / MinWindowHeight the same way.
	Height int `json:"height"`
}

// StatsConfig configures the system-stats row (see internal/sysstats
// and internal/app's stats wiring). The zero value -- the default --
// means enabled, matching the tray.disabled convention: the sampler
// only ever runs while the bar is visible and degrades missing
// sources to placeholders by itself, so only users who actively
// dislike the row need the switch.
type StatsConfig struct {
	// Disabled turns the system-stats sampler (and with it the
	// frontend's stats row data) off.
	Disabled bool `json:"disabled"`
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

// PreviewConfig configures the preview pane: a right-hand pane inside
// a widened window showing the selected result (file contents, image
// thumbnails, directory listings, metadata). The zero value -- the
// default -- keeps the pane off and the window at its classic size.
type PreviewConfig struct {
	// Enabled turns the preview pane on. It is read once at startup
	// (the window size cannot change while the app runs).
	Enabled bool `json:"enabled"`
	// WindowWidth is the window width in pixels while the pane is
	// enabled (default 1600). Ignored when Enabled is false.
	WindowWidth int `json:"windowWidth"`
	// WindowHeight is the window height in pixels while the pane is
	// enabled (default 800). Ignored when Enabled is false.
	WindowHeight int `json:"windowHeight"`
	// TextMaxKB caps how much of a text file one preview reads, in
	// KiB (default 256); longer files are truncated with a marker.
	TextMaxKB int `json:"textMaxKB"`
	// ImageMaxEdge caps an image thumbnail's longest edge in pixels
	// (default 800); larger sources are downscaled.
	ImageMaxEdge int `json:"imageMaxEdge"`
	// DirMaxEntries caps a directory listing preview (default 200).
	DirMaxEntries int `json:"dirMaxEntries"`
	// Kagi configures the explicit-trigger Kagi web-search preview.
	Kagi PreviewKagiConfig `json:"kagi"`
	// OpenAI configures the explicit-trigger OpenAI answer preview.
	OpenAI PreviewOpenAIConfig `json:"openai"`
}

// PreviewKagiConfig configures the Kagi web-search preview provider.
type PreviewKagiConfig struct {
	// APIKey is the Kagi Search API token (secret; passed through
	// verbatim, never logged). Empty means "use the KAGI_API_KEY
	// environment variable, if set"; with neither, the web-search
	// preview stays unavailable.
	APIKey string `json:"apiKey"`
	// MaxResults caps one web-search preview (default 8).
	MaxResults int `json:"maxResults"`
}

// PreviewOpenAIConfig configures the OpenAI answer preview provider.
type PreviewOpenAIConfig struct {
	// APIKey is the OpenAI API key (secret; passed through verbatim,
	// never logged). Empty means "use the OPENAI_API_KEY environment
	// variable, if set"; with neither, the answer preview stays
	// unavailable.
	APIKey string `json:"apiKey"`
	// Model names the model answering (default "gpt-5-mini").
	Model string `json:"model"`
	// MaxOutputTokens caps one answer (default 1024).
	MaxOutputTokens int `json:"maxOutputTokens"`
}

// DefaultPreview returns the default preview pane config: disabled,
// with every knob at its documented default so enabling is a one-key
// edit.
func DefaultPreview() PreviewConfig {
	return PreviewConfig{
		WindowWidth:   DefaultPreviewWindowWidth,
		WindowHeight:  DefaultPreviewWindowHeight,
		TextMaxKB:     DefaultPreviewTextMaxKB,
		ImageMaxEdge:  DefaultPreviewImageMaxEdge,
		DirMaxEntries: DefaultPreviewDirMax,
		Kagi:          PreviewKagiConfig{MaxResults: DefaultPreviewKagiMax},
		OpenAI: PreviewOpenAIConfig{
			Model:           DefaultPreviewOpenAIModel,
			MaxOutputTokens: DefaultPreviewOpenAITokens,
		},
	}
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
// DefaultFrecency returns the default frecency ranking config.
func DefaultFrecency() FrecencyConfig {
	return FrecencyConfig{
		HalfLifeDays:   DefaultFrecencyHalfLifeDays,
		WeightFrecency: DefaultFrecencyWeight,
		WeightRecency:  DefaultFrecencyWeight,
		WeightCwd:      DefaultFrecencyWeight,
		WeightNoise:    DefaultFrecencyWeight,
		TierJumpCount:  DefaultFrecencyTierJump,
	}
}

func Default() Config {
	return Config{
		Roots:                 defaultRoots(),
		RootsVersion:          currentRootsVersion,
		Excludes:              defaultExcludes(),
		Hotkey:                DefaultHotkey,
		RescanIntervalMinutes: 0,
		MaxResults:            DefaultMaxResults,
		Search:                SearchConfig{Frecency: DefaultFrecency()},
		Theme:                 DefaultTheme,
		Plugins:               PluginsConfig{Entries: map[string]PluginEntry{}},
		Bangs:                 BangsConfig{Sigils: DefaultBangSigils(), Aliases: map[string]string{}},
		Window:                WindowConfig{Width: DefaultWindowWidth, Height: DefaultWindowHeight},
		Firefox:               DefaultFirefox(),
		Preview:               DefaultPreview(),
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
// (mkdir -p included). A pre-v2 file is migrated to the current roots
// defaults (see migrateRoots) and rewritten once, with the changes
// reported in the returned Config's MigrationNotes. On any error --
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
// zero/negative knobs get their defaults (the firefox.frequentSites,
// firefox.openTabs and preview numbers included, plus an empty
// preview.openai.model; the search.frecency numbers repair only
// exact zeros -- negatives are the documented per-signal off switch
// there), the window size gets its
// defaults when unset and is clamped up to the minimum floors when set
// too small, an empty theme name gets the
// default theme, nil
// plugin entries and bang aliases become empty maps, and an empty
// sigil list gets the default sigils. The preview API keys are passed
// through verbatim, untouched. Excludes are left as the user
// wrote them (an explicitly empty list means "exclude nothing").
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
	fr := &c.Search.Frecency
	if fr.HalfLifeDays <= 0 {
		fr.HalfLifeDays = DefaultFrecencyHalfLifeDays
	}
	// Weights and the tier-jump threshold repair only the EXACT zero
	// value (absent from the JSON): negative values are the
	// documented per-signal off switch and pass through.
	if fr.WeightFrecency == 0 {
		fr.WeightFrecency = DefaultFrecencyWeight
	}
	if fr.WeightRecency == 0 {
		fr.WeightRecency = DefaultFrecencyWeight
	}
	if fr.WeightCwd == 0 {
		fr.WeightCwd = DefaultFrecencyWeight
	}
	if fr.WeightNoise == 0 {
		fr.WeightNoise = DefaultFrecencyWeight
	}
	if fr.TierJumpCount == 0 {
		fr.TierJumpCount = DefaultFrecencyTierJump
	}
	w := &c.Window
	switch {
	case w.Width <= 0:
		w.Width = DefaultWindowWidth
	case w.Width < MinWindowWidth:
		w.Width = MinWindowWidth
	}
	switch {
	case w.Height <= 0:
		w.Height = DefaultWindowHeight
	case w.Height < MinWindowHeight:
		w.Height = MinWindowHeight
	}
	pv := &c.Preview
	if pv.WindowWidth <= 0 {
		pv.WindowWidth = DefaultPreviewWindowWidth
	}
	if pv.WindowHeight <= 0 {
		pv.WindowHeight = DefaultPreviewWindowHeight
	}
	if pv.TextMaxKB <= 0 {
		pv.TextMaxKB = DefaultPreviewTextMaxKB
	}
	if pv.ImageMaxEdge <= 0 {
		pv.ImageMaxEdge = DefaultPreviewImageMaxEdge
	}
	if pv.DirMaxEntries <= 0 {
		pv.DirMaxEntries = DefaultPreviewDirMax
	}
	if pv.Kagi.MaxResults <= 0 {
		pv.Kagi.MaxResults = DefaultPreviewKagiMax
	}
	if pv.OpenAI.Model == "" {
		pv.OpenAI.Model = DefaultPreviewOpenAIModel
	}
	if pv.OpenAI.MaxOutputTokens <= 0 {
		pv.OpenAI.MaxOutputTokens = DefaultPreviewOpenAITokens
	}
}
