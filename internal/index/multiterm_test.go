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

// naiveQueryMulti is the independent multi-term reference ladder:
// strings.Fields terms, per-term fold via foldPattern, stdlib-strings
// substring checks over testFold copies, naiveSubseq fallback, the
// documented class rule (all-substring = classSub, any subsequence-
// only = classFuzzy with the summed per-term score), and the standard
// comparator. Ranges are filled through the same fillNameRanges the
// engine uses (positions are pinned independently in internal/match).
func naiveQueryMulti(entries []refEntry, q string, limit int, fuzzyOff bool) []Result {
	fields := strings.Fields(q)
	type scored struct {
		refEntry
		class uint8
		score int32
	}
	var matches []scored
	for _, e := range entries {
		anyFuzzy := false
		ok := true
		var score int32
		for _, f := range fields {
			pat, ascii := foldPattern(f)
			qs := string(pat)
			folded := testFold(e.name, ascii)
			if strings.Contains(folded, qs) {
				continue
			}
			if !fuzzyOff && naiveSubseq(folded, qs, ascii) {
				anyFuzzy = true
				continue
			}
			ok = false
			break
		}
		if !ok {
			continue
		}
		class := classSub
		if anyFuzzy {
			class = classFuzzy
			for _, f := range fields {
				pat, ascii := foldPattern(f)
				score += fuzzyScoreFor(e.name, string(pat), ascii)
			}
		}
		matches = append(matches, scored{refEntry: e, class: class, score: score})
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
	fillNameRanges(out, match.Terms(q), !fuzzyOff)
	return out
}

// TestQueryMultiTermFireFox is the user's repro at the file-index
// level: "fire fox" (and "fox fire") finds firefox-named entries, and
// "firefx" fuzzy-matches them; "firefox"/"fire" keep their literal
// behavior.
func TestQueryMultiTermFireFox(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/apps", "firefox.desktop", false)
	mustAdd(t, s, "/apps", "chromium.desktop", false)
	mustAdd(t, s, "/docs", "campfire-guide.txt", false)

	for _, q := range []string{"fire fox", "fox fire", "FIRE FOX"} {
		res := s.Query(q, 10)
		require.Equal(t, []string{"/apps/firefox.desktop"}, pathsOf(res), "query %q", q)
		require.NotEmpty(t, res[0].MatchRanges, "query %q highlights", q)
	}
	require.Equal(t, []string{"/apps/firefox.desktop"}, pathsOf(s.Query("firefx", 10)))

	res := s.Query("fire", 10)
	require.Equal(t, []string{"/apps/firefox.desktop", "/docs/campfire-guide.txt"}, pathsOf(res),
		"single-term behavior unchanged (prefix before substring)")
}

// TestQueryMultiTermSpaceNamedFiles: names containing spaces are still
// found in name mode because each term matches on its own.
func TestQueryMultiTermSpaceNamedFiles(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/home", "My Documents Backup", true)
	mustAdd(t, s, "/home", "Backups Old", true)
	mustAdd(t, s, "/home", "notes.txt", false)

	res := s.Query("my backup", 10)
	require.Equal(t, []string{"/home/My Documents Backup"}, pathsOf(res))
	require.Equal(t, [][2]int{{0, 2}, {13, 19}}, res[0].MatchRanges)

	// The old literal-substring behavior for the exact spacing still
	// matches (the phrase's own words each match).
	require.Contains(t, pathsOf(s.Query("documents backup", 10)), "/home/My Documents Backup")
}

// TestQueryMultiTermClasses pins the class rule: all-substring =
// classSub (after every single-term substring result of either term
// alone would rank... exact/prefix never appear in multi-term), any
// subsequence-only term = classFuzzy below all classSub rows.
func TestQueryMultiTermClasses(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "data_report.txt", false)    // both substring
	mustAdd(t, s, "/a", "report_data_2.txt", false)  // both substring, order swapped
	mustAdd(t, s, "/a", "d_a_t_a_report.txt", false) // "data" subsequence-only
	mustAdd(t, s, "/a", "report_only.txt", false)    // "data" missing entirely
	mustAdd(t, s, "/a", "data_only.txt", false)      // "report" missing

	res := s.Query("data report", 20)
	require.Equal(t, []string{
		"/a/data_report.txt",
		"/a/report_data_2.txt",
		"/a/d_a_t_a_report.txt",
	}, pathsOf(res), "substring conjunction first, fuzzy conjunction after")

	// Fuzzy off: the subsequence-only row disappears, the rest is a
	// pure substring conjunction.
	off := s.QueryWith("data report", 20, QueryOptions{FuzzyDisabled: true})
	require.Equal(t, []string{"/a/data_report.txt", "/a/report_data_2.txt"}, pathsOf(off))
}

