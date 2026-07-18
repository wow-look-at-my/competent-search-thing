package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testRewriteRules(t *testing.T, rules ...RewriteRule) (*rewritesProvider, *[]string) {
	t.Helper()
	compiled, errs := compileRewrites(rules)
	require.Empty(t, errs)
	var logs []string
	p := newRewritesProvider(compiled, func(format string, args ...any) {
		logs = append(logs, format)
	})
	return p, &logs
}

func TestCompileRewrites(t *testing.T) {
	rules, errs := compileRewrites([]RewriteRule{
		{Name: "jira", Pattern: `[A-Z]+-\d+`, Replacement: "https://my.jira.com/$0"},
		{Name: "off", Pattern: "x+", Replacement: "https://x.example/", Disabled: true},
		{Name: "broken", Pattern: "([unclosed", Replacement: "https://y.example/"},
		{Name: "", Pattern: "", Replacement: ""},
		{Name: "anchored", Pattern: "^gh-(\\d+)", Replacement: "https://github.example/$1"},
	})
	require.Len(t, rules, 2, "disabled, invalid, and empty rules are skipped")
	require.Len(t, errs, 2, "one loud error per broken rule")
	require.Contains(t, errs[0].Error(), "broken")
	require.Contains(t, errs[1].Error(), "pattern and replacement are required")

	// Full-match wrapping by default; user anchors disable it.
	require.True(t, rules[0].re.MatchString("XY-12345"))
	require.False(t, rules[0].re.MatchString("see XY-12345 here"), "full match by default")
	require.True(t, rules[1].re.MatchString("gh-42 and more"), "self-anchored patterns stay loose")
}

func TestRewritesJiraExample(t *testing.T) {
	p, _ := testRewriteRules(t, RewriteRule{
		Name: "jira", Pattern: `[A-Z]+-\d+`, Replacement: "https://my.jira.com/$0",
	})

	stripped, boost, ok := p.match("  XY-12345  ", nil)
	require.True(t, ok, "a pasted Jira key claims the query (trimmed)")
	require.Equal(t, "XY-12345", stripped)
	require.Zero(t, boost)
	_, _, ok = p.match("hello world", nil)
	require.False(t, ok)

	results := srcResults(t, p, baseRequest("XY-12345", "XY-12345", 1, nil))
	require.Len(t, results, 1)
	require.Equal(t, "https://my.jira.com/XY-12345", results[0].Title, "title defaults to the URL")
	require.Equal(t, "jira", results[0].Subtitle)
	require.Equal(t, "link", results[0].Icon)
	require.Equal(t, &Action{Type: ActionOpenURL, Value: "https://my.jira.com/XY-12345"}, results[0].Action)
	require.Equal(t, 100.0, *results[0].Score, "instant unambiguous top result (triggered tier)")
}

func TestRewritesGroupExpansionAndTitles(t *testing.T) {
	p, _ := testRewriteRules(t, RewriteRule{
		Name:        "ticket",
		Pattern:     `(?P<proj>[A-Z]+)-(?P<num>\d+)`,
		Replacement: "https://t.example/${proj}/issues/${num}",
		Title:       "Open ${proj} ticket ${num}",
		Icon:        "star",
	})
	results := srcResults(t, p, baseRequest("AB-7", "AB-7", 1, nil))
	require.Len(t, results, 1)
	require.Equal(t, "Open AB ticket 7", results[0].Title, "named groups expand in titles too")
	require.Equal(t, "star", results[0].Icon)
	require.Equal(t, "https://t.example/AB/issues/7", results[0].Action.Value)

	// $$ escapes a literal dollar.
	p2, _ := testRewriteRules(t, RewriteRule{
		Name: "d", Pattern: `(\d+)`, Replacement: "https://d.example/$$$1",
	})
	results = srcResults(t, p2, baseRequest("42", "42", 1, nil))
	require.Equal(t, "https://d.example/$42", results[0].Action.Value)
}

func TestRewritesMultiRuleConfigOrder(t *testing.T) {
	p, _ := testRewriteRules(t,
		RewriteRule{Name: "first", Pattern: `\d+`, Replacement: "https://a.example/$0"},
		RewriteRule{Name: "second", Pattern: `\d\d\d`, Replacement: "https://b.example/$0"},
	)
	results := srcResults(t, p, baseRequest("123", "123", 1, nil))
	require.Len(t, results, 2, "every matching rule emits")
	require.Equal(t, "first", results[0].Subtitle, "config order decides")
	require.Equal(t, "second", results[1].Subtitle)
	require.Greater(t, *results[0].Score, *results[1].Score)
}

func TestRewritesSchemeValidation(t *testing.T) {
	p, logs := testRewriteRules(t,
		RewriteRule{Name: "evil", Pattern: `(.+)`, Replacement: "javascript:alert('$1')"},
		RewriteRule{Name: "relative", Pattern: `(.+)`, Replacement: "/just/a/path"},
		RewriteRule{Name: "ok", Pattern: `(.+)`, Replacement: "https://ok.example/$1"},
	)
	results := srcResults(t, p, baseRequest("x", "x", 1, nil))
	require.Len(t, results, 1, "non-http(s) expansions are dropped")
	require.Equal(t, "ok", results[0].Subtitle)
	require.Len(t, *logs, 2, "each dropped expansion is logged")

	// Nothing matches / empty stripped: no candidates.
	empty, err := p.candidates(context.Background(), baseRequest("", "", 1, nil))
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestRewritesThroughDispatch(t *testing.T) {
	r := New(Options{
		Rewrites: []RewriteRule{
			{Name: "jira", Pattern: `[A-Z]+-\d+`, Replacement: "https://my.jira.com/$0"},
			{Name: "bad", Pattern: "([", Replacement: "https://x.example/"},
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	require.Len(t, r.Errors(), 1, "the broken rule surfaced as a startup error")
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "XY-12345", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	e := recvEmission(t, ch)
	require.Equal(t, builtinRewritesID, e.Plugin)
	require.Len(t, e.Results, 1)
	require.Equal(t, "https://my.jira.com/XY-12345", e.Results[0].Title)
	require.GreaterOrEqual(t, *e.Results[0].Score, 86.0, "triggered tier: the instant top result")

	r.Dispatch(context.Background(), "no rule matches this", 2, nil, emit)
	requireNoEmission(t, ch, 150*time.Millisecond)
}

func TestRewritesAllRulesBrokenRegistersNothing(t *testing.T) {
	r := New(Options{
		Rewrites: []RewriteRule{{Name: "bad", Pattern: "([", Replacement: "https://x/"}},
		Logf:     func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, builtinRewritesID)
	found := false
	for _, err := range r.Errors() {
		if strings.Contains(err.Error(), "bad") {
			found = true
		}
	}
	require.True(t, found)
}
