package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

func TestAppsSearchProviderBasics(t *testing.T) {
	p := newAppsSearchProvider(nil, nil)
	require.Equal(t, "apps-search", p.id())
	require.Equal(t, "Apps", p.displayName())
	require.Empty(t, p.bangNames(), "no bangs: the provider is never targetable")
	require.Zero(t, p.debounce())

	stripped, boost, ok := p.match("fire", nil)
	require.True(t, ok, "all_queries: normal queries fan out to it")
	require.Equal(t, "fire", stripped)
	require.Zero(t, boost)

	_, _, ok = p.match("f", nil)
	require.False(t, ok, "the default all_queries minimum of 2 runes gates dispatch")
	_, _, ok = p.match(" f ", nil)
	require.False(t, ok, "the minimum counts the STRIPPED query")

	results := srcResults(t, p, baseRequest("fire", "fire", 1, nil))
	require.Empty(t, results, "nil InstalledApps getter yields nothing")
}

func TestAppsSearchScore(t *testing.T) {
	// The bands are the shared engine's canonical tiers now; this pins
	// their application per app-name shape.
	score := func(app, needle string) (float64, bool) {
		p := newAppsSearchProvider(func() []InstalledApp {
			return []InstalledApp{{Name: app, Exec: "run"}}
		}, nil)
		res := srcResults(t, p, baseRequest(needle, needle, 1, nil))
		if len(res) == 0 {
			return 0, false
		}
		return *res[0].Score, true
	}
	tests := []struct {
		name string
		app  string
		want float64
		ok   bool
	}{
		{name: "exact", app: "Fire", want: 83, ok: true},
		{name: "exact case-insensitive", app: "FIRE", want: 83, ok: true},
		{name: "prefix", app: "Firefox", want: 73, ok: true},
		{name: "word start space", app: "Amazon Fire TV", want: 63, ok: true},
		{name: "word start hyphen", app: "gnome-fire-manager", want: 63, ok: true},
		{name: "word start dot", app: "org.fire.Tool", want: 63, ok: true},
		{name: "substring", app: "Campfire", want: 53, ok: true},
		{name: "no match", app: "GIMP", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := score(tt.app, "fire")
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, tt.want, got)
			}
		})
	}

	multi, ok := score("Visual Studio Code", "visual studio")
	require.True(t, ok)
	require.Equal(t, float64(63), multi,
		"multi-word queries split into terms; the worst term tier (studio = word start) decides")

	fuzzy, ok := score("Firefox", "firefx")
	require.True(t, ok, "the fuzzy tier reaches apps now")
	require.Less(t, fuzzy, float64(53), "strictly below every literal band")
}

func TestAppsSearchRankingOrderAndShape(t *testing.T) {
	list := []InstalledApp{
		{Name: "GIMP", Exec: "gimp %F"},        // no match: excluded
		{Name: "Campfire", Exec: "campfire"},   // substring: weak, cut from the promoted section
		{Name: "Amazon Fire TV", Exec: "aftv"}, // word start 63
		{Name: "Firefox", Exec: "firefox %u"},  // prefix 73
		{Name: "Firebird", Exec: "firebird"},   // prefix 73, before Firefox alphabetically
		{Name: "Fire", Exec: "fire --start"},   // exact 83
		{Name: "Broken Fireplace", Exec: "%f"}, // unlaunchable: dropped
	}
	p := newAppsSearchProvider(func() []InstalledApp { return list }, nil)

	results, best := srcResultsTier(t, p, baseRequest("FiRe", "FiRe", 1, nil))
	require.Equal(t, match.TierExact, best)
	require.Len(t, results, 4,
		"a strong-best section is PROMOTED and keeps only its strong rows: the substring hit is cut, non-matching and unlaunchable apps excluded")

	require.Equal(t, "Fire", results[0].Title)
	require.Equal(t, float64(83), *results[0].Score)
	require.Equal(t, "Firebird", results[1].Title, "equal tiers tie-break alphabetically")
	require.Equal(t, float64(73), *results[1].Score)
	require.Equal(t, "Firefox", results[2].Title)
	require.Equal(t, "Amazon Fire TV", results[3].Title)
	require.Equal(t, float64(63), *results[3].Score)

	require.Equal(t, "fire --start", results[0].Subtitle, "subtitle is the cleaned exec line")
	require.Equal(t, "app", results[0].Icon)
	require.Equal(t, &Action{Type: ActionRunCommand, Argv: []string{"fire", "--start"}}, results[0].Action)
	require.Equal(t, [][2]int{{0, 4}}, results[0].MatchRanges, "engine-minted highlight")
}

