package plugin

// The source-priority pin family for the two Firefox builtin web
// sources (firefox-frequent + firefox-tabs), mirroring the apps-search
// family in builtin_apps_search_test.go: a strong (word-start tier or
// better) best row promotes the section above the file results cut to
// its strong rows, a weak (substring/fuzzy) best keeps it below the
// files with every row intact, and the promotion is placement
// metadata that never touches a minted score. The driving field
// report: searching "tampermonkey" rendered the exact-title open-tab
// row BELOW every file result, weak fuzzy matches included, because
// only apps-search implemented the prioritized extension.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// dispatchSites drives one full registry dispatch with only the
// frequent-sites web source registered and returns its emission.
func dispatchSites(t *testing.T, query string, sites []SiteInfo) Emission {
	t.Helper()
	r := New(Options{FrequentSites: sitesFn(sites), Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), query, 1, nil, emit)
	return recvEmission(t, ch)
}

// dispatchTabs is the open-tabs twin of dispatchSites.
func dispatchTabs(t *testing.T, query string, tabs []TabInfo) Emission {
	t.Helper()
	r := New(Options{OpenTabs: tabsFn(tabs), Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), query, 1, nil, emit)
	return recvEmission(t, ch)
}

func TestWebSourcePriorityMetadata(t *testing.T) {
	// Both Firefox web sources implement the prioritized extension
	// with the SAME tier gate as apps-search: strong tiers (triggered/
	// exact/prefix/word-start) promote at sourcePriorityWeb, weak
	// tiers (substring/fuzzy/none) stay at 0, below the file results.
	require.Equal(t, 1, sourcePriorityWeb)
	providers := map[string]provider{
		builtinFirefoxID: newFirefoxProvider(func() []SiteInfo { return nil }, 0),
		builtinTabsID:    newTabsProvider(func() []TabInfo { return nil }, 0),
	}
	for name, p := range providers {
		for _, tier := range []match.Tier{match.TierTriggered, match.TierExact, match.TierPrefix, match.TierWordStart} {
			require.Equal(t, sourcePriorityWeb, providerPriority(p, tier),
				"%s: strong tier %d promotes", name, tier)
		}
		for _, tier := range []match.Tier{match.TierSubstring, match.TierFuzzy, match.TierNone} {
			require.Zero(t, providerPriority(p, tier),
				"%s: weak tier %d stays below the files", name, tier)
		}
	}
}

// TestFirefoxWeakMatchesStayBelowFiles pins the tier gate at dispatch
// level for the frequent-sites source: weak bests emit at priority 0
// with every row kept (the section renders, just below the files),
// strong bests earn the promotion.
func TestFirefoxWeakMatchesStayBelowFiles(t *testing.T) {
	// Substring-tier bests are weak: two mid-word title hits, both
	// delivered, canonical substring band untouched.
	e := dispatchSites(t, "fire", []SiteInfo{
		{URL: "https://example.com/chat", Title: "Campfire Chat", Host: "example.com", Visits: 10},
		{URL: "https://example.org/talk", Title: "Bonfire Talk", Host: "example.org", Visits: 5},
	})
	require.Equal(t, builtinFirefoxID, e.Plugin)
	require.Zero(t, e.Priority, "substring-tier site matches render below the file results")
	require.Len(t, e.Results, 2, "a weak-only section keeps all its rows")
	require.Equal(t, float64(53), *e.Results[0].Score, "the canonical substring band is untouched")

	// Fuzzy-only bests are weak too.
	e = dispatchSites(t, "gthb", testSites())
	require.Zero(t, e.Priority, "fuzzy-only site matches render below the file results")
	require.Less(t, *e.Results[0].Score, float64(53), "fuzzy band: strictly below every literal band")

	// Strong bests keep the promotion: host prefix...
	e = dispatchSites(t, "github", testSites())
	require.Equal(t, sourcePriorityWeb, e.Priority, "host-prefix matches promote the section")

	// ... exact host ...
	e = dispatchSites(t, "github.com", testSites())
	require.Equal(t, sourcePriorityWeb, e.Priority, "exact-host matches promote the section")

	// ... and word start.
	e = dispatchSites(t, "ycombinator", testSites())
	require.Equal(t, sourcePriorityWeb, e.Priority, "word-start matches promote the section")
}

// TestTabsWeakMatchesStayBelowFiles is the open-tabs twin, including
// the driving "tampermonkey" field-report shape: the exact/prefix
// title match on an open tab must render above the file results.
func TestTabsWeakMatchesStayBelowFiles(t *testing.T) {
	// The field report's shape: an open tab whose title strongly
	// matches the query earns the above-files zone.
	e := dispatchTabs(t, "tampermonkey", []TabInfo{
		{URL: "https://github.com/Tampermonkey/tampermonkey", Title: "Tampermonkey - the userscript manager", Host: "github.com", LastAccessed: 900},
	})
	require.Equal(t, builtinTabsID, e.Plugin)
	require.Equal(t, sourcePriorityWeb, e.Priority, "a title-prefix open-tab match promotes the section")
	require.Equal(t, float64(73), *e.Results[0].Score, "the canonical prefix band is untouched")

	// Substring-tier bests are weak: all rows kept, priority 0.
	e = dispatchTabs(t, "fire", []TabInfo{
		{URL: "https://forum.example/campfire", Title: "Campfire stories", Host: "forum.example", LastAccessed: 100},
		{URL: "https://forum.example/bonfire", Title: "Bonfire night", Host: "forum.example", LastAccessed: 50},
	})
	require.Zero(t, e.Priority, "substring-tier tab matches render below the file results")
	require.Len(t, e.Results, 2, "a weak-only section keeps all its rows")
	require.Equal(t, float64(53), *e.Results[0].Score, "the canonical substring band is untouched")

	// Fuzzy-only bests are weak too.
	e = dispatchTabs(t, "gthb", testTabs())
	require.Zero(t, e.Priority, "fuzzy-only tab matches render below the file results")
	require.Less(t, *e.Results[0].Score, float64(53), "fuzzy band: strictly below every literal band")

	// Word-start bests promote.
	e = dispatchTabs(t, "started", testTabs())
	require.Equal(t, sourcePriorityWeb, e.Priority, "word-start matches promote the section")
}