// TestQueryMultiTermWhitespaceShapes pins the term-splitting semantics
// at the engine edge: whitespace-only queries match nothing, padded
// single-term queries behave exactly like their trimmed term, and
// multi-space separators split like single spaces.
func TestQueryMultiTermWhitespaceShapes(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/w", "report.txt", false)
	mustAdd(t, s, "/w", "spa ce.txt", false)

	require.Nil(t, s.Query("   ", 10))
	require.Nil(t, s.Query(" \t ", 10))
	require.Equal(t, s.Query("report", 10), s.Query("  report  ", 10),
		"padded single-term queries are trimmed to their term")
	require.Equal(t, s.Query("spa ce", 10), s.Query("spa   ce", 10))
	require.Equal(t, []string{"/w/spa ce.txt"}, pathsOf(s.Query("spa ce", 10)),
		"each term matches the space-named file")
}

// TestQueryMultiTermPathModeStaysLiteral: a separator switches to path
// mode BEFORE term splitting, so spaces in path queries stay literal.
func TestQueryMultiTermPathModeStaysLiteral(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/p", "my dir", true)
	mustAdd(t, s, "/p/my dir", "file.txt", false)
	mustAdd(t, s, "/p", "other", true)
	mustAdd(t, s, "/p/other", "my file.txt", false)

	res := s.Query("my dir/file", 10)
	require.Equal(t, []string{"/p/my dir/file.txt"}, pathsOf(res),
		"the space is literal in path mode")
	require.Equal(t, [][2]int{{0, 4}}, res[0].MatchRanges,
		"the query's final segment highlights as the name prefix")
	require.Nil(t, pathsOf(s.Query("dir file/x", 10)), "no term semantics in path mode")
}

// TestQueryMultiTermMatchesNaiveReference cross-checks the engine
// against the independent reference over a randomized store: crafted
// all-substring, mixed substring/fuzzy, order-free, and no-match
// term sets, at full and small limits, fuzzy on AND off.
func TestQueryMultiTermMatchesNaiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	s := NewStore()
	var ref []refEntry
	parents := []string{"/m", "/m/sub", "/m/sub/deep", "/n"}
	for i := 0; i < 3000; i++ {
		addBoth(t, s, &ref, parents[rng.Intn(len(parents))], randomName(rng, i), rng.Intn(5) == 0)
	}
	// Space-carrying names for good measure.
	for i := 0; i < 60; i++ {
		addBoth(t, s, &ref, "/m", fmt.Sprintf("Report %s %d", []string{"Data", "Cache", "Alpha"}[i%3], i), false)
	}

	queries := []string{
		"data report", "report data", "alpha beta", "img png",
		"dta report", "rpt data", "data data", "zz 9",
		"report cache alpha", "qq zz", "data qqqq",
		"a b", "e t", "report Report",
	}
	for _, q := range queries {
		for _, off := range []bool{false, true} {
			want := naiveQueryMulti(ref, q, len(ref), off)
			got := s.QueryWith(q, len(ref), QueryOptions{FuzzyDisabled: off})
			require.Equal(t, want, got, "query %q fuzzyOff=%v (full)", q, off)
			requireUniquePaths(t, got, q)
			require.Equal(t, naiveQueryMulti(ref, q, 9, off),
				s.QueryWith(q, 9, QueryOptions{FuzzyDisabled: off}),
				"query %q fuzzyOff=%v (limit 9)", q, off)
		}
	}
}

// TestQueryMultiTermSkipRuleEdges drives the phase-B skip decision
// through its boundary: with exactly S all-substring conjunction hits,
// limits S-1 and S must equal the fuzzy-disabled engine exactly (the
// fuzzy sweep is skipped and could not contribute anyway), and S+1
// admits the best fuzzy conjunction row.
func TestQueryMultiTermSkipRuleEdges(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	const q = "cat dog"
	const subHits = 8
	for i := 0; i < subHits; i++ {
		addBoth(t, s, &ref, "/sub", fmt.Sprintf("cat_dog_%02d.txt", i), false)
	}
	// Fuzzy-conjunction names: "cat" subsequence-only, "dog" substring.
	for _, name := range []string{"crate_dog.md", "c_a_t_dog.bin", "combat_dog.log"} {
		addBoth(t, s, &ref, "/fuzz", name, false)
	}
	for i := 0; i < 5; i++ {
		addBoth(t, s, &ref, "/noise", fmt.Sprintf("beryl_%d.log", i), false)
	}

	for _, limit := range []int{subHits - 1, subHits, subHits + 1, subHits + 3, 50} {
		want := naiveQueryMulti(ref, q, limit, false)
		got := s.Query(q, limit)
		require.Equal(t, want, got, "limit %d", limit)
		requireUniquePaths(t, got, fmt.Sprintf("limit %d", limit))
		if limit <= subHits {
			require.Equal(t, s.QueryWith(q, limit, QueryOptions{FuzzyDisabled: true}), got,
				"limit %d: identical to the fuzzy-disabled engine", limit)
		} else {
			require.Greater(t, len(got), subHits, "limit %d runs the fuzzy sweep", limit)
		}
	}
}

