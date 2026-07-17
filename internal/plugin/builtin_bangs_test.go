package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// newSuggestRig wires a test registry with the REAL suggestions
// builtin plus the given fake providers.
func newSuggestRig(t *testing.T, sigils []string, aliases map[string]string, providers ...provider) (*Registry, *bangSuggestProvider) {
	t.Helper()
	r, _ := newTestRegistry(t, sigils, aliases, providers...)
	s := newBangSuggestProvider(r)
	r.suggest = s
	r.byID[builtinSuggestID] = s
	return r, s
}

func TestBangSuggestAmbiguousPreservesRest(t *testing.T) {
	calc := &fakeProvider{pid: "calc-plugin", name: "Calculator", bangs: []string{"calc", "calx"}}
	_, s := newSuggestRig(t, nil, nil, calc)

	results, dropped, err := s.query(context.Background(), baseRequest("!ca 2+2", "!ca 2+2", 1, nil))
	require.NoError(t, err)
	require.Empty(t, dropped)
	require.Len(t, results, 2)

	require.Equal(t, "!calc", results[0].Title)
	require.Equal(t, "Calculator", results[0].Subtitle)
	require.Equal(t, "hash", results[0].Icon)
	require.Equal(t, float64(90), *results[0].Score)
	require.Equal(t, &Action{Type: ActionSetQuery, Value: "!calc 2+2"}, results[0].Action,
		"completing the bang preserves the rest of the query")

	require.Equal(t, "!calx", results[1].Title)
	require.Equal(t, float64(89), *results[1].Score, "stable descending order")
	require.Equal(t, "!calx 2+2", results[1].Action.Value)
}

