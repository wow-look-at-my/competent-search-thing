package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

// builtinTimeout is the per-query timeout for builtin providers
// (external plugins use their manifest timeout_ms instead).
const builtinTimeout = 1500 * time.Millisecond

// maxDebounce caps a manifest's debounce_ms, which arrives unclamped.
const maxDebounce = 2 * time.Second

// provider is one source of virtual results: an external plugin
// (manifest + transport) or a builtin. Implementations are read-only
// after registration and safe for concurrent use.
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
	// query produces results for one request. dropped carries
	// sanitizer reasons worth logging even on success.
	query(ctx context.Context, req Request) (results []Result, dropped []string, err error)
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
	// OpenWindows supplies the open-window snapshot for the builtin
	// window-title search. Unlike InstalledApps, nil means the
	// provider is NOT REGISTERED at all: the app layer passes nil on
	// sessions where windows cannot be enumerated (Wayland,
	// windows/darwin for now), so the section never exists there.
	OpenWindows func() []WindowInfo
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
	suggest   provider // builtin bang-suggestions provider (nil when disabled)
	bangs     *BangSet
	client    *http.Client // shared by all HTTP plugins (nil when none)
	logf      func(format string, args ...any)
	throttle  *logThrottle
	errs      []error
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
		byID:     map[string]provider{},
		bangs:    NewBangSet(opts.Sigils, opts.Aliases),
		logf:     logf,
		throttle: newLogThrottle(logf, logThrottleWindow, nil),
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
// builtin providers: fixed id/name/bangs, no debounce, and no
// non-targeted matching (the builtins are bang-targeted or
// registry-special-cased, except apps-search, which overrides match
// with its all_queries trigger).
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

