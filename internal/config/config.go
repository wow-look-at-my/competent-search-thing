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

// Config is the on-disk configuration.
type Config struct {
	// Roots are the directories to index.
	Roots []string `json:"roots"`
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
	// Theme names the UI theme: a builtin ("dark", "light") or a user
	// theme file at <configDir>/themes/<name>.json (see internal/theme).
	// Unknown or invalid themes fall back to dark at resolve time.
	Theme string `json:"theme"`
	// Plugins configures the plugin system (see internal/plugin).
	Plugins PluginsConfig `json:"plugins"`
	// Bangs configures bang parsing (sigils and aliases).
	Bangs BangsConfig `json:"bangs"`
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

// BangsConfig configures the bang system.
type BangsConfig struct {
	// Sigils are the characters that may start a bang query; empty
	// means the defaults (see DefaultBangSigils).
	Sigils []string `json:"sigils"`
	// Aliases map extra names onto registered bangs.
	Aliases map[string]string `json:"aliases"`
}

// DefaultBangSigils returns the default bang sigil set. It returns a
// fresh slice on every call so callers may modify it safely.
func DefaultBangSigils() []string { return []string{"!", "/", "@"} }

// Default returns the default configuration: index the user's home
// directory (falling back to the current directory if the home cannot
// be determined), skip the usual noise, no periodic rescan.
func Default() Config {
	root, err := os.UserHomeDir()
	if err != nil || root == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return Config{
		Roots:                 []string{root},
		Excludes:              []string{".git", "node_modules", ".cache"},
		Hotkey:                DefaultHotkey,
		RescanIntervalMinutes: 0,
		MaxResults:            DefaultMaxResults,
		Theme:                 DefaultTheme,
		Plugins:               PluginsConfig{Entries: map[string]PluginEntry{}},
		Bangs:                 BangsConfig{Sigils: DefaultBangSigils(), Aliases: map[string]string{}},
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
// (mkdir -p included). On any error -- unresolvable path, unreadable or
// corrupt file, failed default write -- Load still returns a usable
// default config alongside the error; callers log the error and keep
// going, they never crash.
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
	c.Normalize()
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
// zero/negative knobs get their defaults, an empty theme name gets the
// default theme, nil plugin entries and bang aliases become empty maps,
// and an empty sigil list gets the default sigils. Excludes are left as
// the user wrote them (an explicitly empty list means "exclude
// nothing").
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
}
