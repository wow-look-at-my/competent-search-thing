package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// srcResults runs one candidate source through the engine exactly like
// the registry's dispatch does (fuzzy tier on) -- the test-side entry
// into the single mint path.
func srcResults(t *testing.T, src candidateSource, req Request) []Result {
	t.Helper()
	res, _, err := sourceResults(src, context.Background(), req, false)
	require.NoError(t, err)
	return res
}

// srcResultsTier is srcResults plus the emission's strongest minted
// tier -- the per-emission section-priority input.
func srcResultsTier(t *testing.T, src candidateSource, req Request) ([]Result, match.Tier) {
	t.Helper()
	res, best, err := sourceResults(src, context.Background(), req, false)
	require.NoError(t, err)
	return res, best
}

// TestEveryRegisteredSourceRoutesThroughEngine is the inversion guard:
// every provider a production registry registers is either a builtin
// candidate source (raw candidates, engine-minted) or an external
// plugin (sanitized then engine-passed). Nothing else can register, so
// nothing can hand the UI a score or highlight the engine did not
// mint.
func TestEveryRegisteredSourceRoutesThroughEngine(t *testing.T) {
	m := &Manifest{ID: "ext", Name: "Ext", Type: TypeCommand,
		Command: &CommandSpec{Argv: []string{"/bin/true"}}, Bangs: []string{"ext"}}
	r := New(Options{
		Manifests:     []*Manifest{m},
		Version:       "test",
		InstalledApps: func() []InstalledApp { return nil },
		OpenWindows:   func() []WindowInfo { return nil },
		FrequentSites: func() []SiteInfo { return nil },
		OpenTabs:      func() []TabInfo { return nil },
		Rewrites:      []RewriteRule{{Name: "jira", Pattern: `[A-Z]+-\d+`, Replacement: "https://jira.example/$0"}},
		Logf:          func(string, ...any) {},
	})
	defer r.Close()

	require.NotEmpty(t, r.providers)
	seen := map[string]string{}
	for _, p := range r.providers {
		switch p.(type) {
		case candidateSource:
			seen[p.id()] = "source"
		case *externalProvider:
			seen[p.id()] = "external"
		default:
			t.Fatalf("provider %q is neither a candidate source nor an external plugin: it bypasses the engine", p.id())
		}
	}
	for _, id := range []string{builtinAppID, builtinAppsID, builtinAppsSearchID,
		builtinWindowsID, builtinFirefoxID, builtinTabsID, builtinRewritesID} {
		require.Equal(t, "source", seen[id], "builtin %q must be a candidate source", id)
	}
	require.Equal(t, "external", seen["ext"])
	_, ok := r.suggest.(candidateSource)
	require.True(t, ok, "the special-cased suggestions provider is a candidate source too")
}

