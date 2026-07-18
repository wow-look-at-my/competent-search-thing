package match

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testFold lowers s the way the engine folds it against a pattern of
// the given regime (the reference model used by internal/index too).
func testFold(s string, ascii bool) string {
	if ascii {
		b := []byte(s)
		for i := range b {
			b[i] = FoldTable[b[i]]
		}
		return string(b)
	}
	return strings.Map(FoldRune, s)
}

func TestFoldTableExhaustive(t *testing.T) {
	for i := 0; i < 256; i++ {
		b := byte(i)
		want := b
		if b >= 'A' && b <= 'Z' {
			want = b + ('a' - 'A')
		}
		require.Equal(t, want, FoldTable[b], "byte 0x%02x", b)
	}
}

func TestFoldPatternRegimes(t *testing.T) {
	cases := []struct {
		q     string
		pat   string
		ascii bool
	}{
		{"", "", true},
		{"FiReFoX", "firefox", true},
		{"a b", "a b", true}, // FoldPattern never splits; Terms does
		{"\u0130STANBUL", "istanbul", false},
		{"M\u00dcsic", "m\u00fcsic", false},
		{"a\xffb", "a\ufffdb", false},
	}
	for _, tc := range cases {
		pat, ascii := FoldPattern(tc.q)
		require.Equal(t, tc.pat, string(pat), "query %q", tc.q)
		require.Equal(t, tc.ascii, ascii, "query %q regime", tc.q)
	}
}

// TestFoldParityWithStringsToLower pins the shared fold definition
// against the stdlib model on randomized unicode-heavy strings: the
// engine's per-string helpers agree with strings operations over
// testFold copies -- the same parity internal/index's references pin.
func TestFoldParityWithStringsToLower(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	runes := []rune{
		'a', 'B', 'z', '0', '.', ' ', '-',
		'\u00c4', '\u00e4', '\u00dc', '\u00fc', '\u00df', '\u1e9e',
		'\u0130', '\u0131', '\u212a', '\u017f', '\U0001f600',
	}
	randStr := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteRune(runes[rng.Intn(len(runes))])
		}
		return b.String()
	}
	for iter := 0; iter < 4000; iter++ {
		s := randStr(rng.Intn(10))
		q := randStr(1 + rng.Intn(4))
		pat, ascii := FoldPattern(q)
		ps := string(pat)
		folded := testFold(s, ascii)
		if ascii {
			require.Equal(t, strings.HasPrefix(folded, ps), HasPrefixASCII(s, ps), "prefix %q %q", s, q)
			require.Equal(t, strings.HasSuffix(folded, ps), HasSuffixASCII(s, ps), "suffix %q %q", s, q)
			require.Equal(t, strings.Index(folded, ps), IndexASCII(s, ps), "index %q %q", s, q)
			require.Equal(t, strings.Contains(folded, ps), ContainsASCII(s, ps), "contains %q %q", s, q)
		} else {
			require.Equal(t, strings.HasPrefix(folded, ps), FoldPrefixLen(s, ps) >= 0, "prefix %q %q", s, q)
			require.Equal(t, strings.HasSuffix(folded, ps), FoldHasSuffix(s, ps), "suffix %q %q", s, q)
			require.Equal(t, strings.Contains(folded, ps), FoldContains(s, ps), "contains %q %q", s, q)
			require.Equal(t, folded == ps, FoldEquals(s, ps), "equals %q %q", s, q)
		}
	}
}

func TestTerms(t *testing.T) {
	require.Nil(t, Terms(""))
	require.Nil(t, Terms("   \t "))
	require.Equal(t, []Term{{Pat: "firefox", ASCII: true}}, Terms("Firefox"))
	require.Equal(t, []Term{{Pat: "firefox", ASCII: true}}, Terms("  Firefox  "),
		"surrounding whitespace never becomes part of a term")
	require.Equal(t,
		[]Term{{Pat: "fire", ASCII: true}, {Pat: "fox", ASCII: true}},
		Terms("fire fox"))
	require.Equal(t,
		[]Term{{Pat: "fire", ASCII: true}, {Pat: "fire", ASCII: true}},
		Terms("fire\tfire"), "repeated terms are kept (each must match)")
	require.Equal(t,
		[]Term{{Pat: "m\u00fcsic", ASCII: false}, {Pat: "song", ASCII: true}},
		Terms("M\u00dcsic SONG"), "regimes are chosen per term")
}

