package index

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// naiveSubseq is the reference subsequence check, INDEPENDENT of the
// engine's walk: it operates on the pre-folded name (testFold, the
// shared fold definition) with plain index walks -- bytes in the ASCII
// regime, runes otherwise.
func naiveSubseq(foldedName, pat string, ascii bool) bool {
	if ascii {
		j := 0
		for i := 0; i < len(foldedName) && j < len(pat); i++ {
			if foldedName[i] == pat[j] {
				j++
			}
		}
		return j == len(pat)
	}
	hay := []rune(foldedName)
	need := []rune(pat)
	j := 0
	for i := 0; i < len(hay) && j < len(need); i++ {
		if hay[i] == need[j] {
			j++
		}
	}
	return j == len(need)
}

// fuzzyScoreFor is the one-shot test entry into the engine's scoring
// (prepare + align) for one name/pattern pair in the given regime. The
// naive reference model calls it so engine and reference share ONE
// scoring definition while keeping matching and ordering independent.
func fuzzyScoreFor(name, qs string, ascii bool) int32 {
	sc := fuzzyScratchGet()
	defer fuzzyScratchPut(sc)
	sc.pat = fuzzyPatternUnits(qs, ascii)
	if ascii {
		return sc.scoreASCII([]byte(name))
	}
	return sc.scoreFold([]byte(name))
}

