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

// Watcher backend selection values (WatcherConfig.Backend).
const (
	// WatcherBackendAuto (the default) uses a whole-filesystem
	// backend where one exists -- fanotify marks on Linux (kernel,
	// privileges, and filesystems allowing), the FSEvents stream on
	// macOS (no privileges needed) -- and falls back to the
	// per-directory hot set otherwise.
	WatcherBackendAuto = "auto"
	// WatcherBackendFanotify is STRICT: fanotify or nothing; Linux
	// only. When the fanotify backend cannot start (or on any other
	// OS), live watching is disabled outright -- never a silent
	// per-directory fallback -- and the index converges through
	// sweeps only, announced loudly in-app and in the log.
	WatcherBackendFanotify = "fanotify"
	// WatcherBackendFSEvents is STRICT: FSEvents or nothing; macOS
	// only. Same stance as WatcherBackendFanotify -- when the stream
	// cannot start (or on any other OS), live watching is disabled
	// outright and sweeps keep the index converging, announced
	// loudly.
	WatcherBackendFSEvents = "fsevents"
	// WatcherBackendInotify skips the whole-filesystem probe and uses
	// the per-directory hot set directly on every OS (mainly for
	// debugging). Named after Linux's inotify; the runtime backend is
	// whatever fsnotify uses on the OS (kqueue on macOS), and the
	// watch layer labels it honestly.
	WatcherBackendInotify = "inotify"
)

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

// DefaultTelemetryMaxSizeKB is the ranking log's rotation threshold
// (see TelemetryConfig.MaxSizeKB): an append crossing it rotates
// telemetry.jsonl to telemetry.jsonl.1, so the disk cap is two
// generations of this size. Deliberately generous (64 MiB live + one
// rotated generation): "reasonable levels" means don't fill the disk,
// not don't record.
const DefaultTelemetryMaxSizeKB = 65536

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
// is enabled -- a deliberately modest step up from the 780x550 base
// bar (not the pre-v8 1600x800 doubling) now that the pane is on by
// default; the size knobs bound what one preview may cost.
const (
	DefaultPreviewWindowWidth  = 1100
	DefaultPreviewWindowHeight = 700
	DefaultPreviewTextMaxKB    = 256
	DefaultPreviewImageMaxEdge = 800
	DefaultPreviewDirMax       = 200
	DefaultPreviewKagiMax      = 8
	DefaultPreviewOpenAIModel  = "gpt-5-mini"
	DefaultPreviewOpenAITokens = 1024
	// DefaultPreviewAnthropicModel is the Anthropic answer model:
	// the cheapest current-generation model, the right default for
	// short preview answers (the gpt-5-mini analogue).
	DefaultPreviewAnthropicModel  = "claude-haiku-4-5"
	DefaultPreviewAnthropicTokens = 1024
	DefaultPreviewCustomTokens    = 1024
)

// AI answer provider selector values (preview.aiProvider). "openai"
// and "anthropic" are the known providers; "custom" points the
// OpenAI-compatible client at a user-typed base URL (Ollama, LM
// Studio, vLLM, any /v1/responses-speaking server). Normalize repairs
// empty/unknown values to the default, the schema enum stays in
// lockstep (the watcher.backend convention).
const (
	AIProviderOpenAI    = "openai"
	AIProviderAnthropic = "anthropic"
	AIProviderCustom    = "custom"

	DefaultPreviewAIProvider = AIProviderOpenAI
)

// SchemaRef is the value stamped into Config.Schema: a relative
// reference to the schema sidecar the app writes next to config.json
// at every startup (see internal/app's schema sidecar), so editors
// pick up validation and completion without any configuration.
const SchemaRef = "./config.schema.json"

// Config is the on-disk configuration.
type Config struct {
	// Schema is the "$schema" editor hint pointing at the JSON Schema
	// for this file -- the FIRST struct field, so Save/Encode emit it
	// as the document's first key. It is a RESERVED key, never a
	// setting: Normalize stamps SchemaRef when it is empty (existing
	// configs pick it up on their next save), a hand-set value passes
	// through verbatim, the loader never validates it, UnknownKeys
	// knows it, and the GUI editor hides it (x-editor-hidden in the
	// schema). The app ignores the value entirely.
	Schema string `json:"$schema,omitempty"`
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
	// Enabled false turns the whole plugin system off. Absent (nil)
	// = enabled, the default (Normalize repairs nil to true).
	Enabled *bool `json:"enabled"`
	// Entries holds per-plugin overrides keyed by plugin id (builtin
	// provider ids work here too).
	Entries map[string]PluginEntry `json:"entries"`
}

