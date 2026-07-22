package config

// The Default* constructors: the values a fresh install writes and
// Normalize repairs toward. Split from config.go, which sits at the
// repo's hard line cap (the arbiter.go own-file precedent); the
// per-field default constants stay beside their types in config.go.

// DefaultPreview returns the default preview pane config: disabled,
// with every knob at its documented default so enabling is a one-key
// edit.
func DefaultPreview() PreviewConfig {
	return PreviewConfig{
		Enabled:       Bool(true),
		WindowWidth:   DefaultPreviewWindowWidth,
		WindowHeight:  DefaultPreviewWindowHeight,
		TextMaxKB:     DefaultPreviewTextMaxKB,
		ImageMaxEdge:  DefaultPreviewImageMaxEdge,
		DirMaxEntries: DefaultPreviewDirMax,
		AIProvider:    DefaultPreviewAIProvider,
		Kagi:          PreviewKagiConfig{MaxResults: DefaultPreviewKagiMax},
		OpenAI: PreviewOpenAIConfig{
			Model:           DefaultPreviewOpenAIModel,
			MaxOutputTokens: DefaultPreviewOpenAITokens,
		},
		Anthropic: PreviewAnthropicConfig{
			Model:           DefaultPreviewAnthropicModel,
			MaxOutputTokens: DefaultPreviewAnthropicTokens,
		},
		Custom: PreviewCustomConfig{
			MaxOutputTokens: DefaultPreviewCustomTokens,
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

// DefaultTelemetry returns the default ranking log config: the size
// cap at its documented default (the log itself is always on).
func DefaultTelemetry() TelemetryConfig {
	return TelemetryConfig{
		MaxSizeKB: DefaultTelemetryMaxSizeKB,
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
		Enabled:        Bool(true),
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
		Schema:                SchemaRef,
		Roots:                 defaultRoots(),
		RootsVersion:          currentRootsVersion,
		Excludes:              defaultExcludes(),
		Hotkey:                DefaultHotkey,
		RescanIntervalMinutes: 0,
		MaxResults:            DefaultMaxResults,
		Search:                DefaultSearch(),
		Watcher:               WatcherConfig{SweepEnabled: Bool(true), SetupEnabled: Bool(true)},
		Theme:                 DefaultTheme,
		Plugins:               PluginsConfig{Enabled: Bool(true), Entries: map[string]PluginEntry{}},
		Bangs:                 BangsConfig{Sigils: DefaultBangSigils(), Aliases: map[string]string{}},
		Tray:                  TrayConfig{Enabled: Bool(true)},
		History:               HistoryConfig{PersistEnabled: Bool(true)},
		Stats:                 StatsConfig{Enabled: Bool(true)},
		Window:                WindowConfig{Width: DefaultWindowWidth, Height: DefaultWindowHeight},
		Firefox:               DefaultFirefox(),
		Preview:               DefaultPreview(),
	}
}

// DefaultSearch returns the default search section: every switch
// explicitly on, the frecency and telemetry knobs at their
// documented defaults.
func DefaultSearch() SearchConfig {
	return SearchConfig{
		FuzzyEnabled: Bool(true),
		Frecency:     DefaultFrecency(),
		Priors:       PriorsConfig{Enabled: Bool(true)},
		Telemetry:    DefaultTelemetry(),
		Arbiter:      ArbiterConfig{Enabled: Bool(true)},
	}
}
