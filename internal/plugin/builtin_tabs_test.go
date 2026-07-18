package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testTabs is a fixture open-tabs snapshot.
func testTabs() []TabInfo {
	return []TabInfo{
		{URL: "https://github.com/pulls", Title: "Pull requests", Host: "github.com", LastAccessed: 400},
		{URL: "https://www.google.com/search?q=go", Title: "go - Google Search", Host: "www.google.com", LastAccessed: 500},
		{URL: "https://news.ycombinator.com/", Title: "Hacker News", Host: "news.ycombinator.com", Pinned: true, LastAccessed: 300},
		{URL: "https://go.dev/doc/tutorial", Title: "Tutorial: Get started", Host: "go.dev", LastAccessed: 200},
		{URL: "https://blog.example.org/", Title: "", Host: "blog.example.org", LastAccessed: 100},
	}
}

func tabsFn(tabs []TabInfo) func() []TabInfo {
	return func() []TabInfo { return tabs }
}

// tabsQuery drives one provider query the way dispatch would.
func tabsQuery(t *testing.T, p *tabsProvider, q string) []Result {
	t.Helper()
	stripped, boost, ok := p.match(q, nil)
	if !ok {
		return nil
	}
	require.Zero(t, boost)
	return srcResults(t, p, baseRequest(q, stripped, 1, nil))
}

func TestTabsProviderMatchGatesShortQueries(t *testing.T) {
	p := newTabsProvider(tabsFn(nil), 0)
	for _, q := range []string{"", " ", "g", " g "} {
		_, _, ok := p.match(q, nil)
		require.False(t, ok, "query %q is under the 2-rune minimum", q)
	}
	stripped, boost, ok := p.match("  git  ", nil)
	require.True(t, ok)
	require.Equal(t, "git", stripped, "the stripped query is trimmed")
	require.Zero(t, boost)
}

func TestTabsProviderRanking(t *testing.T) {
	p := newTabsProvider(tabsFn(testTabs()), 0)
	tests := []struct {
		query string
		want  string  // first result title
		score float64 // its score
	}{
		{query: "pull", want: "Pull requests", score: 73},            // title prefix
		{query: "hacker", want: "Hacker News", score: 73},            // title prefix
		{query: "started", want: "Tutorial: Get started", score: 63}, // title word start
		// The TITLE field outranks the host field within a tier: "go"
		// title-prefixes "go - Google Search" AND host-prefixes go.dev;
		// the title hit wins the tie.
		{query: "go", want: "go - Google Search", score: 73},
		{query: "github", want: "Pull requests", score: 73},          // host prefix
		{query: "news.y", want: "Hacker News", score: 73},            // host prefix
		{query: "blog.example", want: "blog.example.org", score: 73}, // host prefix
		{query: "tarted", want: "Tutorial: Get started", score: 53},  // mid-word title hit
		{query: "ycombinator", want: "Hacker News", score: 63},       // host segment = word start (after '.')
		{query: "pulls", want: "Pull requests", score: 63},           // URL path segment = word start (after '/')
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			results := tabsQuery(t, p, tt.query)
			require.NotEmpty(t, results)
			require.Equal(t, tt.want, results[0].Title)
			require.Equal(t, tt.score, *results[0].Score)
		})
	}
}

func TestTabsProviderTiebreaks(t *testing.T) {
	// Equal scores fall back to the most recently used tab.
	p := newTabsProvider(tabsFn([]TabInfo{
		{URL: "https://a.example/older", Title: "Docs older", Host: "a.example", LastAccessed: 100},
		{URL: "https://b.example/newer", Title: "Docs newer", Host: "b.example", LastAccessed: 900},
	}), 0)
	results := tabsQuery(t, p, "docs")
	require.Equal(t, []string{"Docs newer", "Docs older"},
		[]string{results[0].Title, results[1].Title})

	// Same score AND same lastAccessed: alphabetical by display title.
	p = newTabsProvider(tabsFn([]TabInfo{
		{URL: "https://zeta.example/", Title: "Zeta docs", Host: "zeta.example", LastAccessed: 5},
		{URL: "https://alpha.example/", Title: "Alpha docs", Host: "alpha.example", LastAccessed: 5},
	}), 0)
	results = tabsQuery(t, p, "docs")
	require.Equal(t, []string{"Alpha docs", "Zeta docs"},
		[]string{results[0].Title, results[1].Title})
}