// TestEngineFireFoxRepro is the user's exact bug pinned at the
// provider level: an installed-apps snapshot containing "Firefox" must
// surface for the query "fire fox" (and "fox fire", and fuzzily for
// "firefx"), while "firefox" and "fire" behave as before.
func TestEngineFireFoxRepro(t *testing.T) {
	installed := func() []InstalledApp {
		return []InstalledApp{
			{Name: "Firefox", Exec: "firefox %u"},
			{Name: "Files", Exec: "nautilus"},
			{Name: "GIMP", Exec: "gimp %F"},
		}
	}
	p := newAppsSearchProvider(installed, nil)

	for _, q := range []string{"fire fox", "fox fire", "firefx", "FIRE FOX"} {
		results := srcResults(t, p, baseRequest(q, q, 1, nil))
		require.Len(t, results, 1, "query %q", q)
		require.Equal(t, "Firefox", results[0].Title, "query %q", q)
		require.NotEmpty(t, results[0].MatchRanges, "query %q must highlight", q)
	}

	results := srcResults(t, p, baseRequest("fire", "fire", 1, nil))
	require.Equal(t, "Firefox", results[0].Title)
	require.Equal(t, [][2]int{{0, 4}}, results[0].MatchRanges)

	// Through the full dispatch pipeline too.
	r := New(Options{InstalledApps: installed, Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()
	info := r.Dispatch(context.Background(), "fire fox", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "apps-search", e.Plugin)
	require.Len(t, e.Results, 1)
	require.Equal(t, "Firefox", e.Results[0].Title)
	require.Equal(t, [][2]int{{0, 7}}, e.Results[0].MatchRanges,
		`"fire" and "fox" together light up the whole name`)
}

// TestEngineFuzzyDisabledGovernsSources: config search.fuzzyDisabled
// reaches every candidate source through the registry; term splitting
// still applies.
func TestEngineFuzzyDisabledGovernsSources(t *testing.T) {
	installed := func() []InstalledApp {
		return []InstalledApp{{Name: "Firefox", Exec: "firefox %u"}}
	}
	r := New(Options{InstalledApps: installed, FuzzyDisabled: true, Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "firefx", 1, nil, emit)
	requireNoEmission(t, ch, 150*time.Millisecond)

	r.Dispatch(context.Background(), "fire fox", 2, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "Firefox", e.Results[0].Title,
		"multi-term substring conjunction works with fuzzy off")
}

// TestEngineMintOverwritesSourceScores: whatever a source smuggles
// into its payload's Score is clobbered by the engine's band.
func TestEngineMintOverwritesSourceScores(t *testing.T) {
	rogue := 99.0
	ranked := match.Rank([]match.Candidate{{
		Display: "Firefox",
		Texts:   []string{"Firefox"},
		Payload: Result{Title: "Firefox", Score: &rogue},
	}}, match.RankOptions{Terms: match.Terms("fire")})
	out := mintResults(ranked, false)
	require.Len(t, out, 1)
	require.Equal(t, 73.0, *out[0].Score, "the engine's prefix band, not the smuggled 99")

	// Non-Result payloads are dropped, never emitted.
	ranked = match.Rank([]match.Candidate{{Display: "x", Texts: []string{"x"}, Payload: 42}},
		match.RankOptions{Terms: match.Terms("x")})
	require.Empty(t, mintResults(ranked, false))
}

// TestRankExternalTextGating: an all-queries plugin's results must
// text-match the query via title or keywords; misses are dropped with
// a logged reason; self-scores demote to intra-tier hints.
func TestRankExternalTextGating(t *testing.T) {
	score := func(v float64) *float64 { return &v }
	results := []Result{
		{Title: "Weather in Paris", Score: score(90)},
		{Title: "Totally unrelated", Score: score(100)},
		{Title: "Fancy pun", Keywords: []string{"paris", "weather"}, Score: score(10)},
	}
	req := baseRequest("paris", "paris", 1, nil)
	out, dropped := rankExternal(results, req, false, false)
	require.Len(t, out, 2)
	require.Equal(t, "Fancy pun", out[0].Title,
		"an EXACT keyword hit outranks a title word-start (tier dominates field)")
	require.Equal(t, "Weather in Paris", out[1].Title, "keywords make unrelated titles findable")
	require.Empty(t, out[0].MatchRanges, "no title positions when only keywords matched")
	require.NotEmpty(t, out[1].MatchRanges)
	require.Len(t, dropped, 1)
	require.Contains(t, dropped[0], "matched no query term")

	// Engine scores replace self-scores (bands, not 90/100/10).
	require.Less(t, *out[0].Score, 86.0)
	require.GreaterOrEqual(t, *out[1].Score, 14.0)
}

// TestRankExternalClaimed: a plugin that claimed the query (prefix/
// regex trigger or bang) enters the triggered tier: no text gating,
// hint-ordered, above every text band; plugin-supplied matchRanges
// survive.
func TestRankExternalClaimed(t *testing.T) {
	score := func(v float64) *float64 { return &v }
	results := []Result{
		{Title: "42", Score: score(80)},
		{Title: "0x2A", Score: score(30), MatchRanges: [][2]int{{0, 2}}},
	}
	req := baseRequest("=6*7", "6*7", 1, nil)
	out, dropped := rankExternal(results, req, true, false)
	require.Empty(t, dropped)
	require.Len(t, out, 2, "never text-gated: '42' does not contain '6*7'")
	require.Equal(t, "42", out[0].Title)
	require.GreaterOrEqual(t, *out[0].Score, 86.0)
	require.Nil(t, out[0].MatchRanges)
	require.Equal(t, [][2]int{{0, 2}}, out[1].MatchRanges, "plugin-supplied ranges survive")
}

// TestDispatchClaimedTrigger drives the triggered tier end to end: a
// prefix-trigger fake external whose results would never text-match.
func TestDispatchClaimedTrigger(t *testing.T) {
	dir := t.TempDir()
	writeCalcManifest(t, dir)
	manifests, errs := LoadDir(dir)
	require.Empty(t, errs)
	r := New(Options{Manifests: manifests, Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "=6*7", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "fakecalc", e.Plugin)
	require.Len(t, e.Results, 1)
	require.Equal(t, "42", e.Results[0].Title)
	require.GreaterOrEqual(t, *e.Results[0].Score, 86.0,
		"claimed-plugin results ride the triggered tier")
}

// writeCalcManifest drops a tiny prefix-triggered command plugin that
// always answers "42" (a /bin/sh echo).
func writeCalcManifest(t *testing.T, dir string) {
	t.Helper()
	writePluginDir(t, dir, "fakecalc", fmt.Sprintf(`{
		"id": "fakecalc", "name": "Calc", "type": "command",
		"command": {"argv": ["/bin/sh", "-c", %q]},
		"trigger": {"prefix": "="}
	}`, `echo '{"v":1,"results":[{"title":"42"}]}'`))
}