// naiveQueryFuzzy extends the naiveQuery reference with the fuzzy
// tier: substring matches keep their exact classes, everything else
// that holds the query as a subsequence lands in classFuzzy, ordered
// by score descending then the usual tie-breaks -- the documented
// comparator, fully re-implemented on plain sorted slices.
func naiveQueryFuzzy(entries []refEntry, q string, limit int) []Result {
	pat, ascii := foldPattern(q)
	qs := string(pat)
	type scored struct {
		refEntry
		class uint8
		score int32
	}
	var matches []scored
	for _, e := range entries {
		folded := testFold(e.name, ascii)
		if strings.Contains(folded, qs) {
			class := classSub
			if strings.HasPrefix(folded, qs) {
				class = classPrefix
				if folded == qs {
					class = classExact
				}
			}
			matches = append(matches, scored{refEntry: e, class: class})
			continue
		}
		if !naiveSubseq(folded, qs, ascii) {
			continue
		}
		matches = append(matches, scored{
			refEntry: e, class: classFuzzy, score: fuzzyScoreFor(e.name, qs, ascii),
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.class != b.class {
			return a.class < b.class
		}
		if a.class == classFuzzy && a.score != b.score {
			return a.score > b.score
		}
		if a.isDir != b.isDir {
			return a.isDir
		}
		if len(a.path) != len(b.path) {
			return len(a.path) < len(b.path)
		}
		return refPathLess(a.path, b.path)
	})
	if len(matches) == 0 {
		return nil
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Result, len(matches))
	for i, m := range matches {
		out[i] = Result{Path: m.path, Name: m.name, IsDir: m.isDir}
	}
	fillNameRanges(out, match.Terms(q), true) // see naiveQuery's note
	return out
}

// requireUniquePaths pins the dedup guarantee: an entry matching both
// as a substring and as a subsequence appears exactly once.
func requireUniquePaths(t *testing.T, res []Result, label string) {
	t.Helper()
	seen := make(map[string]bool, len(res))
	for _, r := range res {
		require.False(t, seen[r.Path], "%s: duplicate result %q", label, r.Path)
		seen[r.Path] = true
	}
}

func TestFuzzySubseq(t *testing.T) {
	ascii := []struct {
		name, q string
		want    bool
	}{
		{"foo_bar", "fb", true},
		{"foo_bar", "fob", true},
		{"foo_bar", "bf", false}, // order matters
		{"FOO_BAR", "fb", true},  // case folds
		{"foo", "foo", true},     // equality is a subsequence too
		{"fo", "foo", false},     // name shorter than the pattern
		{"", "a", false},
		{"anything", "", true}, // degenerate; the engine never sends it
		{"a-b.c", "abc", true},
		{"xyz", "q", false},
		{"report_12.txt", "rpt2x", true},
		{"report_12.txt", "rpt3", false},
	}
	for _, tc := range ascii {
		pat, isASCII := foldPattern(tc.q)
		require.True(t, isASCII, "query %q must take the ASCII regime", tc.q)
		qs := string(pat)
		require.Equal(t, tc.want, fuzzySubseqASCII([]byte(tc.name), qs),
			"fuzzySubseqASCII(%q, %q)", tc.name, tc.q)
		require.Equal(t, tc.want, fuzzySubseqFold([]byte(tc.name), fuzzyPatternUnits(qs, false)),
			"rune-walk parity, fuzzySubseqFold(%q, %q)", tc.name, tc.q)
	}

	runes := []struct {
		name, q string
		want    bool
	}{
		{"M\u00fcsic-S\u00f6ng", "m\u00fcs\u00f6", true},
		{"M\u00fcsic", "\u00f6m", false},
		{"stra\u00dfe", "s\u00dfe", true},
		{"Stra\u1e9eE", "s\u00dfe", true},  // U+1E9E folds to U+00DF
		{"kelvin", "\u212ael", true},       // U+212A folds to 'k'
		{"a\xffb", "a\ufffdb", true},       // invalid UTF-8 compares as U+FFFD
		{"\u00c4hnlich", "\u00e4nl", true}, // diacritic fold + gaps
		{"\u00c4hnlich", "\u00e4nlz", false},
	}
	for _, tc := range runes {
		pat, isASCII := foldPattern(tc.q)
		require.False(t, isASCII, "query %q must take the rune regime", tc.q)
		require.Equal(t, tc.want, fuzzySubseqFold([]byte(tc.name), fuzzyPatternUnits(string(pat), false)),
			"fuzzySubseqFold(%q, %q)", tc.name, tc.q)
	}
}

// TestFuzzyScoreOrderingPins pins score ORDERINGS (never absolute
// values): which structural match beats which for the same query.
func TestFuzzyScoreOrderingPins(t *testing.T) {
	score := func(name, q string) int32 {
		pat, ascii := foldPattern(q)
		require.True(t, ascii)
		return fuzzyScoreFor(name, string(pat), true)
	}

	// Separator word boundary > camelCase step > unstructured scatter.
	require.Greater(t, score("foo_bar", "fb"), score("FooBar", "fb"))
	require.Greater(t, score("FooBar", "fb"), score("fxxbyyy", "fb"))
	// A match at the name start beats a later first match.
	require.Greater(t, score("fxb", "fb"), score("xfxb", "fb"))
	// A consecutive run beats the same units with a gap.
	require.Greater(t, score("xabx", "ab"), score("xa_bx", "ab"))
	require.Greater(t, score("ab_c", "abc"), score("a_b_c", "abc"))
	// A letter<->digit transition counts as a word boundary.
	require.Greater(t, score("x1ab", "ab"), score("xxab", "ab"))
	// Gap penalties are capped: past the cap, longer gaps tie...
	long := "a" + strings.Repeat("x", 40) + "b"
	longer := "a" + strings.Repeat("x", 200) + "b"
	require.Equal(t, score(long, "ab"), score(longer, "ab"))
	// ...but a small gap still beats a capped one.
	require.Greater(t, score("axxb", "ab"), score(long, "ab"))

	// The DP finds the optimal alignment where the greedy leftmost
	// walk does not (matching 'a' at the start looks best until the
	// consecutive "ab" run later in the name).
	sc := fuzzyScratchGet()
	defer fuzzyScratchPut(sc)
	pat, _ := foldPattern("ab")
	sc.pat = fuzzyPatternUnits(string(pat), true)
	dp := sc.scoreASCII([]byte("a_xxxxab"))
	greedy := fuzzyAlignGreedy(sc.pat, sc.units, sc.bonus)
	require.Greater(t, dp, greedy, "DP must beat the greedy alignment here")

	// Names past the DP bound take the greedy fallback.
	huge := "a" + strings.Repeat("x", fuzzyMaxDPUnits) + "ab"
	hugeScore := sc.scoreASCII([]byte(huge))
	require.Equal(t, fuzzyAlignGreedy(sc.pat, sc.units, sc.bonus), hugeScore,
		"past fuzzyMaxDPUnits the score IS the greedy score")
}

// TestFuzzyScoreRegimeParityOnASCII pins that the byte-regime and
// rune-regime scorers agree exactly on pure-ASCII input.
func TestFuzzyScoreRegimeParityOnASCII(t *testing.T) {
	cases := [][2]string{
		{"foo_bar.txt", "fb"},
		{"FooBar", "fob"},
		{"data_report_12.txt", "drp1"},
		{"a_xxxxab", "ab"},
		{"Alpha-Beta.9", "ab9"},
		{"x1ab", "ab"},
		{"UPPER_lower.MIX", "ulm"},
	}
	for _, c := range cases {
		name, q := c[0], c[1]
		pat, ascii := foldPattern(q)
		require.True(t, ascii)
		qs := string(pat)
		require.Equal(t, fuzzyScoreFor(name, qs, true), fuzzyScoreFor(name, qs, false),
			"score parity, name %q query %q", name, q)
	}
}

// TestQueryFuzzyRanking pins the tier order on a deterministic store:
// exact, prefix, substring always first, then fuzzy by score, with the
// standard tie-breaks inside equal scores.
func TestQueryFuzzyRanking(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "fb", false)          // exact
	mustAdd(t, s, "/a", "fbx.txt", false)     // prefix
	mustAdd(t, s, "/a", "xfb.txt", false)     // substring
	mustAdd(t, s, "/a", "foo_bar.txt", false) // fuzzy, boundary bonus
	mustAdd(t, s, "/a", "FooBar.txt", false)  // fuzzy, camel bonus
	mustAdd(t, s, "/a", "fxxbyy.txt", false)  // fuzzy, no bonus
	mustAdd(t, s, "/a", "nothing.txt", false) // no match
	// Equal fuzzy scores: the dir ranks before the file.
	mustAdd(t, s, "/a", "fzzb", true)
	mustAdd(t, s, "/a", "fyyb", false)

	got := pathsOf(s.Query("fb", 20))
	require.Equal(t, []string{
		"/a/fb",
		"/a/fbx.txt",
		"/a/xfb.txt",
		"/a/foo_bar.txt",
		"/a/FooBar.txt",
		"/a/fzzb", // ties on score: dir first
		"/a/fyyb",
		"/a/fxxbyy.txt",
	}, got)
	requireUniquePaths(t, s.Query("fb", 20), "fb")

	// The substring tiers are byte-identical with fuzzy disabled.
	require.Equal(t, []string{"/a/fb", "/a/fbx.txt", "/a/xfb.txt"},
		pathsOf(s.QueryWith("fb", 20, QueryOptions{FuzzyDisabled: true})))
}