func TestTermUnitsAndRuneLen(t *testing.T) {
	a := NewTerm("Fox")
	require.True(t, a.ASCII)
	require.Equal(t, []int32{'f', 'o', 'x'}, a.Units())
	require.Equal(t, 3, a.RuneLen())

	u := NewTerm("\u00c4h")
	require.False(t, u.ASCII)
	require.Equal(t, []int32{0xE4, 'h'}, u.Units())
	require.Equal(t, 2, u.RuneLen())
}

func TestSubseq(t *testing.T) {
	cases := []struct {
		s, q string
		want bool
	}{
		{"foo_bar", "fb", true},
		{"foo_bar", "bf", false},
		{"FOO_BAR", "fb", true},
		{"Firefox", "firefx", true},
		{"fo", "foo", false},
		{"", "a", false},
		{"anything", "", true},
	}
	for _, tc := range cases {
		term := NewTerm(tc.q)
		require.Equal(t, tc.want, Subseq(tc.s, term), "Subseq(%q, %q)", tc.s, tc.q)
		require.Equal(t, tc.want, SubseqASCII(tc.s, term.Pat), "SubseqASCII(%q, %q)", tc.s, tc.q)
		require.Equal(t, tc.want, SubseqFold(tc.s, PatternUnits(term.Pat, false)),
			"rune-walk parity (%q, %q)", tc.s, tc.q)
	}
	require.True(t, Subseq("Stra\u1e9eE", NewTerm("s\u00dfe")))
	require.False(t, Subseq("M\u00fcsic", NewTerm("\u00f6m")))
}

func TestMatchTermLadder(t *testing.T) {
	cases := []struct {
		target, q string
		want      Tier
	}{
		{"Fire", "fire", TierExact},
		{"FIRE", "fire", TierExact},
		{"Firefox", "fire", TierPrefix},
		{"Amazon Fire TV", "fire", TierWordStart},
		{"gnome-fire-manager", "fire", TierWordStart},
		{"org.fire.Tool", "fire", TierWordStart},
		{"Campfire", "fire", TierSubstring},
		{"Firefox", "firefx", TierFuzzy},
		{"Firefox", "fox", TierSubstring}, // fireFOX: mid-word
		{"GIMP", "fire", TierNone},
		{"", "fire", TierNone},
		{"Visual Studio Code", "visual studio", TierPrefix}, // literal phrase term
		{"upper CASE Word", "case", TierWordStart},
	}
	for _, tc := range cases {
		got := MatchTerm(tc.target, NewTerm(tc.q), true)
		require.Equal(t, tc.want, got, "MatchTerm(%q, %q)", tc.target, tc.q)
	}

	// Word semantics: words are letter/digit runs, so a digit does NOT
	// start a new word ("x1able" holds "able" mid-word only).
	require.Equal(t, TierSubstring, MatchTerm("x1able", NewTerm("able"), true))

	// allowFuzzy=false stops the ladder at substring.
	require.Equal(t, TierNone, MatchTerm("Firefox", NewTerm("firefx"), false))
	require.Equal(t, TierSubstring, MatchTerm("Campfire", NewTerm("fire"), false))

	// Rune regime ladder.
	require.Equal(t, TierExact, MatchTerm("M\u00fcsic", NewTerm("M\u00dcSIC"), true))
	require.Equal(t, TierPrefix, MatchTerm("\u00c4hnlich.txt", NewTerm("\u00e4hn"), true))
	require.Equal(t, TierWordStart, MatchTerm("nice \u00c4hnlich", NewTerm("\u00e4hn"), true))
	require.Equal(t, TierSubstring, MatchTerm("xx\u00c4hnlich", NewTerm("\u00e4hn"), true))
	require.Equal(t, TierFuzzy, MatchTerm("M\u00fcsic-S\u00f6ng", NewTerm("m\u00fcs\u00f6"), true))
	require.Equal(t, TierNone, MatchTerm("plain", NewTerm("\u00e4"), true))

	// Engine fold-semantics parity: an ASCII term never decodes stored
	// UTF-8 (U+0130 does not fold to ASCII 'i' on the byte path), while
	// a term carrying the rune matches both forms -- identical to the
	// file-index engine's pinned semantics.
	require.Equal(t, TierNone, MatchTerm("\u0130stanbul", NewTerm("istanbul"), true))
	require.Equal(t, TierExact, MatchTerm("\u0130stanbul", NewTerm("\u0130STANBUL"), true))
	require.Equal(t, TierPrefix, MatchTerm("kelvin.txt", NewTerm("\u212ael"), true))
}

