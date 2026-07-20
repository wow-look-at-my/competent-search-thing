package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// builtinTimeout is the per-query timeout for builtin providers
// (external plugins use their manifest timeout_ms instead).
const builtinTimeout = 1500 * time.Millisecond

// maxDebounce caps a manifest's debounce_ms, which arrives unclamped.
const maxDebounce = 2 * time.Second

// provider is one source of virtual results: an external plugin
// (manifest + transport) or a builtin candidate source. The answering
// side lives in the two sub-interfaces (engine.go): candidateSource
// for builtins (raw candidates minted by the shared engine) and
// resultProvider for externals (wire results, sanitized then
// engine-passed). Implementations are read-only after registration
// and safe for concurrent use.
type provider interface {
	id() string
	displayName() string
	// match decides NON-targeted dispatch: whether the provider wants
	// this query, the stripped query it should receive, and the score
	// boost to apply. Bang targeting bypasses match entirely.
	match(query string, focused *AppInfo) (stripped string, boost int, ok bool)
	// bangNames lists the bang names to register for this provider.
	bangNames() []string
	// debounce is the extra delay before dispatch (0..maxDebounce).
	debounce() time.Duration
}

// externalProvider adapts a loaded Manifest + transport to the
// provider interface. Sanitization happens HERE, on every response --
// builtin providers are trusted in-process code and bypass it (they
// may produce internal-only actions like set_query/run_builtin).
type externalProvider struct {
	m        *Manifest
	tr       transport
	settings json.RawMessage // per-plugin settings, always a JSON object
}

func (p *externalProvider) id() string          { return p.m.ID }
func (p *externalProvider) displayName() string { return p.m.Name }
func (p *externalProvider) bangNames() []string { return p.m.Bangs }

func (p *externalProvider) debounce() time.Duration {
	if p.m.Trigger == nil {
		return 0
	}
	return clampDebounce(p.m.Trigger.DebounceMS)
}

func (p *externalProvider) match(query string, focused *AppInfo) (string, int, bool) {
	if p.m.Trigger == nil {
		return "", 0, false // bang-targeted-only plugin
	}
	stripped, ok := p.m.Trigger.Match(query, focused)
	if !ok {
		return "", 0, false
	}
	return stripped, p.m.Trigger.Boost(focused), true
}

