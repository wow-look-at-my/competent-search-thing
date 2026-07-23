package config

// WatcherConfig, split from config.go to keep that file under the repo's
// line cap (the defaults.go / normalize.go / migrate.go precedent). The
// WatcherBackend* constants stay beside their siblings in config.go.

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
