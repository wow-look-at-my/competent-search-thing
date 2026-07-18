package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testSites is a fixture frequent-sites snapshot.
func testSites() []SiteInfo {
	return []SiteInfo{
		{URL: "https://github.com/pulls", Title: "Pull requests", Host: "github.com", Visits: 40},
		{URL: "https://www.google.com/", Title: "Google", Host: "www.google.com", Visits: 100},
		{URL: "https://news.ycombinator.com/", Title: "Hacker News", Host: "news.ycombinator.com", Visits: 30},
		{URL: "https://go.dev/doc/", Title: "Get started with Go", Host: "go.dev", Visits: 20},
		{URL: "https://blog.example.org/", Title: "", Host: "blog.example.org", Visits: 15},
	}
}

func sitesFn(sites []SiteInfo) func() []SiteInfo {
	return func() []SiteInfo { return sites }
}

// firefoxQuery drives one provider query the way dispatch would.
func firefoxQuery(t *testing.T, p *firefoxProvider, q string) []Result {
	t.Helper()
	stripped, boost, ok := p.match(q, nil)
	if !ok {
		return nil
	}
	require.Zero(t, boost)
	return srcResults(t, p, baseRequest(q, stripped, 1, nil))
}

func TestFirefoxProviderMatchGatesShortQueries(t *testing.T) {
	p := newFirefoxProvider(sitesFn(nil), 0)
	for _, q := range []string{"", " ", "g", " g "} {
		_, _, ok := p.match(q, nil)
		require.False(t, ok, "query %q is under the 2-rune minimum", q)
	}
	stripped, boost, ok := p.match("  git  ", nil)
	require.True(t, ok)
	require.Equal(t, "git", stripped, "the stripped query is trimmed")
	require.Zero(t, boost)
	_, _, ok = p.match("ab", nil)
	require.True(t, ok, "two runes is enough")
}

func TestFirefoxProviderRanking(t *testing.T) {
	p := newFirefoxProvider(sitesFn(testSites()), 0)
	tests := []struct {
		query string
		want  string  // first result title
		score float64 // its score
	}{
		{query: "git", want: "Pull requests", score: 73},        // host prefix
		{query: "GITHUB.COM", want: "Pull requests", score: 83}, // host EXACT under the unified ladder
		{query: "google", want: "Google", score: 83},            // title EXACT beats the host prefix
		{query: "news", want: "Hacker News", score: 73},         // host prefix
		{query: "hacker", want: "Hacker News", score: 73},       // title prefix
		{query: "started", want: "Get started with Go", score: 63},
		{query: "ycombinator", want: "Hacker News", score: 63},    // host segment = word start (after '.')
		{query: "tarted", want: "Get started with Go", score: 53}, // mid-word title hit
		{query: "pulls", want: "Pull requests", score: 63},        // URL path segment = word start (after '/')
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			results := firefoxQuery(t, p, tt.query)
			require.NotEmpty(t, results)
			require.Equal(t, tt.want, results[0].Title)
			require.Equal(t, tt.score, *results[0].Score)
		})
	}
}

func TestFirefoxProviderTiebreaks(t *testing.T) {
	p := newFirefoxProvider(sitesFn(testSites()), 0)
	// "go" host-prefixes both google.com (100 visits) and go.dev (20):
	// equal scores fall back to the visit count.
	results := firefoxQuery(t, p, "go")
	require.GreaterOrEqual(t, len(results), 2)
	require.Equal(t, "Google", results[0].Title)
	require.Equal(t, "Get started with Go", results[1].Title)

	// Same score AND same visits: alphabetical by display title.
	p = newFirefoxProvider(sitesFn([]SiteInfo{
		{URL: "https://zeta.example/", Title: "Zeta docs", Host: "zeta.example", Visits: 12},
		{URL: "https://alpha.example/", Title: "Alpha docs", Host: "alpha.example", Visits: 12},
	}), 0)
	results = firefoxQuery(t, p, "docs")
	require.Equal(t, []string{"Alpha docs", "Zeta docs"}, []string{results[0].Title, results[1].Title})
}