// TestFirefoxPromotedSectionStrongRowsOnly: a promoted frequent-sites
// emission is cut to its strong rows -- a weak row must never ride
// the above-files zone -- while the SAME weak rows render whenever no
// strong match exists (whole section at priority 0).
func TestFirefoxPromotedSectionStrongRowsOnly(t *testing.T) {
	sites := []SiteInfo{
		{URL: "https://firefox.com/download", Title: "Firefox Browser", Host: "firefox.com", Visits: 50}, // host prefix: strong
		{URL: "https://example.com/chat", Title: "Campfire Chat", Host: "example.com", Visits: 10},       // title substring: weak
	}
	e := dispatchSites(t, "fire", sites)
	require.Equal(t, sourcePriorityWeb, e.Priority)
	require.Len(t, e.Results, 1, "the promoted section carries only its strong rows")
	require.Equal(t, "Firefox Browser", e.Results[0].Title)

	// Drop the strong site: the weak row is back, below the files.
	e = dispatchSites(t, "fire", sites[1:])
	require.Zero(t, e.Priority)
	require.Len(t, e.Results, 1)
	require.Equal(t, "Campfire Chat", e.Results[0].Title)
}

// TestTabsPromotedSectionStrongRowsOnly is the open-tabs twin of the
// strong-rows-only cut.
func TestTabsPromotedSectionStrongRowsOnly(t *testing.T) {
	tabs := []TabInfo{
		{URL: "https://nightly.mozilla.org/", Title: "Firefox Nightly", Host: "nightly.mozilla.org", LastAccessed: 900}, // title prefix: strong
		{URL: "https://forum.example/campfire", Title: "Campfire stories", Host: "forum.example", LastAccessed: 100},    // substring: weak
	}
	e := dispatchTabs(t, "fire", tabs)
	require.Equal(t, sourcePriorityWeb, e.Priority)
	require.Len(t, e.Results, 1, "the promoted section carries only its strong rows")
	require.Equal(t, "Firefox Nightly", e.Results[0].Title)

	// Drop the strong tab: the weak row is back, below the files.
	e = dispatchTabs(t, "fire", tabs[1:])
	require.Zero(t, e.Priority)
	require.Len(t, e.Results, 1)
	require.Equal(t, "Campfire stories", e.Results[0].Title)
}

// TestWebPriorityNeverChangesMintedScores: the engine mint through
// the registry dispatch must be byte-identical to the direct
// sourceResults mint for both web sources -- the priority is carried
// NEXT TO the results, never inside them, and the canonical bands
// are untouched. (Both sides run the same strong-rows-only cut for a
// promoted section -- the cut drops whole rows, it never rewrites a
// minted score.)
func TestWebPriorityNeverChangesMintedScores(t *testing.T) {
	sites := []SiteInfo{
		{URL: "https://firefox.com/download", Title: "Firefox Browser", Host: "firefox.com", Visits: 50},
		{URL: "https://example.com/chat", Title: "Campfire Chat", Host: "example.com", Visits: 10},
	}
	directSites := srcResults(t, newFirefoxProvider(sitesFn(sites), 0), baseRequest("fire", "fire", 1, nil))
	require.Len(t, directSites, 1, "the promoted section cuts the weak substring row")
	require.Equal(t, float64(73), *directSites[0].Score, "the canonical prefix band is untouched")
	e := dispatchSites(t, "fire", sites)
	require.Equal(t, sourcePriorityWeb, e.Priority)
	require.Equal(t, directSites, e.Results, "dispatch emissions carry the exact engine mint")

	tabs := []TabInfo{
		{URL: "https://nightly.mozilla.org/", Title: "Firefox Nightly", Host: "nightly.mozilla.org", LastAccessed: 900},
		{URL: "https://forum.example/campfire", Title: "Campfire stories", Host: "forum.example", LastAccessed: 100},
	}
	directTabs := srcResults(t, newTabsProvider(tabsFn(tabs), 0), baseRequest("fire", "fire", 1, nil))
	require.Len(t, directTabs, 1, "the promoted section cuts the weak substring row")
	require.Equal(t, float64(73), *directTabs[0].Score, "the canonical prefix band is untouched")
	e = dispatchTabs(t, "fire", tabs)
	require.Equal(t, sourcePriorityWeb, e.Priority)
	require.Equal(t, directTabs, e.Results, "dispatch emissions carry the exact engine mint")
}