// TestQueryFuzzySingleUnitAddsNothing: a one-unit subsequence IS a
// substring, so single-character queries gain nothing from the fuzzy
// tier in either regime.
func TestQueryFuzzySingleUnitAddsNothing(t *testing.T) {
	st := buildSynthStore(3, 5000)
	for _, q := range []string{"a", "z", "0", "."} {
		require.Equal(t,
			st.QueryWith(q, st.Len(), QueryOptions{FuzzyDisabled: true}),
			st.Query(q, st.Len()),
			"single-unit query %q", q)
	}

	s := NewStore()
	mustAdd(t, s, "/u", "M\u00fcsic", false)
	mustAdd(t, s, "/u", "plain", false)
	require.Equal(t,
		s.QueryWith("\u00fc", 10, QueryOptions{FuzzyDisabled: true}),
		s.Query("\u00fc", 10),
		"single-rune query")
}

// TestQueryFuzzySkipRule drives the phase-2 skip decision through its
// edges: with exactly S substring hits, limits S-1 and S skip phase 2
// (and must equal the disabled engine exactly), limit S+1 runs it and
// appends the best fuzzy hit. Every limit is cross-checked against the
// naive reference.
func TestQueryFuzzySkipRule(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	const q = "cat"
	const subHits = 10
	for i := 0; i < subHits; i++ {
		addBoth(t, s, &ref, "/sub", fmt.Sprintf("cat_%02d.txt", i), false)
	}
	// Fuzzy-only names: subsequence yes, substring no.
	for _, name := range []string{"chart.md", "c_a_t.bin", "crate.txt", "combat.log"} {
		addBoth(t, s, &ref, "/fuzz", name, false)
	}
	for i := 0; i < 5; i++ {
		addBoth(t, s, &ref, "/noise", fmt.Sprintf("beryl_%d.log", i), false)
	}

	for _, limit := range []int{subHits - 1, subHits, subHits + 1, subHits + 3, 50} {
		want := naiveQueryFuzzy(ref, q, limit)
		got := s.Query(q, limit)
		require.Equal(t, want, got, "limit %d", limit)
		requireUniquePaths(t, got, fmt.Sprintf("limit %d", limit))
		if limit <= subHits {
			require.Equal(t, s.QueryWith(q, limit, QueryOptions{FuzzyDisabled: true}), got,
				"limit %d skips phase 2: identical to the disabled engine", limit)
		} else {
			require.Greater(t, len(got), subHits, "limit %d runs phase 2", limit)
		}
	}
	// At limit subHits+1 the extra row is a fuzzy hit, after every
	// substring hit.
	got := s.Query(q, subHits+1)
	require.Len(t, got, subHits+1)
	require.Equal(t, "/fuzz", got[subHits].Path[:5])
}

