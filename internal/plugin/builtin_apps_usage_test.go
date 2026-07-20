package plugin

// The app-usage tie-break and usage-key tests for the two builtin app
// sources, split from builtin_apps_search_test.go (which keeps the
// provider basics, scoring, and source-priority tests).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAppsSearchUsageTieBreak is the second half of the macOS field
// report: query "code" matched four apps all carrying "Code" as a
// word (equal word-start tier), the tie fell to the alphabet, and an
// obscure helper beat the app the user launches daily. Equal-class
// rows must order by recorded usage (decayed launch counts, higher
// first), then name -- and usage must NEVER lift a row across match
// tiers.
func TestAppsSearchUsageTieBreak(t *testing.T) {
	list := []InstalledApp{
		{Name: "Claude Code URL Handler", Exec: "claude-url"},
		{Name: "Time Code Studio", Exec: "timecode"},
		{Name: "Visual Studio Code", Exec: "code %F", ID: "code.desktop"},
		{Name: "Zed Code Preview", Exec: "zed-preview"},
	}
	usage := func(key string) float64 {
		switch key {
		case "app:code.desktop": // a desktop-id key (linux shape)
			return 41.5
		case "app:timecode": // an argv key (id-less shape)
			return 3.25
		}
		return 0
	}

	p := newAppsSearchProvider(func() []InstalledApp { return list }, usage)
	results := srcResults(t, p, baseRequest("code", "code", 1, nil))
	require.Len(t, results, 4, `all four carry "code" at a word start`)
	require.Equal(t, "Visual Studio Code", results[0].Title, "the daily app wins its class")
	require.Equal(t, "Time Code Studio", results[1].Title, "then the occasionally used one")
	require.Equal(t, "Claude Code URL Handler", results[2].Title, "never-used apps fall back to name order")
	require.Equal(t, "Zed Code Preview", results[3].Title)
	for _, r := range results {
		require.Equal(t, float64(63), *r.Score, "usage is a tie-break, never a score change")
	}

	// Cold start (all-zero usage, or no usage seam at all): honest
	// name order, exactly the old default.
	cold := srcResults(t, newAppsSearchProvider(func() []InstalledApp { return list }, func(string) float64 { return 0 }),
		baseRequest("code", "code", 1, nil))
	require.Equal(t, "Claude Code URL Handler", cold[0].Title)
	require.Equal(t, "Time Code Studio", cold[1].Title)
	require.Equal(t, "Visual Studio Code", cold[2].Title)
}

// TestAppsUsageNeverCrossesTiers: a heavily launched weak match still
// ranks below an untouched stronger match -- the tier stays the
// primary sort key. Pinned on the targeted launcher (both apps stay
// visible there; the untargeted section would cut the weak row when
// promoted).
func TestAppsUsageNeverCrossesTiers(t *testing.T) {
	list := []InstalledApp{
		{Name: "Sunfire", Exec: "sunfire"},     // substring, launched constantly
		{Name: "Fireplace", Exec: "fireplace"}, // prefix, never launched
	}
	usage := func(key string) float64 {
		if key == "app:sunfire" {
			return 999
		}
		return 0
	}
	p := newAppsProvider(func() []InstalledApp { return list }, usage)
	results := srcResults(t, p, targetedReq("app", "fire"))
	require.Len(t, results, 2)
	require.Equal(t, "Fireplace", results[0].Title, "prefix beats substring whatever the usage")
	require.Equal(t, "Sunfire", results[1].Title)
}

// TestAppsProviderEmptyQueryUsageFirst: the !app / !launch browse
// list (empty rest lists everything at the flat listed score) orders
// by usage first, then name -- the apps you actually launch surface
// on top of the browse list.
func TestAppsProviderEmptyQueryUsageFirst(t *testing.T) {
	list := []InstalledApp{
		{Name: "Alpha", Exec: "alpha"},
		{Name: "Beta", Exec: "beta"},
		{Name: "Gamma", Exec: "gamma"},
	}
	usage := func(key string) float64 {
		if key == "app:gamma" {
			return 7
		}
		return 0
	}
	p := newAppsProvider(func() []InstalledApp { return list }, usage)
	results := srcResults(t, p, targetedReq("app", ""))
	require.Equal(t, "Gamma", results[0].Title, "used apps first")
	require.Equal(t, "Alpha", results[1].Title, "then name order")
	require.Equal(t, "Beta", results[2].Title)
}