func TestTabsProviderResultShape(t *testing.T) {
	p := newTabsProvider(tabsFn(testTabs()), 0)
	results := tabsQuery(t, p, "hacker")
	require.NotEmpty(t, results)
	r := results[0]
	require.Equal(t, "Hacker News", r.Title)
	require.Equal(t, "https://news.ycombinator.com/", r.Subtitle, "the subtitle is the full URL")
	require.Equal(t, "link", r.Icon, "globe belongs to frequent-sites")
	require.Equal(t, tabPinnedBadge, r.Badge, "pinned tabs carry the badge")
	require.NotNil(t, r.Score)
	require.Equal(t, &Action{Type: ActionOpenURL, Value: "https://news.ycombinator.com/"}, r.Action)

	// Unpinned: no badge. Untitled: the host stands in for the title.
	results = tabsQuery(t, p, "blog.example")
	require.Len(t, results, 1)
	require.Equal(t, "blog.example.org", results[0].Title)
	require.Empty(t, results[0].Badge)
}

func TestTabsProviderCapAndDefaults(t *testing.T) {
	var many []TabInfo
	for i := 0; i < 20; i++ {
		many = append(many, TabInfo{
			URL:          fmt.Sprintf("https://site%02d.example/", i),
			Title:        fmt.Sprintf("Site %02d", i),
			Host:         fmt.Sprintf("site%02d.example", i),
			LastAccessed: int64(100 - i),
		})
	}
	p := newTabsProvider(tabsFn(many), 0)
	require.Equal(t, defaultOpenTabsMax, p.max, "non-positive max takes the default")
	results := tabsQuery(t, p, "site")
	require.Len(t, results, defaultOpenTabsMax)
	require.Equal(t, "Site 00", results[0].Title, "the cap keeps the best-ranked results")

	p = newTabsProvider(tabsFn(many), 3)
	require.Len(t, tabsQuery(t, p, "site"), 3)
}

func TestTabsProviderNoMatchesAndNoSource(t *testing.T) {
	p := newTabsProvider(tabsFn(testTabs()), 0)
	require.Empty(t, tabsQuery(t, p, "zzz-nothing"))

	empty := newTabsProvider(tabsFn(nil), 0)
	require.Empty(t, tabsQuery(t, empty, "git"))

	nilFn := newTabsProvider(nil, 0)
	require.Empty(t, srcResults(t, nilFn, baseRequest("git", "git", 1, nil)),
		"a nil source is inert, never a panic")
}

func TestNewRegistersTabsProviderOnlyWithSource(t *testing.T) {
	// No source: the provider does not exist.
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	require.NotContains(t, r.byID, builtinTabsID)

	// With a source it registers, claims no bangs, and joins the
	// normal fan-out -- alongside the frequent-sites provider when
	// both sources exist.
	r = New(Options{
		FrequentSites: sitesFn(testSites()),
		OpenTabs:      tabsFn(testTabs()),
		Logf:          func(string, ...any) {},
	})
	defer r.Close()
	require.Contains(t, r.byID, builtinTabsID)
	require.Len(t, r.providers, 5, "app + apps + apps-search + firefox-frequent + firefox-tabs")
	require.Empty(t, r.byID[builtinTabsID].(*tabsProvider).bangNames())
	require.Empty(t, r.Errors())
}

func TestNewTabsProviderDisabledEntry(t *testing.T) {
	r := New(Options{
		OpenTabs: tabsFn(testTabs()),
		Entries:  map[string]Entry{builtinTabsID: {Disabled: true}},
		Logf:     func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, builtinTabsID,
		"plugins.entries[firefox-tabs].disabled kills the provider")
}

func TestDispatchTabsEmitsSection(t *testing.T) {
	r := New(Options{
		OpenTabs:    tabsFn(testTabs()),
		OpenTabsMax: 4,
		Logf:        func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "github", 7, nil, emit)
	require.Equal(t, TargetInfo{}, info, "an all-queries builtin never targets")
	e := recvEmission(t, ch)
	require.Equal(t, builtinTabsID, e.Plugin)
	require.Equal(t, "Open Tabs", e.Name)
	require.Equal(t, int64(7), e.Gen)
	require.NotEmpty(t, e.Results)
	require.Equal(t, "Pull requests", e.Results[0].Title)

	// One rune short of the minimum: nothing dispatches.
	r.Dispatch(context.Background(), "g", 8, nil, emit)
	requireNoEmission(t, ch, 100*time.Millisecond)
}

func TestDispatchTabsSourcePanicIsolated(t *testing.T) {
	angry := newTabsProvider(func() []TabInfo { panic("session store exploded") }, 0)
	calm := &fakeProvider{pid: "calm", matchFn: matchAll, queryFn: answer("ok", 0)}
	r, lc := newTestRegistry(t, nil, nil, angry, calm)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "github", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "calm", e.Plugin, "a broken tab source cannot take other providers down")
	require.Eventually(t, func() bool {
		for _, l := range lc.all() {
			if l == "plugin firefox-tabs: panic during dispatch: session store exploded" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
}
