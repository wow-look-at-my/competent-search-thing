package plugin

import (
	"context"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinAppsSearchID is the provider id of the untargeted
// installed-app matcher (disable via plugins.entries["apps-search"]).
const builtinAppsSearchID = "apps-search"

// maxAppsSearchResults caps one untargeted apps section. Normal
// queries share the bar with file results, so the section stays small;
// the targeted !app / !launch path keeps its 15.
const maxAppsSearchResults = 6

// sourcePriorityApps places the untargeted apps section ABOVE the
// file results (Emission.Priority via the prioritized extension:
// priority > 0 = the frontend's above-files zone, magnitude orders
// prioritized sections among themselves). Launchable apps are what
// a Spotlight-style bar answers first; the targeted !app / !launch
// provider deliberately stays 0 -- bang queries have no file results
// to outrank, so targeted mode keeps today's layout.
const sourcePriorityApps = 1

// appsSearchProvider surfaces installed applications in NORMAL search
// results: no bangs, an all_queries trigger (whose effective minimum
// query length defaults to 2 runes). It is a candidate SOURCE: the
// whole snapshot goes to the shared engine, which applies the one
// ladder -- including multi-term ("fire fox" finds Firefox) and the
// fuzzy tier ("firefx") -- and mints every score and highlight.
// Registry routing keeps it mutually exclusive with !app / !launch: a
// resolved bang query dispatches ONLY the targeted provider and
// bypasses all triggers, so apps never render twice.
type appsSearchProvider struct {
	builtinBase
	trigger   *Trigger
	installed func() []InstalledApp
}

func newAppsSearchProvider(installed func() []InstalledApp) *appsSearchProvider {
	t := &Trigger{AllQueries: true}
	// Compile never fails without regexes; keep the call so the
	// trigger is in the same state a manifest-loaded one would be.
	_ = t.Compile()
	return &appsSearchProvider{
		builtinBase: builtinBase{pid: builtinAppsSearchID, name: "Apps"},
		trigger:     t,
		installed:   installed,
	}
}

// match overrides builtinBase: this source fans out on untargeted
// queries, gated exactly like a manifest all_queries trigger (minimum
// 2 runes of stripped query).
func (p *appsSearchProvider) match(query string, focused *AppInfo) (string, int, bool) {
	stripped, ok := p.trigger.Match(query, focused)
	if !ok {
		return "", 0, false
	}
	return stripped, p.trigger.Boost(focused), true
}

func (p *appsSearchProvider) limit() int { return maxAppsSearchResults }

// priority implements the optional prioritized extension: the apps
// section renders above the file results.
func (p *appsSearchProvider) priority() int { return sourcePriorityApps }

func (p *appsSearchProvider) candidates(_ context.Context, _ Request) ([]match.Candidate, error) {
	if p.installed == nil {
		return nil, nil
	}
	return appCandidates(p.installed()), nil
}