// addBuiltins registers the builtin providers (bang suggestions, app
// commands, installed-app launcher, untargeted app search,
// open-windows search) unless
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
		r.register(newAppsProvider(opts.InstalledApps))
	}
	if !disabled(builtinAppsSearchID) {
		r.register(newAppsSearchProvider(opts.InstalledApps))
	}
	if opts.OpenWindows != nil && !disabled(builtinWindowsID) {
		r.register(newWindowsProvider(opts.OpenWindows))
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

// TargetInfo tells the frontend whether a query was bang-targeted, so
// it can render a target chip in the query row. The zero value means
// "not targeted".
type TargetInfo struct {
	Targeted bool   `json:"targeted"`
	Plugin   string `json:"plugin"`
	Name     string `json:"name"`
	Bang     string `json:"bang"`
}

// Emission is one provider's answer for one query generation, shaped
// like the "plugin:results" Wails event payload.
type Emission struct {
	Plugin  string   `json:"plugin"`
	Name    string   `json:"name"`
	Gen     int64    `json:"gen"`
	Results []Result `json:"results"`
}

// Dispatch fans a query out to the matching providers, one goroutine
// each, and returns immediately with the bang-target info. emit is
// called once per provider that produced results -- FROM THE
// PROVIDER'S GOROUTINE, so emit must be goroutine-safe. Cancel ctx to
// abort everything in flight (debounce sleeps, subprocesses, HTTP
// requests); nothing is emitted after ctx is cancelled.
//
// Routing: an empty/whitespace query dispatches nothing. A query
// whose bang name resolves (exact, alias, or unique prefix) AND has a
// space after the name targets ONLY that provider, bypassing all
// trigger gating. A bang-shaped query that does not resolve but has
// candidate completions -- bare sigil, partial or ambiguous name, or
// a resolved name still missing its space -- dispatches ONLY the
// builtin suggestions provider. Anything else (including sigil text
// with no matching bang at all) takes the normal path: every provider
// whose trigger matches.
func (r *Registry) Dispatch(ctx context.Context, query string, gen int64, appCtx *RequestContext, emit func(Emission)) TargetInfo {
	if strings.TrimSpace(query) == "" {
		return TargetInfo{}
	}
	if bq, ok := r.bangs.Parse(query); ok {
		if pid, canonical, resolved := r.bangs.Resolve(bq.Name); resolved {
			if p, ok := r.byID[pid]; ok {
				if bq.HasSpace {
					req := baseRequest(query, strings.TrimSpace(bq.Rest), gen, appCtx)
					req.Targeted = true
					req.Bang = canonical
					r.dispatchOne(ctx, p, req, 0, 0, emit)
					return TargetInfo{Targeted: true, Plugin: p.id(), Name: p.displayName(), Bang: canonical}
				}
				r.dispatchSuggestions(ctx, query, gen, appCtx, emit)
				return TargetInfo{}
			}
			// Resolved bangs always map to registered providers; fall
			// through to the normal path defensively if not.
		} else if len(r.bangs.Candidates(bq.Name)) > 0 {
			r.dispatchSuggestions(ctx, query, gen, appCtx, emit)
			return TargetInfo{}
		}
		// No candidates at all: treat the sigil text as a plain query.
	}
	var focused *AppInfo
	if appCtx != nil {
		focused = appCtx.FocusedApp
	}
	for _, p := range r.providers {
		stripped, boost, ok := p.match(query, focused)
		if !ok {
			continue
		}
		r.dispatchOne(ctx, p, baseRequest(query, stripped, gen, appCtx), boost, p.debounce(), emit)
	}
	return TargetInfo{}
}

// dispatchSuggestions routes a bare/partial/ambiguous bang query to
// the builtin suggestions provider (a no-op when it is disabled).
func (r *Registry) dispatchSuggestions(ctx context.Context, query string, gen int64, appCtx *RequestContext, emit func(Emission)) {
	if r.suggest == nil {
		return
	}
	r.dispatchOne(ctx, r.suggest, baseRequest(query, strings.TrimSpace(query), gen, appCtx), 0, 0, emit)
}

// CheatSheet returns the bang command cheat sheet: exactly what the
// builtin suggestions provider yields for a bare primary sigil -- the
// same list typing "!" shows -- as one synchronous Emission (Gen 0).
// The frontend renders it for an empty query, so no dispatch fan-out,
// goroutines, or plugin subprocesses are involved. When the
// suggestions provider is disabled per-entry the zero Emission (no
// results) comes back.
func (r *Registry) CheatSheet() Emission {
	if r.suggest == nil {
		return Emission{}
	}
	sigil := r.bangs.Primary()
	results, _, err := r.suggest.query(context.Background(), baseRequest(sigil, sigil, 0, nil))
	if err != nil {
		r.logf("plugin %s: cheat sheet: %v", r.suggest.id(), err)
		return Emission{}
	}
	return Emission{Plugin: r.suggest.id(), Name: r.suggest.displayName(), Results: results}
}

// dispatchOne runs one provider query on its own goroutine: optional
// debounce sleep (cancelled by ctx), per-provider timeout, throttled
// error/drop logging, boost application, and the emit guard (results
// only, never after cancellation). Panics are recovered and logged so
// a broken provider cannot take the app down.
func (r *Registry) dispatchOne(ctx context.Context, p provider, req Request, boost int, wait time.Duration, emit func(Emission)) {
	go func() {
		defer func() {
			if v := recover(); v != nil {
				r.throttle.logf(p.id(), "plugin %s: panic during dispatch: %v", p.id(), v)
			}
		}()
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
		qctx, cancel := context.WithTimeout(ctx, providerTimeout(p))
		defer cancel()
		results, dropped, err := p.query(qctx, req)
		if err != nil {
			r.throttle.logf(p.id(), "plugin %s: %v", p.id(), err)
			return
		}
		if len(dropped) > 0 {
			r.throttle.logf(p.id(), "plugin %s: sanitizer: %s", p.id(), strings.Join(dropped, "; "))
		}
		applyBoost(results, boost)
		if len(results) > 0 && ctx.Err() == nil {
			emit(Emission{Plugin: p.id(), Name: p.displayName(), Gen: req.Gen, Results: results})
		}
	}()
}

// baseRequest assembles the request fields shared by every dispatch
// mode. Providers fill in their own Settings/Context filtering.
func baseRequest(query, stripped string, gen int64, appCtx *RequestContext) Request {
	return Request{
		V:        ProtocolVersion,
		Query:    query,
		Stripped: stripped,
		Gen:      gen,
		Context:  appCtx,
	}
}

// providerTimeout picks the per-query timeout: the manifest's
// timeout_ms for external plugins, builtinTimeout otherwise. A
// non-positive manifest timeout (impossible via LoadDir) falls back
// to the default rather than expiring instantly.
func providerTimeout(p provider) time.Duration {
	if ep, ok := p.(*externalProvider); ok {
		ms := ep.m.TimeoutMS
		if ms <= 0 {
			ms = defaultTimeoutMS
		}
		return time.Duration(ms) * time.Millisecond
	}
	return builtinTimeout
}

// applyBoost adds a trigger's focused boost to every result score,
// clamped to 0..100.
func applyBoost(results []Result, boost int) {
	if boost == 0 {
		return
	}
	for i := range results {
		s := DefaultScore
		if results[i].Score != nil {
			s = *results[i].Score
		}
		s = math.Min(100, math.Max(0, s+float64(boost)))
		results[i].Score = &s
	}
}
