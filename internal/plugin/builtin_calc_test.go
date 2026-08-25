package plugin

// Tests for the builtin calculator provider: the evaluator's
// arithmetic surface, the trigger/bang wiring, and the minted row
// shape (fields, action, formatting bounds).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// evalCases exercises evalCalc directly.
var evalCases = []struct {
	expr string
	want string // formatted result; "" = must fail
}{
	// Basic arithmetic
	{"2+2", "4"},
	{"2 + 3 * 4", "14"},
	{"(2+3)*4", "20"},
	{"10/4", "2.5"},
	{"7//2", "3"},
	{"7%3", "1"},
	{"2**10", "1024"},
	{"2**3**2", "512"}, // right-assoc: 2**(3**2)
	{"-5+3", "-2"},
	{"+5", "5"},
	{"-(-4)", "4"},
	{"1.5*2", "3"},
	{"0.1+0.2", "0.3"}, // float formatting trims artifacts
	{"1e3", "1000"},
	{"2.5e2", "250"},
	{"0x10", "16"},
	{"0b101", "5"},
	{"0o17", "15"},
	{"0x1e", "30"}, // hex digits may contain 'e'; still an integer
	{"0xdeadbeef", "3735928559"},
	{"1/3", "0.333333333333"},
	{"2**0.5", "1.41421356237"},
	{"7 % 4", "3"},
	{"100/8", "12.5"},
	{"2**-2", "0.25"},
	{"1//3", "0"},
	// Floor division and modulo follow Python semantics: -7//2 == -4,
	// 7%-3 == -2, -7%2 == 1 (the example plugin's behavior).
	{"-7//2", "-4"},
	{"7%-3", "-2"},
	{"-7%2", "1"},

	// Must fail
	{"", ""},
	{"  ", ""},
	{"abc", ""},
	{"2+", ""},
	{"2++", ""},
	{"(2+3", ""},
	{"2+3)", ""},
	{"2..3", ""},
	{"1e", ""},
	{"0x", ""},
	{"2//0", ""},
	{"2%0", ""},
	{"1/0", ""},
	// Bounds and overflow: refused rather than wrapped or hung.
	{"2**999", ""},
	{"0**-1", ""},
	{"2**63", ""},
	{"99999999999999999999", ""},
	{"9223372036854775807+1", ""},
	{"-9223372036854775807-2", ""},
	// Exactly MinInt64 is representable and valid.
	{"-9223372036854775807-1", "-9223372036854775808"},
	{"2+2 x", ""},
	{"x+1", ""},
	{"2 and 3", ""},
}

