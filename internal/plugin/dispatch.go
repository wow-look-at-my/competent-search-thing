package plugin

// The Registry's dispatch pipeline: the wire TargetInfo/Emission
// shapes, query fan-out and routing, the per-provider goroutine, and
// the bang cheat sheet. Split from registry.go, which keeps the
// provider interface, Options, New, and builtin registration.

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

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
// like the "plugin:results" Wails event payload. Priority is the
// provider's source priority (the prioritized extension, stamped
// HERE per emission from the strongest minted tier, never by a
// provider response): sections with priority > 0 render in the
// frontend zone ABOVE the file results, and the magnitude orders
// prioritized sections among themselves. External plugins can never
// set it -- their wire Response carries no such field and
// *externalProvider does not implement prioritized.
type Emission struct {
	Plugin   string   `json:"plugin"`
	Name     string   `json:"name"`
	Gen      int64    `json:"gen"`
	Results  []Result `json:"results"`
	Priority int      `json:"priority,omitempty"`
}

// providerPriority reads a provider's optional source priority for an
// emission whose strongest engine-minted tier is best (the
// prioritized extension); every provider without it is 0.
func providerPriority(p provider, best match.Tier) int {
	if pr, ok := p.(prioritized); ok {
		return pr.priority(best)
	}
	return 0
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
	results, best, err := sourceResults(r.suggest, context.Background(), baseRequest(sigil, sigil, 0, nil), r.fuzzyDisabled)
	if err != nil {
		r.logf("plugin %s: cheat sheet: %v", r.suggest.id(), err)
		return Emission{}
	}
	return Emission{
		Plugin:   r.suggest.id(),
		Name:     r.suggest.displayName(),
		Results:  results,
		Priority: providerPriority(r.suggest, best), // suggestions are not prioritized: 0
	}
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
		var results []Result
		var dropped []string
		var err error
		// The strongest tier the engine minted for this emission --
		// the input to per-emission section priority. Only builtin
		// candidate sources fill it; external providers cannot
		// implement prioritized, so TierNone is fine for them.
		bestTier := match.TierNone
		switch src := p.(type) {
		case candidateSource:
			// Builtins: raw candidates through the engine mint.
			results, bestTier, err = sourceResults(src, qctx, req, r.fuzzyDisabled)
		case *externalProvider:
			// External plugins: sanitize, then the engine pass --
			// triggered tier for claimed queries, text gating for the
			// all-queries fan-out.
			results, dropped, err = src.query(qctx, req)
			if err == nil {
				claimed := req.Targeted || src.claims(req.Query)
				var engineDropped []string
				results, engineDropped = rankExternal(results, req, claimed, r.fuzzyDisabled)
				dropped = append(dropped, engineDropped...)
			}
		case resultProvider:
			// Test doubles only: production providers are always one of
			// the two shapes above (enforced by the routing test).
			results, dropped, err = src.query(qctx, req)
		}
		if err != nil {
			r.throttle.logf(p.id(), "plugin %s: %v", p.id(), err)
			return
		}
		if len(dropped) > 0 {
			r.throttle.logf(p.id(), "plugin %s: sanitizer: %s", p.id(), strings.Join(dropped, "; "))
		}
		applyBoost(results, boost)
		if len(results) > 0 && ctx.Err() == nil {
			emit(Emission{
				Plugin:   p.id(),
				Name:     p.displayName(),
				Gen:      req.Gen,
				Results:  results,
				Priority: providerPriority(p, bestTier),
			})
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