// TestQueryFuzzyMatchesNaiveReference cross-checks the fuzzy-enabled
// engine against the extended naive model over a random seeded store:
// zero-substring queries, mixed tiers, separators, and random
// subsequences sliced from real names.
func TestQueryFuzzyMatchesNaiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	s := NewStore()
	var ref []refEntry
	parents := []string{"/f", "/f/sub", "/f/sub/deep", "/g", "/g/Very/Deep"}
	for i := 0; i < 3000; i++ {
		addBoth(t, s, &ref, parents[rng.Intn(len(parents))], randomName(rng, i), rng.Intn(5) == 0)
	}

	queries := []string{
		"dtrp", "rpt", "alp", "aebt", "IMGpng", "data", "zz9",
		"qqq", "a_1", "..", "cche", "NdeT",
	}
	// Random subsequences sampled from real names.
	for i := 0; i < 15; i++ {
		name := ref[rng.Intn(len(ref))].name
		var b []byte
		for j := 0; j < len(name) && len(b) < 4; j++ {
			if rng.Intn(3) == 0 {
				b = append(b, name[j])
			}
		}
		if len(b) >= 2 {
			queries = append(queries, string(b))
		}
	}

	for _, q := range queries {
		want := naiveQueryFuzzy(ref, q, len(ref))
		got := s.Query(q, len(ref))
		require.Equal(t, want, got, "query %q (full list)", q)
		requireUniquePaths(t, got, q)
		require.Equal(t, naiveQueryFuzzy(ref, q, 9), s.Query(q, 9), "query %q (limit 9)", q)
	}
}

