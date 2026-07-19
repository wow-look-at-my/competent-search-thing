package app

// The config live-apply engine: one pass diffs the incoming
// configuration against the currently applied one per section and
// runs each changed section's applier. Appliers are IDEMPOTENT (a
// re-apply of the same value is harmless) and cheap; sections whose
// live path has not landed yet carry a nil applier and are reported
// in ApplyResult.Pending -- the table is meant to become total, one
// applier at a time, until every knob applies without a restart.
// Phase-B slots: add a sectionApplier.apply (or a shared group) and
// the section moves from Pending to Applied; nothing else changes.

import (
	"crypto/sha256"
	"log"
	"os"
	"reflect"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// ApplyResult reports what one applyConfig pass did.
type ApplyResult struct {
	// Applied lists the changed sections applied live, in table order.
	Applied []string `json:"applied"`
	// Pending lists the changed sections that have no live applier
	// yet (they take effect on the next launch until their applier
	// lands).
	Pending []string `json:"pending"`
	// Errors lists per-section apply failures ("<section>: <error>").
	Errors []string `json:"errors,omitempty"`
}

// sectionApplier is one row of the live-apply table: a section name
// (a top-level config key, or a finer "key.sub" grain where one key's
// subsections apply on different paths), its change predicate, and
// how to apply it.
type sectionApplier struct {
	name string
	// changed reports whether the section differs between the applied
	// config and the incoming one. old is never nil here (a nil
	// baseline applies every section).
	changed func(old, next *config.Config) bool
	// apply applies the section live; nil = no live path yet, the
	// section lands in Pending. Appliers must be idempotent.
	apply func(a *App, next *config.Config) error
	// group names a shared applier (applyGroups) that runs at most
	// once per pass no matter how many of its sections changed; ""
	// means none. A row may carry both its own apply and a group.
	group string
}

// Shared applier group names.
const groupRegistry = "registry"

// applyGroups maps a group name to its shared applier. The registry
// group covers every section buildRegistry re-reads from disk on a
// reload (plugins, bangs, rewrites, firefox, and search.fuzzyDisabled
// for the plugin engine's ranking): one reload per pass applies them
// all.
var applyGroups = map[string]func(a *App, next *config.Config) error{
	groupRegistry: func(a *App, _ *config.Config) error {
		a.reloadRegistry()
		return nil
	},
}

// sectionAppliers is the live-apply table, in report order. Phase-B
// work extends it in place: give a row an apply func (or group) and
// delete nothing. rootsVersion has no row on purpose -- it is
// app-managed (SaveConfig forces it, Load migrations own it) and can
// never legitimately differ between two loaded configs.
var sectionAppliers = []sectionApplier{
	{
		name:    "roots",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Roots, n.Roots) },
		// Pending: needs Manager root swapping + a rescan (Phase B).
	},
	{
		name:    "excludes",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Excludes, n.Excludes) },
		// Pending: needs Manager exclude swapping + a rescan (Phase B).
	},
	{
		name:    "hotkey",
		changed: func(o, n *config.Config) bool { return o.Hotkey != n.Hotkey },
		// Pending: needs hotkey re-registration across the native/
		// portal/gsettings backends (Phase B).
	},
	{
		name:    "rescanIntervalMinutes",
		changed: func(o, n *config.Config) bool { return o.RescanIntervalMinutes != n.RescanIntervalMinutes },
		// Pending: needs the rescanner ticker rebuilt (Phase B).
	},
	{
		name:    "maxResults",
		changed: func(o, n *config.Config) bool { return o.MaxResults != n.MaxResults },
		apply: func(a *App, n *config.Config) error {
			if a.manager != nil {
				a.manager.SetMaxResults(n.MaxResults)
			}
			return nil
		},
	},
	{
		name:    "search.fuzzyDisabled",
		changed: func(o, n *config.Config) bool { return o.Search.FuzzyDisabled != n.Search.FuzzyDisabled },
		apply: func(a *App, n *config.Config) error {
			if a.manager != nil {
				a.manager.SetFuzzyDisabled(n.Search.FuzzyDisabled)
			}
			return nil
		},
		// The plugin engine ranks with the same switch; the registry
		// reload re-reads it from disk.
		group: groupRegistry,
	},
	{
		name:    "search.frecency",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Search.Frecency, n.Search.Frecency) },
		// Pending: needs the frecency store/blend rebuilt (Phase B).
	},
	{
		name:    "watcher",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Watcher, n.Watcher) },
		// Pending: needs the watch layer rebuilt (Phase B).
	},
	{
		name:    "theme",
		changed: func(o, n *config.Config) bool { return o.Theme != n.Theme },
		// Live through existing machinery: GetTheme fresh-loads the
		// config per call, and the config-dir watcher's theme:changed
		// makes the frontend refetch on every config.json write --
		// nothing to do here beyond reporting it applied.
		apply: func(*App, *config.Config) error { return nil },
	},
	{
		name:    "plugins",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Plugins, n.Plugins) },
		group:   groupRegistry,
	},
	{
		name:    "bangs",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Bangs, n.Bangs) },
		group:   groupRegistry,
	},
	{
		name:    "tray",
		changed: func(o, n *config.Config) bool { return o.Tray != n.Tray },
		// Pending: needs tray start/stop on flip (Phase B).
	},
	{
		name:    "history",
		changed: func(o, n *config.Config) bool { return o.History != n.History },
		// Pending: needs the history store rebuilt (Phase B).
	},
	{
		name:    "stats",
		changed: func(o, n *config.Config) bool { return o.Stats != n.Stats },
		// Pending: needs the stats sampler start/stop (Phase B).
	},
	{
		name:    "window",
		changed: func(o, n *config.Config) bool { return o.Window != n.Window },
		// Pending: window size/translucency are creation-time Wails
		// properties; the live path is under investigation (Phase B).
	},
	{
		name:    "firefox",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Firefox, n.Firefox) },
		group:   groupRegistry,
	},
	{
		name:    "rewrites",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Rewrites, n.Rewrites) },
		group:   groupRegistry,
	},
	{
		name:    "preview",
		changed: func(o, n *config.Config) bool { return !reflect.DeepEqual(o.Preview, n.Preview) },
		// Pending: needs the preview dispatcher rebuilt (window size
		// half rides the window investigation) (Phase B).
	},
}