func TestTierString(t *testing.T) {
	for tier, want := range map[Tier]string{
		TierTriggered: "triggered", TierExact: "exact", TierPrefix: "prefix",
		TierWordStart: "word-start", TierSubstring: "substring",
		TierFuzzy: "fuzzy", TierNone: "none",
	} {
		require.Equal(t, want, tier.String())
	}
}

// TestMatchFieldsFireFox is the user's repro pinned at the engine
// level: "fire fox" (and "fox fire", and the fuzzy "firefx") must
// match the single field "Firefox".
func TestMatchFieldsFireFox(t *testing.T) {
	fields := []string{"Firefox"}

	fr, ok := MatchFields(fields, Terms("fire fox"), true)
	require.True(t, ok, `"fire fox" must match Firefox`)
	require.Equal(t, TierSubstring, fr.Tier, "both terms substring-match: substring tier")
	require.Zero(t, fr.WorstField)

	fr, ok = MatchFields(fields, Terms("fox fire"), true)
	require.True(t, ok, "order-free")
	require.Equal(t, TierSubstring, fr.Tier)

	fr, ok = MatchFields(fields, Terms("firefx"), true)
	require.True(t, ok, "single-term fuzzy")
	require.Equal(t, TierFuzzy, fr.Tier)
	require.Positive(t, fr.Score)

	_, ok = MatchFields(fields, Terms("firefx"), false)
	require.False(t, ok, "fuzzy disabled: the subsequence-only term fails")

	fr, ok = MatchFields(fields, Terms("fire fox"), false)
	require.True(t, ok, "term-splitting works with fuzzy disabled (pure substring conjunction)")
	require.Equal(t, TierSubstring, fr.Tier)

	_, ok = MatchFields(fields, Terms("fire zebra"), true)
	require.False(t, ok, "ALL terms must match")
}

func TestMatchFieldsMultiField(t *testing.T) {
	// A term matches when it matches ANY field; the worst per-term
	// best-field index is reported.
	fields := []string{"dashboard", "firefox"}
	fr, ok := MatchFields(fields, Terms("fire dash"), true)
	require.True(t, ok, "terms may match different fields")
	require.Equal(t, TierPrefix, fr.Tier, "dash prefixes field 0, fire prefixes field 1")
	require.Equal(t, 1, fr.WorstField, "one term had to fall back to field 1")

	fr, ok = MatchFields(fields, Terms("dash"), true)
	require.True(t, ok)
	require.Zero(t, fr.WorstField)

	// Empty fields are skipped.
	fr, ok = MatchFields([]string{"", "firefox"}, Terms("fire"), true)
	require.True(t, ok)
	require.Equal(t, 1, fr.WorstField)

	_, ok = MatchFields(nil, Terms("fire"), true)
	require.False(t, ok)
	_, ok = MatchFields(fields, nil, true)
	require.False(t, ok)

	// Repeated terms: each must match (they can share occurrences).
	fr, ok = MatchFields([]string{"Firefox"}, Terms("fire fire"), true)
	require.True(t, ok)
	require.Equal(t, TierPrefix, fr.Tier)

	// Worst tier governs: one substring term + one fuzzy term = fuzzy.
	fr, ok = MatchFields([]string{"zqxr_marker"}, Terms("marker zqr"), true)
	require.True(t, ok)
	require.Equal(t, TierFuzzy, fr.Tier)

	// Unicode terms across fields.
	fr, ok = MatchFields([]string{"notes", "M\u00fcsic"}, Terms("m\u00dcsic notes"), true)
	require.True(t, ok)
	require.Equal(t, TierExact, fr.Tier)
	require.Equal(t, 1, fr.WorstField)
}

func TestNormalizeScore(t *testing.T) {
	require.Zero(t, NormalizeScore(-5, 3))
	require.Zero(t, NormalizeScore(10, 0))
	require.Equal(t, 1.0, NormalizeScore(1<<30, 2), "clamps at 1")
	mid := NormalizeScore(ScoreMatch*2, 2)
	require.Greater(t, mid, 0.0)
	require.Less(t, mid, 1.0)
	require.Greater(t, NormalizeScore(60, 2), NormalizeScore(40, 2), "monotone")
}
