package plugin

import (
	"context"
	"strings"
	"unicode"
)

// builtinAppsSearchID is the provider id of the untargeted
// installed-app matcher (disable via plugins.entries["apps-search"]).
const builtinAppsSearchID = "apps-search"

// maxAppsSearchResults caps one untargeted apps section. Normal
// queries share the bar with file results, so the section stays small;
// the targeted !app / !launch path keeps its 15.
const maxAppsSearchResults = 6

// appsSearchProvider surfaces installed applications in NORMAL search
// results: no bangs, an all_queries trigger (whose effective minimum
// query length defaults to 2 runes), and a stricter ranking than the
// targeted launcher. Registry routing keeps it mutually exclusive with
// !app / !launch: a resolved bang query dispatches ONLY the targeted
// provider and bypasses all triggers, so apps never render twice.
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

// match overrides builtinBase: this is the one builtin that fans out
// on untargeted queries, gated exactly like a manifest all_queries
// trigger (minimum 2 runes of stripped query).
func (p *appsSearchProvider) match(query string, focused *AppInfo) (string, int, bool) {
	stripped, ok := p.trigger.Match(query, focused)
	if !ok {
		return "", 0, false
	}
	return stripped, p.trigger.Boost(focused), true
}

func (p *appsSearchProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	if p.installed == nil {
		return nil, nil, nil
	}
	needle := strings.ToLower(strings.TrimSpace(req.Stripped))
	if needle == "" {
		return nil, nil, nil // unreachable via dispatch (min length 2)
	}
	return collectAppResults(p.installed(), appsSearchScore(needle), maxAppsSearchResults), nil, nil
}

// appsSearchScore ranks one installed-app name against the lowercased
// needle: exact match 100, name prefix 90, word start 75 (any word in
// the name starts with the needle, so "code" matches "Visual Studio
// Code"), substring 60; anything else is skipped. Ties break
// alphabetically in collectAppResults.
func appsSearchScore(needle string) func(name string) (float64, bool) {
	return func(name string) (float64, bool) {
		lower := strings.ToLower(name)
		switch {
		case lower == needle:
			return 100, true
		case strings.HasPrefix(lower, needle):
			return 90, true
		case wordStartMatch(lower, needle):
			return 75, true
		case strings.Contains(lower, needle):
			return 60, true
		}
		return 0, false
	}
}

// wordStartMatch reports whether any word of the (lowercased) name
// starts with needle. Words are maximal runs of letters and digits, so
// spaces, hyphens and dots all separate ("gnome-fire-manager" has a
// word "fire").
func wordStartMatch(lower, needle string) bool {
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, w := range words {
		if strings.HasPrefix(w, needle) {
			return true
		}
	}
	return false
}