// applyConfig diffs next against the currently applied configuration
// and runs the changed sections' appliers (shared groups once per
// pass), then makes next the applied baseline. origin labels the log
// lines ("gui-save", "external-edit"). A nil baseline (a pass before
// Startup seeded it) applies every section -- appliers are idempotent,
// so over-applying is safe. Returns what was applied, what is still
// pending a live path, and any per-section failures.
func (a *App) applyConfig(next *config.Config, origin string) ApplyResult {
	a.cfgMu.Lock()
	old := a.cfgCurrent
	a.cfgMu.Unlock()

	var res ApplyResult
	groups := make(map[string]bool)
	for _, s := range sectionAppliers {
		if old != nil && !s.changed(old, next) {
			continue
		}
		if s.apply == nil && s.group == "" {
			res.Pending = append(res.Pending, s.name)
			continue
		}
		failed := false
		if s.apply != nil {
			if err := s.apply(a, next); err != nil {
				res.Errors = append(res.Errors, s.name+": "+err.Error())
				failed = true
			}
		}
		if s.group != "" && !groups[s.group] {
			groups[s.group] = true
			if err := applyGroups[s.group](a, next); err != nil {
				res.Errors = append(res.Errors, s.name+": "+err.Error())
				failed = true
			}
		}
		if !failed {
			res.Applied = append(res.Applied, s.name)
			log.Printf("config: applied %s live (origin=%s)", s.name, origin)
		}
	}
	if len(res.Pending) > 0 {
		log.Printf("config: %d section(s) await a live applier (origin=%s): %v", len(res.Pending), origin, res.Pending)
	}

	a.cfgMu.Lock()
	a.cfgCurrent = next
	a.cfgMu.Unlock()
	return res
}

// handleConfigFileChange is the config-dir watcher's hook for
// config.json events (theme.go batches them behind the same debounce
// theme:changed uses): skip the app's own just-saved bytes, otherwise
// reload and hot-apply, reporting the outcome to the frontend via
// eventConfigChanged. Resilient by construction -- any failure logs
// and keeps the previous config applied.
func (a *App) handleConfigFileChange() {
	p, err := config.Path()
	if err != nil {
		return
	}
	if data, rerr := os.ReadFile(p); rerr == nil && sha256.Sum256(data) == a.getLastSavedSum() {
		return // our own save landing on disk; already applied
	}
	cfg, lerr := config.Load()
	if lerr != nil {
		log.Printf("config: reload after external edit failed: %v (keeping the previous config)", lerr)
		a.emitEvent(eventConfigChanged, configChangedEvent{Error: lerr.Error()})
		return
	}
	res := a.applyConfig(&cfg, "external-edit")
	a.emitEvent(eventConfigChanged, configChangedEvent{Applied: res.Applied, Pending: res.Pending})
}