// TestQueryMultiTermDedup: an entry matching the driver both ways or
// several terms in overlapping places appears exactly once.
func TestQueryMultiTermDedup(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/d", "fire_fox_firefox.txt", false)
	mustAdd(t, s, "/d", "firefox.txt", false)
	res := s.Query("fire fox", 10)
	requireUniquePaths(t, res, "fire fox")
	require.Len(t, res, 2)
	// Repeated terms are satisfied by the same text.
	res = s.Query("fire fire", 10)
	requireUniquePaths(t, res, "fire fire")
	require.Len(t, res, 2)
}

// TestQueryMultiTermImpossibleByte: a term with a byte the blob never
// contains proves the query empty on the fast path.
func TestQueryMultiTermImpossibleByte(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/x", "data_report.txt", false)
	require.Nil(t, s.Query("data reportq", 10), "'q' occurs nowhere")
	require.Nil(t, s.Query("dataq report", 10))
}

// TestQueryMultiTermNonASCII drives the mixed/rune-regime slow path
// against the reference.
func TestQueryMultiTermNonASCII(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	addBoth(t, s, &ref, "/u", "M\u00fcsic-S\u00f6ng.mp3", false)
	addBoth(t, s, &ref, "/u", "M\u00fcsic Notes.txt", false)
	addBoth(t, s, &ref, "/u", "\u00c4hnlich data.txt", false)
	addBoth(t, s, &ref, "/u", "plain data.txt", false)

	for _, q := range []string{
		"m\u00fcsic notes", "notes m\u00fcsic", "\u00e4hnlich data",
		"m\u00fcsc notes", "data \u00e4hnl", "s\u00f6ng m\u00fcsic",
		"data plain", "\u00e4 zz",
	} {
		for _, off := range []bool{false, true} {
			require.Equal(t, naiveQueryMulti(ref, q, len(ref), off),
				s.QueryWith(q, len(ref), QueryOptions{FuzzyDisabled: off}),
				"query %q fuzzyOff=%v", q, off)
		}
	}

	// Mixed regimes share the sharding machinery: push past one shard.
	big := buildSynthStore(21, minShardEntries*2+11)
	mustAdd(t, big, "/bench", "\u00c4hnlich_data_deep.txt", false)
	res := big.Query("\u00e4hnlich data", 10)
	require.Equal(t, []string{"/bench/\u00c4hnlich_data_deep.txt"}, pathsOf(res))
}

// TestQueryMultiTermParallelShards pushes multi-term past the shard
// threshold and requires naive equality (bitset word disjointness, the
// two-phase merge, and the driver scan must be invisible).
func TestQueryMultiTermParallelShards(t *testing.T) {
	st := buildSynthStore(23, 3*minShardEntries+123)
	var ref []refEntry
	st.ForEachLive(func(id int32) bool {
		ref = append(ref, refEntry{path: st.EntryPath(id), name: st.Name(id), isDir: st.IsDir(id)})
		return true
	})
	for _, q := range []string{"data report", "zzqx marker", "dta cache", "qq nomatch"} {
		for _, off := range []bool{false, true} {
			require.Equal(t, naiveQueryMulti(ref, q, len(ref), off),
				st.QueryWith(q, len(ref), QueryOptions{FuzzyDisabled: off}),
				"query %q fuzzyOff=%v (full)", q, off)
			require.Equal(t, naiveQueryMulti(ref, q, 50, off),
				st.QueryWith(q, 50, QueryOptions{FuzzyDisabled: off}),
				"query %q fuzzyOff=%v (limit 50)", q, off)
		}
	}
}

// TestQueryMultiTermTombstones: removed entries never surface through
// either phase.
func TestQueryMultiTermTombstones(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/w", "data_report.txt", false)
	mustAdd(t, s, "/w", "gone", true)
	mustAdd(t, s, "/w/gone", "data_report_2.txt", false)
	require.Len(t, s.Query("data report", 10), 2)
	s.RemoveByPath("/w/gone")
	require.Equal(t, []string{"/w/data_report.txt"}, pathsOf(s.Query("data report", 10)))
}

// TestNameRangesSingleTerm: single-term results carry per-character
// ranges too (prefix, substring, and fuzzy shapes).
func TestNameRangesSingleTerm(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/r", "report.txt", false)
	mustAdd(t, s, "/r", "my_report.txt", false)
	mustAdd(t, s, "/r", "foo_bar.txt", false)

	res := s.Query("report", 10)
	require.Equal(t, [][2]int{{0, 6}}, res[0].MatchRanges, "prefix ranges")
	require.Equal(t, [][2]int{{3, 9}}, res[1].MatchRanges, "substring ranges")

	res = s.Query("fb", 10)
	require.Equal(t, []string{"/r/foo_bar.txt"}, pathsOf(res))
	require.Equal(t, [][2]int{{0, 1}, {4, 5}}, res[0].MatchRanges, "fuzzy alignment ranges")

	// Fuzzy-off results still carry substring ranges.
	res = s.QueryWith("report", 10, QueryOptions{FuzzyDisabled: true})
	require.Equal(t, [][2]int{{0, 6}}, res[0].MatchRanges)
}