func TestBangSuggestBareSigilListsAllCapped(t *testing.T) {
	var bangs []string
	for i := 1; i <= 15; i++ {
		bangs = append(bangs, fmt.Sprintf("b%02d", i))
	}
	many := &fakeProvider{pid: "many", name: "Many", bangs: bangs}
	_, s := newSuggestRig(t, nil, nil, many)

	results, _, err := s.query(context.Background(), baseRequest("!", "!", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, maxBangSuggestions)
	require.Equal(t, "!b01", results[0].Title)
	require.Equal(t, "!b01 ", results[0].Action.Value, "empty rest leaves a trailing space ready to type")
	require.Equal(t, "!b12", results[11].Title)
	require.Equal(t, float64(79), *results[11].Score)
}

func TestBangSuggestResolvedWithoutSpace(t *testing.T) {
	calc := &fakeProvider{pid: "calc", name: "Calculator", bangs: []string{"calc", "calx"}}
	_, s := newSuggestRig(t, nil, nil, calc)

	results, _, err := s.query(context.Background(), baseRequest("!calc", "!calc", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "!calc", results[0].Title)
	require.Equal(t, "!calc ", results[0].Action.Value)
}

func TestBangSuggestAliasResolvedListedFirst(t *testing.T) {
	// Alias "a" resolves to calc although the bang "apple" is the only
	// prefix candidate: the resolved bang still leads the list.
	apple := &fakeProvider{pid: "apple-plugin", name: "Apple", bangs: []string{"apple"}}
	calc := &fakeProvider{pid: "calc-plugin", name: "Calculator", bangs: []string{"calc"}}
	_, s := newSuggestRig(t, nil, map[string]string{"a": "calc"}, apple, calc)

	results, _, err := s.query(context.Background(), baseRequest("!a", "!a", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "!calc", results[0].Title, "alias-resolved bang first")
	require.Equal(t, "Calculator", results[0].Subtitle)
	require.Equal(t, "!apple", results[1].Title)
	require.Equal(t, "Apple", results[1].Subtitle)
}

func TestBangSuggestCustomSigils(t *testing.T) {
	calc := &fakeProvider{pid: "calc", name: "Calculator", bangs: []string{"calc", "calx"}}
	_, s := newSuggestRig(t, []string{"$", "?"}, nil, calc)

	results, _, err := s.query(context.Background(), baseRequest("?ca 2+2", "?ca 2+2", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "$calc", results[0].Title, "titles use the primary (first configured) sigil")
	require.Equal(t, "?calc 2+2", results[0].Action.Value, "set_query keeps the sigil the user typed")
}

func TestBangSuggestNonBangQueryYieldsNothing(t *testing.T) {
	_, s := newSuggestRig(t, nil, nil)
	results, _, err := s.query(context.Background(), baseRequest("plain", "plain", 1, nil))
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestBangSuggestThroughDispatch(t *testing.T) {
	// End-to-end through New: an ambiguous partial lists builtin bangs.
	r := New(Options{Version: "1.0", Logf: func(string, ...any) {}})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!re", 3, nil, emit) // reload | rescan
	require.Equal(t, TargetInfo{}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "bangs", e.Plugin)
	require.Equal(t, "Commands", e.Name)
	require.Equal(t, int64(3), e.Gen)
	require.Len(t, e.Results, 2)
	require.Equal(t, "!reload", e.Results[0].Title)
	require.Equal(t, "App Commands", e.Results[0].Subtitle)
	require.Equal(t, "!rescan", e.Results[1].Title)
}

func TestCheatSheetDefaultBuiltins(t *testing.T) {
	// A default builtins-only registry lists every builtin bang, sorted,
	// exactly as a bare "!" would.
	r := New(Options{Version: "1.0", Logf: func(string, ...any) {}})
	defer r.Close()

	e := r.CheatSheet()
	require.Equal(t, "bangs", e.Plugin)
	require.Equal(t, "Commands", e.Name)
	require.EqualValues(t, 0, e.Gen)

	wantBangs := []string{"app", "config", "launch", "quit", "reload", "rescan", "version"}
	subtitles := map[string]string{
		"app":     "Launch",
		"launch":  "Launch",
		"config":  "App Commands",
		"quit":    "App Commands",
		"reload":  "App Commands",
		"rescan":  "App Commands",
		"version": "App Commands",
	}
	require.Len(t, e.Results, len(wantBangs))
	for i, b := range wantBangs {
		res := e.Results[i]
		require.Equal(t, "!"+b, res.Title, "titles use the primary sigil, sorted order")
		require.Equal(t, subtitles[b], res.Subtitle, "subtitle names the providing builtin")
		require.Equal(t, "hash", res.Icon)
		require.Equal(t, float64(90-i), *res.Score, "scores descend by list position")
		require.Equal(t, &Action{Type: ActionSetQuery, Value: "!" + b + " "}, res.Action,
			"completion leaves a trailing space ready to type")
	}
}

func TestCheatSheetIncludesManifestBangs(t *testing.T) {
	m := &Manifest{
		V:       1,
		ID:      "calc",
		Name:    "Calculator",
		Type:    TypeCommand,
		Bangs:   []string{"calc"},
		Command: &CommandSpec{Argv: []string{"true"}},
	}
	r := New(Options{Manifests: []*Manifest{m}, Logf: func(string, ...any) {}})
	defer r.Close()

	e := r.CheatSheet()
	var calc *Result
	for i := range e.Results {
		if e.Results[i].Title == "!calc" {
			calc = &e.Results[i]
		}
	}
	require.NotNil(t, calc, "a manifest-registered bang appears in the sheet")
	require.Equal(t, "Calculator", calc.Subtitle)
}

func TestCheatSheetCappedAtMax(t *testing.T) {
	var bangs []string
	for i := 1; i <= 15; i++ {
		bangs = append(bangs, fmt.Sprintf("b%02d", i))
	}
	many := &fakeProvider{pid: "many", name: "Many", bangs: bangs}
	r, _ := newSuggestRig(t, nil, nil, many)

	e := r.CheatSheet()
	require.Len(t, e.Results, maxBangSuggestions)
	require.Equal(t, "!b01", e.Results[0].Title)
	require.Equal(t, "!b12", e.Results[11].Title)
}

func TestCheatSheetSuggestionsDisabled(t *testing.T) {
	r := New(Options{
		Entries: map[string]Entry{builtinSuggestID: {Disabled: true}},
		Logf:    func(string, ...any) {},
	})
	defer r.Close()
	require.Equal(t, Emission{}, r.CheatSheet(),
		"a disabled suggestions provider yields the zero Emission")
}

func TestCheatSheetUsesConfiguredPrimarySigil(t *testing.T) {
	r := New(Options{Sigils: []string{"/", "!"}, Logf: func(string, ...any) {}})
	defer r.Close()

	e := r.CheatSheet()
	require.NotEmpty(t, e.Results)
	require.Equal(t, "/app", e.Results[0].Title, "titles use the primary (first configured) sigil")
	require.Equal(t, &Action{Type: ActionSetQuery, Value: "/app "}, e.Results[0].Action,
		"completion keeps the primary sigil the sheet was built from")
}

func TestCheatSheetProviderErrorYieldsZero(t *testing.T) {
	// The builtin never errors; a defensive fake proves an erroring
	// suggest provider degrades to the zero Emission plus one log line.
	r, lc := newTestRegistry(t, nil, nil)
	boom := &fakeProvider{
		pid:  builtinSuggestID,
		name: "Commands",
		queryFn: func(context.Context, Request) ([]Result, []string, error) {
			return nil, nil, errors.New("boom")
		},
	}
	r.suggest = boom
	r.byID[builtinSuggestID] = boom

	require.Equal(t, Emission{}, r.CheatSheet())
	require.Contains(t, lc.joined(), "cheat sheet: boom")
}
