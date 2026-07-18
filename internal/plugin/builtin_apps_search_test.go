package plugin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppsSearchProviderBasics(t *testing.T) {
	p := newAppsSearchProvider(nil)
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

	results, _, err := p.query(context.Background(), baseRequest("fire", "fire", 1, nil))
	require.NoError(t, err)
	require.Empty(t, results, "nil InstalledApps getter yields nothing")
}

func TestAppsSearchScore(t *testing.T) {
	score := appsSearchScore("fire")
	tests := []struct {
		name string
		app  string
		want float64
		ok   bool
	}{
		{name: "exact", app: "Fire", want: 100, ok: true},
		{name: "exact case-insensitive", app: "FIRE", want: 100, ok: true},
		{name: "prefix", app: "Firefox", want: 90, ok: true},
		{name: "word start space", app: "Amazon Fire TV", want: 75, ok: true},
		{name: "word start hyphen", app: "gnome-fire-manager", want: 75, ok: true},
		{name: "word start dot", app: "org.fire.Tool", want: 75, ok: true},
		{name: "substring", app: "Campfire", want: 60, ok: true},
		{name: "no match", app: "GIMP", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := score(tt.app)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, tt.want, got)
			}
		})
	}

	multi, ok := appsSearchScore("visual studio")("Visual Studio Code")
	require.True(t, ok)
	require.Equal(t, float64(90), multi, "multi-word needles still rank prefix/substring")
}

func TestAppsSearchRankingOrderAndShape(t *testing.T) {
	list := []InstalledApp{
		{Name: "GIMP", Exec: "gimp %F"},        // no match: excluded
		{Name: "Campfire", Exec: "campfire"},   // substring 60
		{Name: "Amazon Fire TV", Exec: "aftv"}, // word start 75
		{Name: "Firefox", Exec: "firefox %u"},  // prefix 90
		{Name: "Firebird", Exec: "firebird"},   // prefix 90, before Firefox alphabetically
		{Name: "Fire", Exec: "fire --start"},   // exact 100
		{Name: "Broken Fireplace", Exec: "%f"}, // unlaunchable: dropped
	}
	p := newAppsSearchProvider(func() []InstalledApp { return list })

	results, _, err := p.query(context.Background(), baseRequest("FiRe", "FiRe", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, 5, "case-insensitive; non-matching and unlaunchable apps excluded")

	require.Equal(t, "Fire", results[0].Title)
	require.Equal(t, float64(100), *results[0].Score)
	require.Equal(t, "Firebird", results[1].Title, "equal scores tie-break alphabetically")
	require.Equal(t, float64(90), *results[1].Score)
	require.Equal(t, "Firefox", results[2].Title)
	require.Equal(t, "Amazon Fire TV", results[3].Title)
	require.Equal(t, float64(75), *results[3].Score)
	require.Equal(t, "Campfire", results[4].Title)
	require.Equal(t, float64(60), *results[4].Score)

	require.Equal(t, "fire --start", results[0].Subtitle, "subtitle is the cleaned exec line")
	require.Equal(t, "app", results[0].Icon)
	require.Equal(t, &Action{Type: ActionRunCommand, Argv: []string{"fire", "--start"}}, results[0].Action)
}

func TestAppsSearchCapAndEmptySnapshot(t *testing.T) {
	var list []InstalledApp
	for i := 20; i >= 1; i-- {
		list = append(list, InstalledApp{Name: fmt.Sprintf("Tool %02d", i), Exec: "tool"})
	}
	p := newAppsSearchProvider(func() []InstalledApp { return list })

	results, _, err := p.query(context.Background(), baseRequest("tool", "tool", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, maxAppsSearchResults, "untargeted sections cap at 6")
	require.Equal(t, "Tool 01", results[0].Title)

	empty := newAppsSearchProvider(func() []InstalledApp { return nil })
	results, _, err = empty.query(context.Background(), baseRequest("tool", "tool", 1, nil))
	require.NoError(t, err)
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
	require.Len(t, e.Results, 1)
	require.Equal(t, "Firefox", e.Results[0].Title)
	require.Equal(t, float64(90), *e.Results[0].Score)
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
	requireNoEmission(t, ch, 150*time.Millisecond)
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
	})
	r, lc := newTestRegistry(t, nil, nil, angry, apps)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "fire", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "apps-search", e.Plugin, "a panicking sibling never blocks the apps section")
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "panic during dispatch")
	}, time.Second, 10*time.Millisecond)
}