// TestQueryFuzzyParallelShardsMatchNaive pushes the store past the
// sharding threshold with fuzzy on and requires the full ordered list
// to match the naive reference -- shard splits, the shared bitset, and
// the two-phase merge must be invisible.
func TestQueryFuzzyParallelShardsMatchNaive(t *testing.T) {
	st := buildSynthStore(19, 3*minShardEntries+123)
	var ref []refEntry
	st.ForEachLive(func(id int32) bool {
		ref = append(ref, refEntry{path: st.EntryPath(id), name: st.Name(id), isDir: st.IsDir(id)})
		return true
	})
	for _, q := range []string{"zzx", "dta", "rprt", "cnfg", "qqnomatch"} {
		require.Equal(t, naiveQueryFuzzy(ref, q, len(ref)), st.Query(q, len(ref)),
			"query %q (full list)", q)
		require.Equal(t, naiveQueryFuzzy(ref, q, 50), st.Query(q, 50),
			"query %q (limit 50)", q)
	}
}

// TestQueryFuzzyTombstonesSkipped: removed entries never surface
// through phase 2.
func TestQueryFuzzyTombstonesSkipped(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/w", "foo_bar.txt", false)
	mustAdd(t, s, "/w", "gone", true)
	mustAdd(t, s, "/w/gone", "faraway_bin.dat", false)
	require.Len(t, s.Query("fb", 10), 2, "both fuzzy hits before the remove")

	s.RemoveByPath("/w/gone")
	res := s.Query("fb", 10)
	require.Equal(t, []string{"/w/foo_bar.txt"}, pathsOf(res))
}

// TestQueryFuzzyNonASCII drives the rune slow path: non-ASCII queries
// whose subsequence hits span diacritics and mixed case, cross-checked
// against the naive reference.
func TestQueryFuzzyNonASCII(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	addBoth(t, s, &ref, "/u", "M\u00fcsic-S\u00f6ng.mp3", false)
	addBoth(t, s, &ref, "/u", "M\u00fcsic", true)
	addBoth(t, s, &ref, "/u", "\u00c4hnlich_Notes.txt", false)
	addBoth(t, s, &ref, "/u", "Stra\u1e9eE_data.txt", false)
	addBoth(t, s, &ref, "/u", "plain_data.txt", false)
	addBoth(t, s, &ref, "/u", "\u00e4_b\u00e4_c\u00e4.log", false)

	// A fuzzy-only non-ASCII hit: m..u-umlaut..s..o-umlaut.
	res := s.Query("m\u00fcs\u00f6", 10)
	require.Equal(t, []string{"/u/M\u00fcsic-S\u00f6ng.mp3"}, pathsOf(res))

	for _, q := range []string{
		"m\u00fcs\u00f6", "\u00e4nl", "\u00e4hlich", "s\u00dfd", "\u00e4c\u00e4",
		"M\u00dcSNG", "stra\u00dfe", "\u00e4\u00e4\u00e4", "\u00e4\u00e4\u00e4\u00e4",
		"\u00c4hnlich_Notes.txt",
	} {
		require.Equal(t, naiveQueryFuzzy(ref, q, len(ref)), s.Query(q, len(ref)),
			"non-ASCII query %q", q)
	}

	// The rune fuzzy path shares the sharding machinery: push past one
	// shard and find a planted fuzzy-only match.
	big := buildSynthStore(13, minShardEntries*2+7)
	mustAdd(t, big, "/bench", "\u00c4hn_lich_deep.txt", false)
	res = big.Query("\u00e4hnlichdeep", 10)
	require.Equal(t, []string{"/bench/\u00c4hn_lich_deep.txt"}, pathsOf(res))
}

// TestManagerFuzzyOption wires the Manager-level toggle.
func TestManagerFuzzyOption(t *testing.T) {
	m := NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/w", "foo_bar.txt", false))
	require.Len(t, m.Query("fb", 10), 1, "fuzzy on by default")
	m.SetFuzzyDisabled(true)
	require.Empty(t, m.Query("fb", 10), "disabled: subsequence-only match gone")
	m.SetFuzzyDisabled(false)
	require.Len(t, m.Query("fb", 10), 1)
}