// TestAppsSearchUsageThroughDispatch pins the Options.AppUsage plumb:
// the registry hands the seam to both app sources.
func TestAppsSearchUsageThroughDispatch(t *testing.T) {
	list := []InstalledApp{
		{Name: "Ant Code", Exec: "antcode"},
		{Name: "Zebra Code", Exec: "zebracode"},
	}
	r := New(Options{
		InstalledApps: func() []InstalledApp { return list },
		AppUsage: func(key string) float64 {
			if key == "app:zebracode" {
				return 12
			}
			return 0
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "code", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "apps-search", e.Plugin)
	require.Equal(t, "Zebra Code", e.Results[0].Title, "usage rides Options.AppUsage into the fan-out")
	require.Equal(t, "Ant Code", e.Results[1].Title)
}

// TestAppUsageKeyShapes pins the stable key contract: the desktop id
// when one is stamped (linux), else the parsed argv joined with
// single spaces (darwin's `open -a "<bundle>"` and id-less .desktop
// entries) -- computable identically from the installed-app snapshot
// and from the echoed run_command action.
func TestAppUsageKeyShapes(t *testing.T) {
	require.Equal(t, "app:firefox.desktop", AppUsageKey("firefox.desktop", []string{"firefox"}))
	require.Equal(t, "app:open -a /Applications/Safari.app",
		AppUsageKey("", []string{"open", "-a", "/Applications/Safari.app"}),
		"the darwin bundle-launch shape keys on the argv")
	require.Equal(t, "app:vi", AppUsageKey("", []string{"vi"}))
	require.Empty(t, AppUsageKey("", nil), "nothing derivable, nothing recorded")

	// Candidate build and action echo derive the SAME key on both
	// platform shapes.
	for _, app := range []InstalledApp{
		{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"},
		{Name: "Safari", Exec: `open -a "/Applications/Safari.app"`, ID: "Safari.app"},
	} {
		cands := appCandidates([]InstalledApp{app}, nil)
		require.Len(t, cands, 1)
		res, ok := cands[0].Payload.(Result)
		require.True(t, ok)
		argv := parseDesktopExec(app.Exec)
		lookupID := ""
		if app.ID == "firefox.desktop" {
			lookupID = app.ID
		}
		require.Equal(t, AppUsageKey(lookupID, argv), AppPickKey(builtinAppsSearchID, res.Action),
			"app %s: record and lookup must agree", app.Name)
	}
}

// TestAppPickKey: only run_command launches from the two builtin app
// sources record usage; external plugins' run_commands and every
// other action shape yield no key.
func TestAppPickKey(t *testing.T) {
	act := &Action{Type: ActionRunCommand, Argv: []string{"firefox"}, DesktopID: "firefox.desktop"}
	require.Equal(t, "app:firefox.desktop", AppPickKey(builtinAppsID, act))
	require.Equal(t, "app:firefox.desktop", AppPickKey(builtinAppsSearchID, act))
	require.Empty(t, AppPickKey("some-external-plugin", act), "external run_commands are not app launches")
	require.Empty(t, AppPickKey(builtinAppsID, &Action{Type: ActionCopyText, Value: "x"}))
	require.Empty(t, AppPickKey(builtinAppsID, nil))
}

// TestAppCandidatesDesktopIDGate: Action.DesktopID is stamped only
// for bare *.desktop ids. The darwin scan fills InstalledApp.ID with
// the ".app" bundle name, which the app layer's run_command
// re-validation (launch.ValidDesktopID) rejects -- stamping it used
// to error every macOS launch, and off linux the credentialed
// desktop-id path does not exist anyway.
func TestAppCandidatesDesktopIDGate(t *testing.T) {
	cands := appCandidates([]InstalledApp{
		{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"},
		{Name: "Safari", Exec: `open -a "/Applications/Safari.app"`, ID: "Safari.app"},
		{Name: "Bare", Exec: "bare"},
	}, nil)
	require.Len(t, cands, 3)
	byTitle := map[string]*Action{}
	for _, c := range cands {
		res := c.Payload.(Result)
		byTitle[res.Title] = res.Action
	}
	require.Equal(t, "firefox.desktop", byTitle["Firefox"].DesktopID)
	require.Empty(t, byTitle["Safari"].DesktopID, "a darwin bundle name never rides as a desktop id")
	require.Equal(t, []string{"open", "-a", "/Applications/Safari.app"}, byTitle["Safari"].Argv)
	require.Empty(t, byTitle["Bare"].DesktopID)
}