func TestFirefoxProviderResultShape(t *testing.T) {
	p := newFirefoxProvider(sitesFn(testSites()), 0)
	results := firefoxQuery(t, p, "blog.example")
	require.Len(t, results, 1)
	r := results[0]
	require.Equal(t, "blog.example.org", r.Title, "an untitled page falls back to its host")
	require.Equal(t, "https://blog.example.org/", r.Subtitle, "the subtitle is the full URL")
	require.Equal(t, "globe", r.Icon)
	require.NotNil(t, r.Score)
	require.GreaterOrEqual(t, *r.Score, float64(0))
	require.LessOrEqual(t, *r.Score, float64(100))
	require.Equal(t, &Action{Type: ActionOpenURL, Value: "https://blog.example.org/"}, r.Action)
}

func TestFirefoxProviderCapAndDefaults(t *testing.T) {
	var many []SiteInfo
	for i := 0; i < 20; i++ {
		many = append(many, SiteInfo{
			URL:    fmt.Sprintf("https://site%02d.example/", i),
			Title:  fmt.Sprintf("Site %02d", i),
			Host:   fmt.Sprintf("site%02d.example", i),
			Visits: 100 - i,
		})
	}
	p := newFirefoxProvider(sitesFn(many), 0)
	require.Equal(t, defaultFrequentSitesMax, p.max, "non-positive max takes the default")
	results := firefoxQuery(t, p, "site")
	require.Len(t, results, defaultFrequentSitesMax)
	require.Equal(t, "Site 00", results[0].Title, "the cap keeps the best-ranked results")

	p = newFirefoxProvider(sitesFn(many), 3)
	require.Len(t, firefoxQuery(t, p, "site"), 3)
}

func TestFirefoxProviderNoMatchesAndNoSource(t *testing.T) {
	p := newFirefoxProvider(sitesFn(testSites()), 0)
	require.Empty(t, firefoxQuery(t, p, "zzz-nothing"))

	empty := newFirefoxProvider(sitesFn(nil), 0)
	require.Empty(t, firefoxQuery(t, empty, "git"))

	nilFn := newFirefoxProvider(nil, 0)
	require.Empty(t, srcResults(t, nilFn, baseRequest("git", "git", 1, nil)),
		"a nil source is inert, never a panic")
}

func TestNewRegistersFirefoxProviderOnlyWithSource(t *testing.T) {
	// No source: the provider does not exist (default Options keep the
	// historical builtin set, which other tests count on).
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	require.NotContains(t, r.byID, builtinFirefoxID)

	// With a source it registers, claims no bangs, and joins the
	// normal fan-out.
	r = New(Options{FrequentSites: sitesFn(testSites()), Logf: func(string, ...any) {}})
	defer r.Close()
	require.Contains(t, r.byID, builtinFirefoxID)
	require.Len(t, r.providers, 4, "app + apps + apps-search + firefox-frequent")
	require.Empty(t, r.byID[builtinFirefoxID].(*firefoxProvider).bangNames())
	require.Empty(t, r.Errors())
}

func TestNewFirefoxProviderDisabledEntry(t *testing.T) {
	r := New(Options{
		FrequentSites: sitesFn(testSites()),
		Entries:       map[string]Entry{builtinFirefoxID: {Disabled: true}},
		Logf:          func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, builtinFirefoxID,
		"plugins.entries[firefox-frequent].disabled kills the provider")
}

func TestDispatchFirefoxEmitsSection(t *testing.T) {
	r := New(Options{
		FrequentSites:    sitesFn(testSites()),
		FrequentSitesMax: 4,
		Logf:             func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "github", 7, nil, emit)
	require.Equal(t, TargetInfo{}, info, "an all-queries builtin never targets")
	e := recvEmission(t, ch)
	require.Equal(t, builtinFirefoxID, e.Plugin)
	require.Equal(t, "Frequent Sites", e.Name)
	require.Equal(t, int64(7), e.Gen)
	require.NotEmpty(t, e.Results)
	require.Equal(t, "Pull requests", e.Results[0].Title)

	// One rune short of the minimum: nothing dispatches.
	r.Dispatch(context.Background(), "g", 8, nil, emit)
	requireNoEmission(t, ch, 100*time.Millisecond)
}

func TestDispatchFirefoxSourcePanicIsolated(t *testing.T) {
	angry := newFirefoxProvider(func() []SiteInfo { panic("history exploded") }, 0)
	calm := &fakeProvider{pid: "calm", matchFn: matchAll, queryFn: answer("ok", 0)}
	r, lc := newTestRegistry(t, nil, nil, angry, calm)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "github", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "calm", e.Plugin, "a broken history source cannot take other providers down")
	require.Eventually(t, func() bool {
		for _, l := range lc.all() {
			if l == "plugin firefox-frequent: panic during dispatch: history exploded" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
}
