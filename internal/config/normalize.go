package config

import (
	"path/filepath"
	"strings"
)

// Config repair: the Normalize method every Load runs over a parsed
// config. Split from config.go, which keeps the types, defaults, and
// Load/Save.

// Normalize repairs missing or nonsensical fields in place: empty roots
// fall back to the default root, relative roots are absolutized,
// zero/negative knobs get their defaults (the firefox.frequentSites,
// firefox.openTabs and preview numbers included, plus an empty
// preview.openai.model; a negative watcher.sweepMinutes becomes 0 =
// the built-in cadence; the search.frecency numbers repair only
// exact zeros -- negatives are the documented per-signal off switch
// there; a non-positive search.telemetry.maxSizeKB gets its
// default), the window size gets its defaults when unset and is clamped
// up to the minimum floors when set too small, an empty theme name
// gets the default theme, watcher.backend is lowercased and repaired
// to "auto" when empty or unknown, nil plugin entries and bang
// aliases become empty maps, and an empty sigil list gets the default
// sigils. The preview API keys are passed through verbatim, untouched.
// Excludes are left as the user wrote them (an explicitly empty list
// means "exclude nothing"), and so are watcher.watchExcludes and
// watcher.maxWatches (negative = explicitly unlimited).
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
	switch strings.ToLower(strings.TrimSpace(c.Watcher.Backend)) {
	case WatcherBackendFanotify:
		c.Watcher.Backend = WatcherBackendFanotify
	case WatcherBackendInotify:
		c.Watcher.Backend = WatcherBackendInotify
	default: // "", "auto", or anything unknown
		c.Watcher.Backend = WatcherBackendAuto
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
	if c.Search.Telemetry.MaxSizeKB <= 0 {
		c.Search.Telemetry.MaxSizeKB = DefaultTelemetryMaxSizeKB
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
