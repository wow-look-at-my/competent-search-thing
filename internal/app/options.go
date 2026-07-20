package app

import (
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// Options configures an App.
type Options struct {
	// RescanEvery > 0 enables periodic full rescans at that interval
	// (wire config.RescanIntervalMinutes here); 0 disables them.
	RescanEvery time.Duration
	// WatchMaxWatches bounds the live-watch hot set (wire config's
	// watcher.maxWatches here): 0 = automatic budget, negative =
	// explicitly unlimited, positive taken as-is. See
	// watch.Options.MaxWatches.
	WatchMaxWatches int
	// SweepInterval is the reconcile-sweep cadence (wire config's
	// watcher.sweepMinutes here, as a Duration); 0 selects the watch
	// layer's default (20 minutes).
	SweepInterval time.Duration
	// SweepDisabled turns the sweep tier off entirely (wire the
	// INVERSE of config's watcher.sweepEnabled here, via
	// !config.Enabled). startWatch then logs a loud warning: without
	// sweeps, directories without a live watch converge only at full
	// rescans.
	SweepDisabled bool
	// WatchExcludes are exclude patterns applied to live watching
	// only (wire config's watcher.watchExcludes here): matching
	// directories and their subtrees stay indexed and swept but never
	// hold a watch. See watch.Options.WatchEx.
	WatchExcludes []string
	// WatchBackend selects the notification backend (wire config's
	// watcher.backend here): "auto"/"" = automatic detection,
	// "fanotify" = strict fanotify-or-nothing, "inotify" = skip the
	// fanotify probe. See watch.Options.Backend; the effective backend
	// is announced to the frontend via eventWatchBackend either way.
	WatchBackend string
	// Hotkey is the config hotkey string ("alt+space"); empty disables
	// the global hotkey.
	Hotkey string
	// IPC is the single-instance IPC server the CLI layer acquired
	// (nil when IPC is unavailable; everything degrades to no-ops).
	// Startup wires the toggle/show/hide handlers into it and the App
	// owns it from then on: Shutdown closes it.
	IPC *ipc.Server
	// ShowOnStartup asks for the bar to be shown as soon as the
	// frontend is ready (set when a CLI toggle/show started the app).
	ShowOnStartup bool
	// OpenConfigOnStartup asks for the bar to open straight into the
	// config editor once the frontend is ready (set when the CLI
	// config subcommand started the app); it implies ShowOnStartup.
	OpenConfigOnStartup bool
	// TrayDisabled turns the tray icon off (wire the INVERSE of
	// config's tray.enabled here, via !config.Enabled); the default
	// zero value keeps it on.
	TrayDisabled bool
	// HistoryPersistDisabled keeps the query history in memory only
	// (wire the INVERSE of config's history.persistEnabled here, via
	// !config.Enabled); the default zero value persists it to
	// <configDir>/history.json. See history.go.
	HistoryPersistDisabled bool
	// ConfigNotes are the human-readable migration notes config.Load
	// produced (wire cfg.MigrationNotes here); Startup logs each one
	// loudly, exactly once, so a changed index scope is never silent.
	ConfigNotes []string
	// Frecency configures the frecency ranking blend (wire config's
	// search.frecency here; see frecency.go). Weights arrive
	// Normalize-repaired; Enabled = false leaves the whole layer
	// unwired (absent means on, the tray.enabled convention).
	Frecency config.FrecencyConfig
	// Priors configures the pick-memory ranking priors (wire config's
	// search.priors here; see priors.go in this package). ON by
	// default: the zero value (nil Enabled) wires the layer; Enabled
	// = false (the debug escape hatch) keeps it entirely unwired --
	// no file reads, no goroutines, no blend term.
	Priors config.PriorsConfig
	// Telemetry bounds the always-on local ranking log (wire config's
	// search.telemetry here; see telemetry.go in this package). There
	// is deliberately no off switch; the zero value records at the
	// default size bound.
	Telemetry config.TelemetryConfig
	// Arbiter configures the learned composition arbitration layer
	// (wire config's search.arbiter here; see arbiter.go in this
	// package). ON by default: the zero value (nil Enabled) wires the
	// layer (inert until its activation gate passes); Enabled = false
	// (the debug escape hatch / kill switch) keeps it entirely
	// unwired -- no file reads, no goroutines, no model term, and
	// plugin emissions pass through untouched.
	Arbiter config.ArbiterConfig
	// Preview is the preview pane configuration (wire config's
	// preview section here); the zero value keeps the pane off and
	// every preview method degrades to a no-op. See preview.go.
	Preview config.PreviewConfig
	// WindowWidth and WindowHeight are the effective bar window size
	// in pixels; the positioning math uses them, so they must match
	// what the native window was built with (main.go feeds both from
	// the one app.PreviewWindowSize() read -- the configured
	// window.width/height, or the preview-widened size when the pane
	// is on). Zero values fall back to the config defaults, keeping
	// bare-Options tests working.
	WindowWidth  int
	WindowHeight int
	// ResultsWidth is the pixel width the left results column keeps
	// while the preview pane is on -- the flag-off bar width (wire
	// config's window.width here), so the column matches what the bar
	// would be without the pane. Zero or negative values fall back to
	// config.DefaultWindowWidth in GetPreviewConfig, keeping
	// bare-Options tests working. Unused while the pane is off.
	ResultsWidth int
}