// PluginEntry is one plugin's configuration.
type PluginEntry struct {
	// Enabled false turns this one plugin off; nil = on (repaired).
	Enabled *bool `json:"enabled"`
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
	// Enabled false turns the rule off without deleting it; nil =
	// on, deliberately NOT repaired (user rules never grow keys).
	Enabled *bool `json:"enabled,omitempty"`
}

// SearchConfig configures the search engine. The zero value means the
// default behavior: fuzzy matching on, frecency ranking on, the
// always-on ranking log at its default bound, both learned layers on.
type SearchConfig struct {
	// FuzzyEnabled false turns the fuzzy (subsequence) name-match
	// tier off, leaving exact/prefix/substring matching only; absent
	// (nil) = enabled, the default (Normalize repairs nil to true).
	// Exact/prefix/substring always rank above fuzzy either way.
	FuzzyEnabled *bool `json:"fuzzyEnabled"`
	// Frecency configures the frecency/recency/noise ranking blend.
	Frecency FrecencyConfig `json:"frecency"`
	// Priors configures the pick-memory ranking priors (see
	// internal/priors).
	Priors PriorsConfig `json:"priors"`
	// Telemetry bounds the always-on local ranking log.
	Telemetry TelemetryConfig `json:"telemetry"`
	// Arbiter configures the learned composition arbitration layer.
	Arbiter ArbiterConfig `json:"arbiter"`
}

// PriorsConfig configures the pick-memory ranking priors: small
// local lookup tables -- exact-query pick memory plus per-extension
// and per-directory-prefix pick rates -- learned from the local
// ranking log (search.telemetry) and bootstrapped from frecency.json,
// folded into result ordering as one additive blend term (see
// internal/priors and the README's "Pick-memory priors"). ON by
// default (the tray.enabled absent-means-on convention): everything
// is local-only, so there is no default worth losing the learning
// over. The half-life, smoothing, and table caps are internal
// defaults; the switch is a debug escape hatch, not a privacy option.
type PriorsConfig struct {
	// Enabled false turns the priors layer off -- a debug escape
	// hatch for a deterministic ranking baseline, or a kill switch if
	// the learned layer misbehaves. Absent (nil) = enabled, the
	// default (Normalize repairs nil to true).
	Enabled *bool `json:"enabled"`
}

// TelemetryConfig configures the local ranking log (see
// internal/telemetry and the README's "Ranking log"): one size-capped
// JSONL record per activated result, carrying the query, the
// delivered result list with its ranking signals, and which row was
// picked. It is a LOG, not telemetry in the phone-home sense: it
// never leaves this machine -- the only place it goes is a debugging
// chat if the user pastes it. ALWAYS ON, by design: there is
// deliberately no off switch -- the log is private by staying on the
// machine, and deleting the file is always safe (recording just
// starts fresh). The only knob is the disk bound.
type TelemetryConfig struct {
	// MaxSizeKB is the log rotation threshold in KiB (default 65536 =
	// 64 MiB): an append that would cross it first rotates
	// telemetry.jsonl to telemetry.jsonl.1 (replacing the previous
	// .1), so at most two generations exist. Bounded disk is
	// engineering, not redaction. Zero or negative values are
	// repaired to the default by Normalize.
	MaxSizeKB int `json:"maxSizeKB"`
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
	// Enabled false turns the whole blend off: no open counts are
	// recorded or loaded, no recency stats run, and result ordering
	// is exactly the pre-blend engine's; nil = on (repaired).
	Enabled *bool `json:"enabled"`
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
	// SweepEnabled false turns the sweep tier off entirely; absent
	// (nil) = enabled, the default (Normalize repairs nil to true).
	// With sweeps off, directories without a live watch converge only
	// at full rescans; the app logs a loud warning saying so.
	SweepEnabled *bool `json:"sweepEnabled"`
	// WatchExcludes are exclude patterns (the excludes syntax, see
	// internal/index.Excluder) applied ONLY to live watching: matching
	// directories never get a per-directory watch, but they are still
	// indexed and still swept, so changes inside them appear within
	// one sweep interval instead of ~1s. Use it to keep high-churn
	// trees you still want searchable from consuming watch budget.
	WatchExcludes []string `json:"watchExcludes,omitempty"`
	// Backend selects the notification backend (the WatcherBackend*
	// constants): "auto" (the default; a whole-filesystem backend
	// when the binary can use one -- fanotify marks on Linux, the
	// FSEvents stream on macOS -- else the per-directory hot set),
	// "fanotify" or "fsevents" (STRICT: when the named backend cannot
	// start, including on the wrong OS, live watching is DISABLED --
	// never a silent per-directory fallback -- and sweeps keep the
	// index converging), or "inotify" (skip the whole-filesystem
	// probe on every OS; for debugging). Normalize lowercases the
	// value and repairs empty or unknown values to "auto" -- no
	// migration note is recorded for that repair because the
	// effective backend is never silent: the watch layer logs it at
	// startup and the frontend shows a notice chip whenever coverage
	// is not full.
	Backend string `json:"backend,omitempty"`
	// SetupEnabled controls the automatic optimal-backend setup that
	// runs before the GUI starts (see internal/watchsetup). On Linux the
	// whole-filesystem fanotify backend needs capabilities the raw
	// binary lacks; when they are missing but grantable, the app prompts
	// for privilege escalation (pkexec), grants them with setcap, and
	// re-execs into the capable binary so it comes up with full,
	// low-memory coverage instead of the per-directory fallback. Absent
	// (nil) = enabled, the default (Normalize repairs nil to true, the
	// tray.enabled convention); false is the persistent per-user opt-out
	// (no probe, no prompt, no re-exec). The COMPETENT_SEARCH_NO_WATCH_SETUP
	// env var is the per-process opt-out. Irrelevant off Linux (the
	// setup is a no-op there).
	SetupEnabled *bool `json:"setupEnabled"`
}