func TestAppsSearchCapAndEmptySnapshot(t *testing.T) {
	var list []InstalledApp
	for i := 20; i >= 1; i-- {
		list = append(list, InstalledApp{Name: fmt.Sprintf("Tool %02d", i), Exec: "tool"})
	}
	p := newAppsSearchProvider(func() []InstalledApp { return list }, nil)

	results := srcResults(t, p, baseRequest("tool", "tool", 1, nil))
	require.Len(t, results, maxAppsSearchResults, "untargeted sections cap at 6")
	require.Equal(t, "Tool 01", results[0].Title)

	empty := newAppsSearchProvider(func() []InstalledApp { return nil }, nil)
	results = srcResults(t, empty, baseRequest("tool", "tool", 1, nil))
	require.Empty(t, results, "an empty snapshot emits nothing")
}

func TestAppsSearchThroughDispatch(t *testing.T) {
	r := New(Options{
		InstalledApps: func() []InstalledApp {
			return []InstalledApp{
				{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"},
				{Name: "Files", Exec: "nautilus"},
			}
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "fire", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info, "normal queries are never targeted")
	e := recvEmission(t, ch)
	require.Equal(t, "apps-search", e.Plugin)
	require.Equal(t, "Apps", e.Name)
	require.Equal(t, int64(1), e.Gen)
	require.Equal(t, 1, e.Priority, "the apps section carries source priority 1 (above the file results)")
	require.Len(t, e.Results, 1)
	require.Equal(t, "Firefox", e.Results[0].Title)
	require.Equal(t, float64(73), *e.Results[0].Score,
		"band integrity: the priority is placement metadata and never leaks into the engine score")
	require.Equal(t, &Action{Type: ActionRunCommand, Argv: []string{"firefox"}, DesktopID: "firefox.desktop"},
		e.Results[0].Action, "the desktop id rides along so the app can launch with activation credentials")
	requireNoEmission(t, ch, 100*time.Millisecond)

	info = r.Dispatch(context.Background(), "f", 2, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	requireNoEmission(t, ch, 100*time.Millisecond)
}

func TestAppsSearchExcludedFromTargetedApp(t *testing.T) {
	r := New(Options{
		InstalledApps: func() []InstalledApp {
			return []InstalledApp{{Name: "Firefox", Exec: "firefox %u"}}
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!app fire", 1, nil, emit)
	require.Equal(t, TargetInfo{Targeted: true, Plugin: "apps", Name: "Launch", Bang: "app"}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "apps", e.Plugin, "a resolved bang dispatches ONLY the targeted provider")
	require.Zero(t, e.Priority, "the targeted launcher stays unprioritized: bang queries have no files to outrank")
	requireNoEmission(t, ch, 150*time.Millisecond)
}

func TestSourcePriorityMetadata(t *testing.T) {
	// apps-search is a prioritized source, and its priority is
	// TIER-GATED: promoted only when the emission's best row matched
	// strong (word-start or better); substring/fuzzy bests keep it at
	// 0, below the file results (the macOS "test" field report --
	// scattered-subsequence app hits must not outrank a directory
	// literally named "test"). The two Firefox web sources share the
	// same tier-gated promotion (pinned in
	// builtin_web_priority_test.go); every other builtin -- and every
	// external provider shape -- is 0 whatever the tier.
	require.Equal(t, 1, sourcePriorityApps)
	require.Equal(t, match.TierWordStart, strongTier, "the promotion line is word-start or better")
	apps := newAppsSearchProvider(nil, nil)
	for _, tier := range []match.Tier{match.TierTriggered, match.TierExact, match.TierPrefix, match.TierWordStart} {
		require.Equal(t, sourcePriorityApps, providerPriority(apps, tier), "strong tier %d promotes", tier)
	}
	for _, tier := range []match.Tier{match.TierSubstring, match.TierFuzzy, match.TierNone} {
		require.Zero(t, providerPriority(apps, tier), "weak tier %d stays below the files", tier)
	}
	for _, tier := range []match.Tier{match.TierExact, match.TierFuzzy} {
		require.Zero(t, providerPriority(newAppsProvider(nil, nil), tier), "the targeted !app launcher")
		require.Zero(t, providerPriority(newAppCommandProvider("v1"), tier))
		require.Zero(t, providerPriority(newWindowsProvider(func() []WindowInfo { return nil }), tier))
	}

	// External plugins can NEVER be prioritized: the wire Response has
	// no priority field and *externalProvider does not implement the
	// extension, so the registry always stamps 0 for them.
	_, isPrioritized := any(&externalProvider{}).(prioritized)
	require.False(t, isPrioritized, "externalProvider must never satisfy prioritized")
	require.Zero(t, providerPriority(&externalProvider{m: &Manifest{ID: "x"}}, match.TierExact))
	require.Zero(t, providerPriority(&fakeProvider{pid: "fake"}, match.TierExact))
}

func TestExternalEmissionPriorityAlwaysZero(t *testing.T) {
	// A dispatched external-shaped provider emits Priority 0 no matter
	// what its response contains -- the field is registry-stamped.
	ext := &fakeProvider{pid: "ext", name: "Ext", matchFn: matchAll,
		queryFn: answer("hit", 0)}
	r, _ := newTestRegistry(t, nil, nil, ext)
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "hit", 7, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "ext", e.Plugin)
	require.Zero(t, e.Priority)
}

func TestCheatSheetEmissionPriorityZero(t *testing.T) {
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	e := r.CheatSheet()
	require.NotEmpty(t, e.Results, "the builtin commands populate the sheet")
	require.Zero(t, e.Priority, "the cheat sheet renders in the classic below-files zone")
}

func TestPriorityNeverChangesMintedScores(t *testing.T) {
	// The engine mint through the registry dispatch must be
	// byte-identical to the direct sourceResults mint: the priority is
	// carried NEXT TO the results, never inside them. (Both sides run
	// the same strong-rows-only cut for a promoted section -- the cut
	// drops whole rows, it never rewrites a minted score.)
	installed := func() []InstalledApp {
		return []InstalledApp{
			{Name: "Fire", Exec: "fire"},
			{Name: "Firefox", Exec: "firefox %u"},
			{Name: "Campfire", Exec: "campfire"},
		}
	}
	direct := srcResults(t, newAppsSearchProvider(installed, nil), baseRequest("fire", "fire", 1, nil))
	require.Len(t, direct, 2, "the promoted section cuts the weak substring row")

	r := New(Options{InstalledApps: installed, Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "fire", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, 1, e.Priority)
	require.Equal(t, direct, e.Results, "dispatch emissions carry the exact engine mint")
}

func TestAppsSearchDisabledPerEntry(t *testing.T) {
	r := New(Options{
		InstalledApps: func() []InstalledApp {
			return []InstalledApp{{Name: "Firefox", Exec: "firefox %u"}}
		},
		Entries: map[string]Entry{builtinAppsSearchID: {Disabled: true}},
		Logf:    func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, builtinAppsSearchID)
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "fire", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	requireNoEmission(t, ch, 150*time.Millisecond)

	info = r.Dispatch(context.Background(), "!app fire", 2, nil, emit)
	require.True(t, info.Targeted)
	e := recvEmission(t, ch)
	require.Equal(t, "apps", e.Plugin, "the targeted launcher is independent of the apps-search entry")
	require.Len(t, e.Results, 1)
}

func TestAppsSearchSurvivesFailingSibling(t *testing.T) {
	angry := &fakeProvider{pid: "angry", matchFn: matchAll, queryFn: func(context.Context, Request) ([]Result, []string, error) {
		panic("plugin bug")
	}}
	apps := newAppsSearchProvider(func() []InstalledApp {
		return []InstalledApp{{Name: "Firefox", Exec: "firefox %u"}}
	}, nil)
	r, lc := newTestRegistry(t, nil, nil, angry, apps)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "fire", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "apps-search", e.Plugin, "a panicking sibling never blocks the apps section")
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "panic during dispatch")
	}, time.Second, 10*time.Millisecond)
}

// TestAppsSearchWeakMatchesStayBelowFiles is the macOS field report
// pinned at dispatch level: query "test" fuzzy-lands on installed
// apps like "Keynote Creator Studio" and "Little Snitch"
// (scattered-subsequence hits), and such sections must NOT ride the
// above-files zone over a directory literally named "test". Weak
// (substring/fuzzy) best rows emit at priority 0 -- rendered below
// the file results, all rows kept -- while a strong best row
// (word-start or better) still earns the promotion.
func TestAppsSearchWeakMatchesStayBelowFiles(t *testing.T) {
	dispatch := func(query string, list []InstalledApp) Emission {
		t.Helper()
		r := New(Options{
			InstalledApps: func() []InstalledApp { return list },
			Logf:          func(string, ...any) {},
		})
		defer r.Close()
		emit, ch := collectEmissions()
		r.Dispatch(context.Background(), query, 1, nil, emit)
		return recvEmission(t, ch)
	}

	// The report's shape: fuzzy-only matches, priority 0, every row
	// still delivered (the section renders, just below the files).
	e := dispatch("test", []InstalledApp{
		{Name: "Keynote Creator Studio", Exec: "keynote"},
		{Name: "Little Snitch", Exec: "snitch"},
	})
	require.Equal(t, "apps-search", e.Plugin)
	require.Zero(t, e.Priority, "fuzzy-only app matches render below the file results")
	require.Len(t, e.Results, 2, "a weak-only section keeps all its rows")
	require.Less(t, *e.Results[0].Score, float64(53), "fuzzy band: strictly below every literal band")

	// Substring-tier bests are weak too.
	e = dispatch("fire", []InstalledApp{{Name: "Campfire", Exec: "campfire"}})
	require.Zero(t, e.Priority, "substring-tier app matches render below the file results")

	// Strong bests keep the promotion: prefix...
	e = dispatch("fire", []InstalledApp{{Name: "Firefox", Exec: "firefox %u"}})
	require.Equal(t, 1, e.Priority, "prefix matches promote the section")

	// ... and word-start.
	e = dispatch("fire", []InstalledApp{{Name: "Amazon Fire TV", Exec: "aftv"}})
	require.Equal(t, 1, e.Priority, "word-start matches promote the section")
}

// TestAppsSearchPromotedSectionStrongRowsOnly: when the section is
// promoted, sub-word-start rows are cut -- a weak row must never ride
// the above-files zone -- while the SAME weak rows render whenever no
// strong match exists (whole section at priority 0).
func TestAppsSearchPromotedSectionStrongRowsOnly(t *testing.T) {
	list := []InstalledApp{
		{Name: "Firefox", Exec: "firefox %u"}, // prefix: strong
		{Name: "Campfire", Exec: "campfire"},  // substring: weak
	}
	r := New(Options{
		InstalledApps: func() []InstalledApp { return list },
		Logf:          func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "fire", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, 1, e.Priority)
	require.Len(t, e.Results, 1, "the promoted section carries only its strong rows")
	require.Equal(t, "Firefox", e.Results[0].Title)

	// Drop the strong app: the weak row is back, below the files.
	r2 := New(Options{
		InstalledApps: func() []InstalledApp { return list[1:] },
		Logf:          func(string, ...any) {},
	})
	defer r2.Close()
	emit2, ch2 := collectEmissions()
	r2.Dispatch(context.Background(), "fire", 2, nil, emit2)
	e = recvEmission(t, ch2)
	require.Zero(t, e.Priority)
	require.Len(t, e.Results, 1)
	require.Equal(t, "Campfire", e.Results[0].Title)
}