func (p *externalProvider) query(ctx context.Context, req Request) ([]Result, []string, error) {
	req.Settings = p.settings
	req.Context = filterContext(req.Context, p.m.Context)
	resp, err := p.tr.roundTrip(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	results, dropped := SanitizeResponse(resp, p.m.AllowRunCommand)
	return results, dropped, nil
}

// claims reports whether the plugin's trigger CLAIMED the query (its
// prefix or regex path matched): such results enter the engine at the
// triggered tier -- they answer the query rather than text-match it.
func (p *externalProvider) claims(query string) bool {
	return p.m.Trigger != nil && p.m.Trigger.Claims(query)
}

// filterContext narrows an app-context snapshot to the parts a
// manifest declared. Undeclared (or empty) parts are omitted; when
// nothing remains -- or nothing was declared -- the whole context is
// nil, so the "context" field is absent from the request JSON.
func filterContext(rc *RequestContext, declared []string) *RequestContext {
	if rc == nil || len(declared) == 0 {
		return nil
	}
	out := &RequestContext{}
	has := false
	for _, part := range declared {
		switch part {
		case "focused":
			if rc.FocusedApp != nil {
				out.FocusedApp = rc.FocusedApp
				has = true
			}
		case "running":
			if len(rc.RunningApps) > 0 {
				out.RunningApps = rc.RunningApps
				has = true
			}
		case "installed":
			if len(rc.InstalledApps) > 0 {
				out.InstalledApps = rc.InstalledApps
				has = true
			}
		}
	}
	if !has {
		return nil
	}
	return out
}

// clampDebounce turns a manifest debounce_ms (unvalidated, possibly
// negative or huge) into 0..maxDebounce.
func clampDebounce(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	if d := time.Duration(ms) * time.Millisecond; d <= maxDebounce {
		return d
	}
	return maxDebounce
}

// Entry mirrors the per-plugin config entry (config.PluginEntry)
// without importing internal/config: a disable flag plus the opaque
// settings object forwarded to the plugin in every request.
type Entry struct {
	Disabled bool
	Settings json.RawMessage
}

// Options configures New.
type Options struct {
	// Manifests are the loaded plugin manifests (from LoadDir).
	Manifests []*Manifest
	// LoadErrors are manifest-loading problems, folded into Errors()
	// so the app logs everything in one place.
	LoadErrors []error
	// Sigils and Aliases configure the bang set (config.json "bangs").
	Sigils  []string
	Aliases map[string]string
	// AllDisabled is the global kill switch: no providers at all,
	// builtins included.
	AllDisabled bool
	// Entries holds per-provider config keyed by id (builtin ids work
	// here too).
	Entries map[string]Entry
	// Version is the app version shown by the builtin "version"
	// command.
	Version string
	// InstalledApps supplies the installed-application snapshot for
	// the builtin launcher; nil means the launcher returns nothing.
	InstalledApps func() []InstalledApp
	// AppUsage reports the decayed launch count recorded under an
	// installed app's usage key (AppUsageKey) -- the app layer's
	// frecency store behind a live accessor. The two app sources use
	// it as the within-tier tie-break: equal-class rows order by
	// usage (higher first), then name. nil (frecency disabled,
	// tests) or an all-zero store keeps the pure name order.
	AppUsage func(key string) float64
	// OpenWindows supplies the open-window snapshot for the builtin
	// window-title search. Unlike InstalledApps, nil means the
	// provider is NOT REGISTERED at all: the app layer passes nil on
	// sessions where windows cannot be enumerated (Wayland,
	// windows/darwin for now), so the section never exists there.
	OpenWindows func() []WindowInfo
	// FrequentSites supplies the frequently-visited-sites snapshot for
	// the builtin firefox-frequent provider; nil (no Firefox profile)
	// means the provider is not registered at all.
	FrequentSites func() []SiteInfo
	// FrequentSitesMax caps one firefox-frequent response (config
	// firefox.frequentSites.maxResults; non-positive = the default 6).
	FrequentSitesMax int
	// OpenTabs supplies the open-Firefox-tabs snapshot for the builtin
	// firefox-tabs provider; nil (no Firefox profile) means the
	// provider is not registered at all.
	OpenTabs func() []TabInfo
	// OpenTabsMax caps one firefox-tabs response (config
	// firefox.openTabs.maxResults; non-positive = the default 6).
	OpenTabsMax int
	// FuzzyDisabled turns the engine's fuzzy (subsequence) tier off
	// for every candidate source and text-matched external result
	// (config search.fuzzyDisabled -- the same toggle that governs the
	// file index). Term splitting applies regardless.
	FuzzyDisabled bool
	// Rewrites are the config "rewrites" rules for the builtin regex
	// rewrite source; invalid rules are collected into Errors() and
	// skipped.
	Rewrites []RewriteRule
	// Logf receives all registry logging (default log.Printf).
	Logf func(format string, args ...any)
}

// Registry owns the enabled providers, the bang set, and the dispatch
// pipeline. Build one with New at startup; to reload plugins, build a
// NEW Registry and swap it in (a Registry is immutable after New and
// safe for concurrent Dispatch calls), then Close the old one.
type Registry struct {
	providers []provider
	byID      map[string]provider
	suggest   candidateSource // builtin bang-suggestions source (nil when disabled)
	bangs     *BangSet
	client    *http.Client // shared by all HTTP plugins (nil when none)
	logf      func(format string, args ...any)
	throttle  *logThrottle
	errs      []error
	// fuzzyDisabled is Options.FuzzyDisabled, threaded into every
	// engine Rank call.
	fuzzyDisabled bool
}

// New builds a Registry from loaded manifests and config-shaped
// options. It never fails: anything wrong (bad sigils, duplicate
// bangs, manifest load errors) is collected for Errors() and skipped.
func New(opts Options) *Registry {
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	r := &Registry{
		byID:          map[string]provider{},
		bangs:         NewBangSet(opts.Sigils, opts.Aliases),
		logf:          logf,
		throttle:      newLogThrottle(logf, logThrottleWindow, nil),
		fuzzyDisabled: opts.FuzzyDisabled,
	}
	r.errs = append(r.errs, opts.LoadErrors...)
	r.errs = append(r.errs, r.bangs.Errors()...)
	if opts.AllDisabled {
		return r
	}
	disabled := func(id string) bool {
		e, ok := opts.Entries[id]
		return ok && e.Disabled
	}
	r.addBuiltins(opts, disabled)
	for _, m := range opts.Manifests {
		if disabled(m.ID) {
			continue
		}
		if _, taken := r.byID[m.ID]; taken {
			r.errs = append(r.errs, fmt.Errorf(
				"plugin %q: id already taken by another provider; skipped", m.ID))
			continue
		}
		settings := json.RawMessage("{}")
		if e, ok := opts.Entries[m.ID]; ok && len(e.Settings) > 0 {
			settings = e.Settings
		}
		var tr transport
		switch m.Type {
		case TypeCommand:
			tr = &commandTransport{m: m}
		case TypeHTTP:
			if r.client == nil {
				r.client = newHTTPClient()
			}
			tr = &httpTransport{m: m, client: r.client}
		default: // unreachable for LoadDir-validated manifests
			r.errs = append(r.errs, fmt.Errorf("plugin %q: unknown type %q; skipped", m.ID, m.Type))
			continue
		}
		r.register(&externalProvider{m: m, tr: tr, settings: settings})
	}
	return r
}

// builtinBase supplies the boring provider methods shared by the
// builtin candidate sources: fixed id/name/bangs, no debounce, no
// non-targeted matching (overridden by the all-queries sources), and
// not preRanked (overridden by the query-derived sources: bang
// suggestions, app commands, rewrite rules).
type builtinBase struct {
	pid   string
	name  string
	bangs []string
}

func (b *builtinBase) id() string                                 { return b.pid }
func (b *builtinBase) displayName() string                        { return b.name }
func (b *builtinBase) bangNames() []string                        { return b.bangs }
func (b *builtinBase) debounce() time.Duration                    { return 0 }
func (b *builtinBase) match(string, *AppInfo) (string, int, bool) { return "", 0, false }
func (b *builtinBase) preRanked() bool                            { return false }

// addBuiltins registers the builtin providers (bang suggestions, app
// commands, installed-app launcher, untargeted app search,
// open-windows search, frequent sites, open tabs) unless
// individually disabled -- the open-windows search additionally needs
// its Options.OpenWindows seam, which is nil on sessions that cannot
// enumerate windows. Builtins register BEFORE external plugins so a
// manifest can never shadow an app bang or claim a builtin id.
func (r *Registry) addBuiltins(opts Options, disabled func(string) bool) {
	if !disabled(builtinSuggestID) {
		s := newBangSuggestProvider(r)
		// Special-cased by Dispatch, never part of the normal fan-out:
		// present in byID (so the id stays reserved) but not providers.
		r.suggest = s
		r.byID[builtinSuggestID] = s
	}
	if !disabled(builtinAppID) {
		r.register(newAppCommandProvider(opts.Version))
	}
	if !disabled(builtinAppsID) {
		r.register(newAppsProvider(opts.InstalledApps, opts.AppUsage))
	}
	if !disabled(builtinAppsSearchID) {
		r.register(newAppsSearchProvider(opts.InstalledApps, opts.AppUsage))
	}
	if opts.OpenWindows != nil && !disabled(builtinWindowsID) {
		r.register(newWindowsProvider(opts.OpenWindows))
	}
	// The Firefox-backed providers exist only when the app layer found
	// a Firefox profile and supplied their sources (see Options).
	if opts.FrequentSites != nil && !disabled(builtinFirefoxID) {
		r.register(newFirefoxProvider(opts.FrequentSites, opts.FrequentSitesMax))
	}
	if opts.OpenTabs != nil && !disabled(builtinTabsID) {
		r.register(newTabsProvider(opts.OpenTabs, opts.OpenTabsMax))
	}
	if len(opts.Rewrites) > 0 && !disabled(builtinRewritesID) {
		rules, errs := compileRewrites(opts.Rewrites)
		r.errs = append(r.errs, errs...)
		if len(rules) > 0 {
			r.register(newRewritesProvider(rules, r.logf))
		}
	}
}

// register adds a provider to the dispatch list and claims its bangs.
// A duplicate bang is recorded as an error; the first registration
// wins.
func (r *Registry) register(p provider) {
	r.providers = append(r.providers, p)
	r.byID[p.id()] = p
	for _, b := range p.bangNames() {
		if err := r.bangs.Register(b, p.id()); err != nil {
			r.errs = append(r.errs, err)
		}
	}
}

// Errors returns everything New collected (manifest load errors, bad
// sigils, duplicate bangs/ids) for the app to log once at startup.
func (r *Registry) Errors() []error { return r.errs }

// Close releases pooled resources (idle HTTP connections). In-flight
// dispatches are unaffected; cancel their context to stop them.
func (r *Registry) Close() {
	if r.client != nil {
		r.client.CloseIdleConnections()
	}
}
