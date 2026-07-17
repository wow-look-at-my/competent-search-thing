package plugin

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// windowsRequest builds the request the dispatch pipeline would hand
// the provider for a plain (non-targeted) query.
func windowsRequest(stripped string) Request {
	return Request{V: ProtocolVersion, Query: stripped, Stripped: stripped, Gen: 1}
}

func TestWindowsProviderIdentity(t *testing.T) {
	p := newWindowsProvider(func() []WindowInfo { return nil })
	require.Equal(t, "windows", p.id())
	require.Equal(t, "Open Windows", p.displayName())
	require.Empty(t, p.bangNames(), "not bang-reachable")
	require.Zero(t, p.debounce())
}

func TestWindowsProviderMatchMinLength(t *testing.T) {
	p := newWindowsProvider(func() []WindowInfo { return nil })
	cases := []struct {
		query    string
		stripped string
		ok       bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"a", "", false},
		{" a ", "", false}, // one rune after trimming
		{"ab", "ab", true},
		{"  fire  ", "fire", true}, // trimmed like the all_queries trigger path
		{"ab cd", "ab cd", true},
	}
	for _, tc := range cases {
		stripped, boost, ok := p.match(tc.query, nil)
		require.Equal(t, tc.ok, ok, "query %q", tc.query)
		require.Equal(t, tc.stripped, stripped, "query %q", tc.query)
		require.Zero(t, boost, "query %q never boosts", tc.query)
	}
}

func TestWindowsProviderRanking(t *testing.T) {
	wins := []WindowInfo{
		{ID: "1", Title: "main.go - Code", App: "code", PID: 10},         // "main": title word start
		{ID: "2", Title: "domain-list.txt", App: "mainapp", PID: 20},     // "main": app prefix (title hit is mid-word)
		{ID: "3", Title: "the domain overview", App: "firefox", PID: 30}, // "main": title substring only
		{ID: "4", Title: "notes", App: "xmainx", PID: 40},                // "main": app substring only
		{ID: "5", Title: "unrelated", App: "kitty", PID: 50},             // no match
	}
	p := newWindowsProvider(func() []WindowInfo { return wins })

	results, dropped, err := p.query(context.Background(), windowsRequest("MAIN"))
	require.NoError(t, err)
	require.Empty(t, dropped)
	require.Len(t, results, 4, "matching is case-insensitive; non-matches dropped")

	var titles []string
	var scores []float64
	for _, r := range results {
		titles = append(titles, r.Title)
		require.NotNil(t, r.Score)
		scores = append(scores, *r.Score)
	}
	require.Equal(t, []string{"main.go - Code", "domain-list.txt", "the domain overview", "notes"}, titles,
		"title word-start > app prefix > title substring > app substring")
	require.Equal(t, []float64{85, 80, 65, 60}, scores)
}

func TestWindowsProviderResultShape(t *testing.T) {
	p := newWindowsProvider(func() []WindowInfo {
		return []WindowInfo{{ID: "4294967295", Title: "Mozilla Firefox", App: "firefox", PID: 7}}
	})
	results, _, err := p.query(context.Background(), windowsRequest("fire"))
	require.NoError(t, err)
	require.Len(t, results, 1)
	r := results[0]
	require.Equal(t, "Mozilla Firefox", r.Title)
	require.Equal(t, "firefox", r.Subtitle, "app name is the subtitle")
	require.Equal(t, "app", r.Icon)
	require.Equal(t, &Action{Type: ActionActivateWindow, Window: "4294967295"}, r.Action,
		"the internal-only action carries the window id verbatim (full uint32 range)")
}

func TestWindowsProviderTieBreakAlphabeticalAndCap(t *testing.T) {
	var wins []WindowInfo
	for i := 9; i >= 0; i-- { // reverse alphabetical input
		wins = append(wins, WindowInfo{
			ID:    fmt.Sprint(i),
			Title: fmt.Sprintf("term %d", i),
			App:   "kitty",
		})
	}
	p := newWindowsProvider(func() []WindowInfo { return wins })
	results, _, err := p.query(context.Background(), windowsRequest("term"))
	require.NoError(t, err)
	require.Len(t, results, maxWindowResults, "capped at 8")
	for i, r := range results {
		require.Equal(t, fmt.Sprintf("term %d", i), r.Title, "equal scores sort alphabetically by title")
	}
}

func TestWindowsProviderEmptyAndDegenerateInputs(t *testing.T) {
	// Empty snapshot: no results, so dispatch emits nothing.
	p := newWindowsProvider(func() []WindowInfo { return nil })
	results, dropped, err := p.query(context.Background(), windowsRequest("fire"))
	require.NoError(t, err)
	require.Empty(t, dropped)
	require.Empty(t, results)

	// Nil getter (never happens via New, which requires the seam): safe.
	p = &windowsProvider{builtinBase: builtinBase{pid: builtinWindowsID, name: "Open Windows"}}
	results, _, err = p.query(context.Background(), windowsRequest("fire"))
	require.NoError(t, err)
	require.Empty(t, results)

	// Defensive: an empty stripped query yields nothing rather than
	// matching everything, and untitled windows are skipped.
	p = newWindowsProvider(func() []WindowInfo {
		return []WindowInfo{{ID: "1", Title: "", App: "fireplace"}}
	})
	results, _, err = p.query(context.Background(), windowsRequest(""))
	require.NoError(t, err)
	require.Empty(t, results)
	results, _, err = p.query(context.Background(), windowsRequest("fire"))
	require.NoError(t, err)
	require.Empty(t, results, "untitled windows never become results")
}

func TestWordStartWindowTitleCases(t *testing.T) {
	cases := []struct {
		haystack, needle string
		want             bool
	}{
		{"main.go - code", "main", true}, // start of string
		{"app - main.go", "main", true},  // after a space
		{"foo-main", "main", true},       // after punctuation
		{"[main] scratch", "main", true}, // after a bracket
		{"domain", "main", false},        // mid-word only
		{"domain main", "main", true},    // later word-start occurrence wins
		{"xx2main", "main", false},       // digits end a word too
		{"", "main", false},
		{"main", "nomatch", false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, wordStart(tc.haystack, tc.needle),
			"wordStart(%q, %q)", tc.haystack, tc.needle)
	}
}