// BangsConfig configures the bang system.
type BangsConfig struct {
	// Sigils are the characters that may start a bang query; empty
	// means the defaults (see DefaultBangSigils).
	Sigils []string `json:"sigils"`
	// Aliases map extra names onto registered bangs.
	Aliases map[string]string `json:"aliases"`
}

// TrayConfig configures the tray icon (StatusNotifierItem). An absent
// switch means enabled -- the default: the icon degrades away by
// itself on sessions without a StatusNotifierItem host, so only users
// who actively dislike it need the switch. (This absent-means-on rule
// is "the tray.enabled convention" the other default-on switches
// reference.)
type TrayConfig struct {
	// Enabled false turns the tray icon off; nil = on (repaired).
	Enabled *bool `json:"enabled"`
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
// and internal/app's stats wiring). An absent switch means enabled --
// the default, matching the tray.enabled convention: the sampler
// only ever runs while the bar is visible and degrades missing
// sources to placeholders by itself, so only users who actively
// dislike the row need the switch.
type StatsConfig struct {
	// Enabled false turns the system-stats sampler (and with it the
	// frontend's stats row data) off; nil = on (repaired).
	Enabled *bool `json:"enabled"`
}

// HistoryConfig configures the query history (see internal/history).
type HistoryConfig struct {
	// PersistEnabled false keeps the history in memory only: nothing
	// is read from or written to <configDir>/history.json, while
	// in-session Up/Down recall keeps working; nil = on (repaired).
	PersistEnabled *bool `json:"persistEnabled"`
}

// FirefoxConfig configures the Firefox integrations.
type FirefoxConfig struct {
	// FrequentSites configures the frequently-visited-sites result
	// section (the builtin firefox-frequent provider; turn it off via
	// plugins.entries["firefox-frequent"].enabled = false).
	FrequentSites FrequentSitesConfig `json:"frequentSites"`
	// OpenTabs configures the open-tabs result section (the builtin
	// firefox-tabs provider; turn it off via
	// plugins.entries["firefox-tabs"].enabled = false).
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
// thumbnails, directory listings, metadata). ON by default since
// rootsVersion 8 (preview.enabled=false is the opt-out); the
// file/dir/image previews need no configuration, while the web/AI
// strips stay disabled-with-a-hint until their provider is set up.
type PreviewConfig struct {
	// Enabled turns the preview pane on -- the DEFAULT since
	// rootsVersion 8: the affirmative *bool convention (nil = absent
	// = ON; Normalize repairs nil to explicit true, the tray.enabled
	// pattern). An explicit false written after the flip is a
	// respected opt-out; a machine-written pre-v8 false (the
	// plain-bool era serialized false on every save) is reset to on
	// by the v8 migration with a loud note (see migrate.go).
	Enabled *bool `json:"enabled"`
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
	// AIProvider picks which provider answers the explicit AI
	// preview (Ctrl+I): "openai" (the default), "anthropic", or
	// "custom" (an OpenAI-compatible endpoint named by
	// preview.custom.baseUrl). Only the selected provider's section
	// is consulted; the others keep their values for switching back.
	// Normalize trims, lowercases, and repairs empty/unknown values
	// to "openai".
	AIProvider string `json:"aiProvider"`
	// Kagi configures the explicit-trigger Kagi web-search preview.
	Kagi PreviewKagiConfig `json:"kagi"`
	// OpenAI configures the OpenAI answer provider (aiProvider
	// "openai").
	OpenAI PreviewOpenAIConfig `json:"openai"`
	// Anthropic configures the Anthropic answer provider (aiProvider
	// "anthropic").
	Anthropic PreviewAnthropicConfig `json:"anthropic"`
	// Custom configures the user-typed OpenAI-compatible answer
	// provider (aiProvider "custom").
	Custom PreviewCustomConfig `json:"custom"`
}

// PreviewKagiConfig configures the Kagi web-search preview provider.
type PreviewKagiConfig struct {
	// APIKey is the Kagi Search API token (secret; passed through
	// verbatim, never logged). Empty means "use the KAGI_API_KEY
	// environment variable, if set"; with neither, the web-search
	// preview stays unavailable.
	APIKey string `json:"apiKey"`
	// BaseURL is a custom API base URL (e.g. a self-hosted
	// Kagi-compatible server) replacing the WHOLE default base
	// verbatim; empty = the official https://kagi.com/api/v1. No env
	// fallback; requests go to <baseUrl>/search.
	BaseURL string `json:"baseUrl"`
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
	// BaseURL is a custom API base URL, e.g. an OpenAI-compatible
	// server; empty means "use the OPENAI_BASE_URL environment
	// variable, if set", else the official endpoint
	// (https://api.openai.com). Passed through verbatim; requests go
	// to <baseUrl>/v1/responses (the Responses API).
	BaseURL string `json:"baseUrl"`
	// Model names the model answering (default "gpt-5-mini").
	Model string `json:"model"`
	// MaxOutputTokens caps one answer (default 1024).
	MaxOutputTokens int `json:"maxOutputTokens"`
}

// PreviewAnthropicConfig configures the Anthropic answer preview
// provider (active when preview.aiProvider is "anthropic").
type PreviewAnthropicConfig struct {
	// APIKey is the Anthropic API key (secret; passed through
	// verbatim, never logged). Empty means "use the
	// ANTHROPIC_API_KEY environment variable, if set"; with neither,
	// the answer preview stays unavailable.
	APIKey string `json:"apiKey"`
	// BaseURL is a custom API base URL; empty means "use the
	// ANTHROPIC_BASE_URL environment variable, if set", else the
	// official endpoint (https://api.anthropic.com). Passed through
	// verbatim; requests go to <baseUrl>/v1/messages (the Messages
	// API).
	BaseURL string `json:"baseUrl"`
	// Model names the model answering (default "claude-haiku-4-5").
	Model string `json:"model"`
	// MaxOutputTokens caps one answer (default 1024).
	MaxOutputTokens int `json:"maxOutputTokens"`
}

// PreviewCustomConfig configures a user-typed OpenAI-compatible
// answer endpoint (active when preview.aiProvider is "custom") --
// Ollama, LM Studio, vLLM, or any server speaking the Responses API.
type PreviewCustomConfig struct {
	// APIKey is the endpoint's API key, when it needs one (secret;
	// passed through verbatim, never logged). Local servers usually
	// need none -- empty sends no Authorization header. No
	// environment fallback.
	APIKey string `json:"apiKey"`
	// BaseURL is the endpoint's base URL -- REQUIRED for the custom
	// provider to be usable; requests go to <baseUrl>/v1/responses
	// (the OpenAI Responses API wire shape). No environment
	// fallback.
	BaseURL string `json:"baseUrl"`
	// Model names the model answering -- required (there is no
	// sensible default for an unknown server; the app never invents
	// one).
	Model string `json:"model"`
	// MaxOutputTokens caps one answer (default 1024).
	MaxOutputTokens int `json:"maxOutputTokens"`
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
	migrated := c.migrateRoots(data)
	c.Normalize()
	if migrated {
		if werr := Save(c); werr != nil {
			return c, fmt.Errorf("config: persisting the roots migration: %w", werr)
		}
	}
	return c, nil
}

// Save writes the config file, creating the directory as needed. The
// write is atomic (internal/history's temp-file-then-rename pattern,
// at this file's historical 0644 perms): a crash mid-write never
// leaves a truncated config.json behind, and watchers of the file see
// exactly one rename event per save instead of a partial-content
// window. The bytes written are Encode's.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: creating %s: %w", dir, err)
	}
	data, err := Encode(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, p)
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("config: writing %s: %w", p, err)
	}
	return nil
}

// Encode returns exactly the bytes Save writes for c: two-space
// indented JSON plus a trailing newline. Callers that need to know
// the on-disk representation of a config they saved (e.g. the app's
// self-write suppression around the config-dir watcher) use it
// instead of re-deriving the format.
func Encode(c Config) ([]byte, error) {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("config: encoding: %w", err)
	}
	return append(data, '\n'), nil
}