func TestCalcEval(t *testing.T) {
	for _, tc := range evalCases {
		t.Run(tc.expr, func(t *testing.T) {
			v, ok := evalCalc(tc.expr)
			if tc.want == "" {
				require.False(t, ok, "expected %q to fail", tc.expr)
				return
			}
			require.True(t, ok, "expected %q to evaluate", tc.expr)
			got, ok := formatCalcValue(v)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCalcDeepExpressionBounded(t *testing.T) {
	// A pathological nesting must fail fast, not recurse away.
	depth := 200
	expr := ""
	for i := 0; i < depth; i++ {
		expr += "("
	}
	expr += "1"
	for i := 0; i < depth; i++ {
		expr += ")"
	}
	_, ok := evalCalc(expr)
	require.False(t, ok, "over-deep nesting must be refused")
}

func TestCalcCandidatesRowShape(t *testing.T) {
	p := newCalcProvider()
	cands, err := p.candidates(context.Background(), Request{Stripped: "6*7"})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	res, ok := cands[0].Payload.(Result)
	require.True(t, ok)
	require.Equal(t, "42", res.Title)
	require.Equal(t, "6*7 =", res.Subtitle)
	require.Equal(t, "calculator", res.Icon)
	require.Equal(t, "CALC", res.Badge)
	require.Equal(t, "#a6e3a1", res.AccentColor)
	require.NotNil(t, res.Action)
	require.Equal(t, ActionCopyText, res.Action.Type)
	require.Equal(t, "42", res.Action.Value)
	require.Len(t, res.Fields, 2)
	require.Equal(t, "Hex", res.Fields[0].Label)
	require.Equal(t, "0x2a", res.Fields[0].Value)
	require.Equal(t, "Binary", res.Fields[1].Label)
	require.Equal(t, "0b101010", res.Fields[1].Value)
}

func TestCalcCandidatesFloatNoFields(t *testing.T) {
	p := newCalcProvider()
	cands, err := p.candidates(context.Background(), Request{Stripped: "1/4"})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	res := cands[0].Payload.(Result)
	require.Equal(t, "0.25", res.Title)
	require.Empty(t, res.Fields, "floats carry no Hex/Binary fields")
}

func TestCalcCandidatesNoResult(t *testing.T) {
	p := newCalcProvider()
	for _, q := range []string{"hello", "2+", "abc", "9**999", ""} {
		cands, err := p.candidates(context.Background(), Request{Stripped: q})
		require.NoError(t, err)
		require.Empty(t, cands, "query %q must yield no result", q)
	}
}

func TestCalcCandidatesNegativeInteger(t *testing.T) {
	p := newCalcProvider()
	cands, err := p.candidates(context.Background(), Request{Stripped: "5-7"})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	res := cands[0].Payload.(Result)
	require.Equal(t, "-2", res.Title)
	// Negative integers still get Hex/Binary fields, rendered
	// Python-style ("-0x2"), matching the example plugin.
	require.Len(t, res.Fields, 2)
	require.Equal(t, "-0x2", res.Fields[0].Value)
	require.Equal(t, "-0b10", res.Fields[1].Value)
}

func TestCalcMatchPrefix(t *testing.T) {
	p := newCalcProvider()
	stripped, boost, ok := p.match("=2+2", nil)
	require.True(t, ok)
	require.Equal(t, "2+2", stripped)
	require.Equal(t, 0, boost)
	_, _, ok = p.match("= 2+2", nil)
	require.True(t, ok)
	_, _, ok = p.match("2+2", nil)
	require.False(t, ok)
	_, _, ok = p.match("=", nil)
	require.False(t, ok, "a bare sigil with no expression is not claimed")
	_, _, ok = p.match("=   ", nil)
	require.False(t, ok)
	_, _, ok = p.match("==2", nil)
	require.True(t, ok, "the first '=' is the prefix; '=2' is the expression")
}

func TestCalcProviderMetadata(t *testing.T) {
	p := newCalcProvider()
	require.Equal(t, builtinCalcID, p.id())
	require.Equal(t, "Calculator", p.displayName())
	require.Equal(t, []string{"calc", "c"}, p.bangNames())
	require.Equal(t, 1, p.limit())
	require.True(t, p.preRanked())
	_, _, ok := p.match("hello", nil)
	require.False(t, ok, "plain text queries never reach the calculator")
	_, _, ok = p.match("calc", nil)
	require.False(t, ok, "the word 'calc' alone is not claimed")
}

func TestCalcProviderRegisteredByDefault(t *testing.T) {
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	require.Contains(t, r.byID, builtinCalcID)
	require.IsType(t, &calcProvider{}, r.byID[builtinCalcID])

	pid, _, ok := r.bangs.Resolve("calc")
	require.True(t, ok)
	require.Equal(t, builtinCalcID, pid)
	pid, _, ok = r.bangs.Resolve("c")
	require.True(t, ok)
	require.Equal(t, builtinCalcID, pid)
}

func TestCalcProviderDisableable(t *testing.T) {
	r := New(Options{
		Entries: map[string]Entry{builtinCalcID: {Disabled: true}},
		Logf:    func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, builtinCalcID)
	_, _, ok := r.bangs.Resolve("calc")
	require.False(t, ok, "disabled builtin registers no bangs")
}

// TestCalcBangDispatch routes "!calc 2+2" through the registry and
// asserts the emitted row.
func TestCalcBangDispatch(t *testing.T) {
	r, _ := newTestRegistry(t, nil, nil, newCalcProvider())
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!calc 2+2", 7, nil, emit)
	require.True(t, info.Targeted)
	require.Equal(t, builtinCalcID, info.Plugin)
	require.Equal(t, "calc", info.Bang)

	e := recvEmission(t, ch)
	require.Equal(t, builtinCalcID, e.Plugin)
	require.Equal(t, "Calculator", e.Name)
	require.EqualValues(t, 7, e.Gen)
	require.Len(t, e.Results, 1)
	require.Equal(t, "4", e.Results[0].Title)
	require.Equal(t, &Action{Type: ActionCopyText, Value: "4"}, e.Results[0].Action)
}

// TestCalcPrefixDispatch routes "=3+4" through the registry's
// non-targeted path.
func TestCalcPrefixDispatch(t *testing.T) {
	r, _ := newTestRegistry(t, nil, nil, newCalcProvider())
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "=3+4", 8, nil, emit)
	require.False(t, info.Targeted, "a '=' query is trigger-claimed, not bang-targeted")

	e := recvEmission(t, ch)
	require.Equal(t, builtinCalcID, e.Plugin)
	require.Len(t, e.Results, 1)
	require.Equal(t, "7", e.Results[0].Title)
}
