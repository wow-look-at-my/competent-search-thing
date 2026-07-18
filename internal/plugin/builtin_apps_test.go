package plugin

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppsProviderBasics(t *testing.T) {
	p := newAppsProvider(nil)
	require.Equal(t, "apps", p.id())
	require.Equal(t, "Launch", p.displayName())
	require.Equal(t, []string{"app", "launch"}, p.bangNames())
	_, _, ok := p.match("firefox", nil)
	require.False(t, ok, "targeted-only provider")

	results := srcResults(t, p, targetedReq("app", ""))
	require.Empty(t, results, "nil InstalledApps getter yields nothing")
}

func TestAppsProviderEmptyQueryListsAlphabetically(t *testing.T) {
	var list []InstalledApp
	for i := 20; i >= 1; i-- { // reverse input order: sorting must not rely on it
		list = append(list, InstalledApp{
			Name: fmt.Sprintf("App %02d", i),
			Exec: fmt.Sprintf("app%d %%u", i),
			ID:   fmt.Sprintf("app%d.desktop", i),
		})
	}
	p := newAppsProvider(func() []InstalledApp { return list })

	results := srcResults(t, p, targetedReq("app", ""))
	require.Len(t, results, maxAppResults)
	require.Equal(t, "App 01", results[0].Title)
	require.Equal(t, "App 15", results[14].Title)
	require.Equal(t, DefaultScore, *results[0].Score, "listed rows keep the neutral 50")
}

func TestAppsProviderSearchScoring(t *testing.T) {
	list := []InstalledApp{
		{Name: "Sunfire", Exec: "sunfire"},
		{Name: "Firefox", Exec: "firefox %u"},
		{Name: "GIMP", Exec: "gimp %F"},
		{Name: "Fireplace", Exec: "fireplace"},
	}
	p := newAppsProvider(func() []InstalledApp { return list })

	results := srcResults(t, p, targetedReq("launch", "FiRe"))
	require.Len(t, results, 3, "case-insensitive substring match")

	require.Equal(t, "Firefox", results[0].Title, "prefix matches first, alphabetical within")
	require.Equal(t, float64(73), *results[0].Score, "the engine's prefix band")
	require.Equal(t, "Fireplace", results[1].Title)
	require.Equal(t, float64(73), *results[1].Score)
	require.Equal(t, "Sunfire", results[2].Title, "substring matches after prefixes")
	require.Equal(t, float64(53), *results[2].Score, "the engine's substring band")

	require.Equal(t, "firefox", results[0].Subtitle, "subtitle is the cleaned exec line")
	require.Equal(t, "app", results[0].Icon)
	require.Equal(t, &Action{Type: ActionRunCommand, Argv: []string{"firefox"}}, results[0].Action)
}

func TestAppsProviderSkipsUnlaunchableAndCaps(t *testing.T) {
	list := []InstalledApp{
		{Name: "Broken", Exec: "%f"}, // parses to nothing
		{Name: "Empty", Exec: `""`},  // parses to one empty argument
	}
	for i := 1; i <= 20; i++ {
		list = append(list, InstalledApp{Name: fmt.Sprintf("Tool %02d", i), Exec: "tool"})
	}
	p := newAppsProvider(func() []InstalledApp { return list })

	results := srcResults(t, p, targetedReq("app", ""))
	require.Len(t, results, maxAppResults)
	require.Equal(t, "Tool 01", results[0].Title, "unlaunchable apps are skipped before the cap")

	results = srcResults(t, p, targetedReq("app", "tool"))
	require.Len(t, results, maxAppResults, "search results are capped too")
}

func TestAppsProviderIgnoresNonTargeted(t *testing.T) {
	p := newAppsProvider(func() []InstalledApp { return []InstalledApp{{Name: "X", Exec: "x"}} })
	results := srcResults(t, p, baseRequest("x", "x", 1, nil))
	require.Empty(t, results)
}

func TestAppsThroughDispatch(t *testing.T) {
	r := New(Options{
		InstalledApps: func() []InstalledApp {
			return []InstalledApp{{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"}}
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!app fire", 1, nil, emit)
	require.Equal(t, TargetInfo{Targeted: true, Plugin: "apps", Name: "Launch", Bang: "app"}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "apps", e.Plugin)
	require.Len(t, e.Results, 1)
	require.Equal(t, "Firefox", e.Results[0].Title)
	require.Equal(t, &Action{Type: ActionRunCommand, Argv: []string{"firefox"}, DesktopID: "firefox.desktop"},
		e.Results[0].Action, "the .desktop id rides the internal action for the credentialed launch path")
}

func TestParseDesktopExec(t *testing.T) {
	tests := []struct {
		name string
		exec string
		want []string
	}{
		{name: "simple with code", exec: "firefox %u", want: []string{"firefox"}},
		{name: "flags", exec: "/usr/bin/prog --new-window", want: []string{"/usr/bin/prog", "--new-window"}},
		{name: "quoted path with spaces", exec: `"/opt/My App/run" --x`, want: []string{"/opt/My App/run", "--x"}},
		{name: "escaped quote", exec: `prog "arg with \"quotes\""`, want: []string{"prog", `arg with "quotes"`}},
		{name: "escaped backslash", exec: `prog "a\\b"`, want: []string{"prog", `a\b`}},
		{name: "multiple field codes", exec: "env FOO=bar prog %F %i", want: []string{"env", "FOO=bar", "prog"}},
		{name: "percent escape", exec: "echo 100%%", want: []string{"echo", "100%"}},
		{name: "unknown code kept", exec: "prog %x", want: []string{"prog", "%x"}},
		{name: "trailing percent", exec: "prog %", want: []string{"prog", "%"}},
		{name: "empty", exec: "", want: nil},
		{name: "only a field code", exec: "%f", want: nil},
		{name: "spaces and tabs", exec: "  a \t b  ", want: []string{"a", "b"}},
		{name: "unterminated quote", exec: `prog "half`, want: []string{"prog", "half"}},
		{name: "empty quoted arg", exec: `prog ""`, want: []string{"prog", ""}},
		{name: "adjacent quoted parts", exec: `pre"fix suf"fix`, want: []string{"prefix suffix"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseDesktopExec(tt.exec))
		})
	}
}
