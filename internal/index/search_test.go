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

// refEntry is the naive reference model of one indexed entry.
type refEntry struct {
	path  string
	name  string
	isDir bool
}

// refPathLess is the naive references' final tie-break: the
// numeric-aware lexicographic path order, re-implemented independently
// of numorder.go on plain strings. Aligned digit runs compare
// numerically DESCENDING (newest/highest first); numerically equal
// runs continue the walk; any other first difference keeps plain byte
// order; an all-equal walk falls back to plain string order.
func refPathLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if ca >= '0' && ca <= '9' && cb >= '0' && cb <= '9' {
			ia, jb := i, j
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			ra := strings.TrimLeft(a[ia:i], "0")
			rb := strings.TrimLeft(b[jb:j], "0")
			if len(ra) != len(rb) {
				return len(ra) > len(rb) // longer number = bigger = first
			}
			if ra != rb {
				return ra > rb // equal length: bigger digits first
			}
			continue
		}
		if ca != cb {
			return ca < cb
		}
		i++
		j++
	}
	if i < len(a) || j < len(b) {
		return j < len(b) // shorter first
	}
	return a < b // leading-zero twins: plain order keeps it total
}

// naiveQuery is an independent, obviously-correct implementation of the
// documented ranking, used to cross-check Store.Query. It fully sorts
// every match with the same total order (class, dir-first, path length,
// numeric-aware lexicographic path -- see refPathLess). Folding goes
// through foldPattern + testFold so
// engine and reference share one fold definition (fold.go's foldTable
// and foldRune) while the matching itself stays independent stdlib
// strings operations over folded copies.
func naiveQuery(entries []refEntry, q string, limit int) []Result {
	pat, ascii := foldPattern(q)
	ql := string(pat)
	type scored struct {
		refEntry
		class uint8
	}
	var matches []scored
	for _, e := range entries {
		lower := testFold(e.name, ascii)
		if !strings.Contains(lower, ql) {
			continue
		}
		class := classSub
		if strings.HasPrefix(lower, ql) {
			class = classPrefix
			if lower == ql {
				class = classExact
			}
		}
		matches = append(matches, scored{refEntry: e, class: class})
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.class != b.class {
			return a.class < b.class
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
		return nil // Store.Query returns nil for no matches
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Result, len(matches))
	for i, m := range matches {
		out[i] = Result{Path: m.path, Name: m.name, IsDir: m.isDir}
	}
	// Ranges through the same shared-engine helper the store uses:
	// positions are pinned independently in internal/match, so the
	// reference stays the authority on MATCHING and ORDERING only.
	fillNameRanges(out, match.Terms(q), false)
	return out
}

// addBoth adds an entry to the store and to the reference model.
func addBoth(t *testing.T, s *Store, ref *[]refEntry, parentDir, name string, isDir bool) {
	t.Helper()
	mustAdd(t, s, parentDir, name, isDir)
	*ref = append(*ref, refEntry{path: joinDir(parentDir, name), name: name, isDir: isDir})
}

func TestQueryEdgeCases(t *testing.T) {
	s := NewStore()
	require.Nil(t, s.Query("anything", 10), "empty store")

	buildSampleTree(t, s)
	require.Nil(t, s.Query("", 10), "empty query")
	require.Nil(t, s.Query("txt", 0), "zero limit")
	require.Nil(t, s.Query("txt", -3), "negative limit")
	require.Nil(t, s.Query("a\x00b", 10), "NUL byte in query")
	require.Nil(t, s.Query("no-such-name-here", 10), "no matches")
}

func TestQueryRankingOrder(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)        // exact, file, short path
	mustAdd(t, s, "/longer", "report", false)   // exact, file, longer path
	mustAdd(t, s, "/c", "report", false)        // exact, file, lex after /a
	mustAdd(t, s, "/b", "report", true)         // exact, dir
	mustAdd(t, s, "/a", "report2", true)        // prefix, dir
	mustAdd(t, s, "/a", "reports.txt", false)   // prefix, file
	mustAdd(t, s, "/a", "myreport.txt", false)  // substring, file
	mustAdd(t, s, "/a", "unrelated.txt", false) // no match

	got := s.Query("report", 10)
	var paths []string
	for _, r := range got {
		paths = append(paths, r.Path)
	}
	require.Equal(t, []string{
		"/b/report", // exact matches first, directories first
		"/a/report", // then exact files, shorter path, then lex
		"/c/report",
		"/longer/report",
		"/a/report2", // prefix class: dir before file
		"/a/reports.txt",
		"/a/myreport.txt", // plain substring last
	}, paths)

	// Case-insensitivity does not change the ranking class.
	upper := s.Query("REPORT", 10)
	require.Equal(t, got, upper)
}

func TestQueryLimit(t *testing.T) {
	s := NewStore()
	for i := 0; i < 25; i++ {
		mustAdd(t, s, "/lim", fmt.Sprintf("file%02d.txt", i), false)
	}
	res := s.Query("file", 7)
	require.Len(t, res, 7)
	// All same class/kind/length: the numeric-aware final tie-break
	// decides, and a numbered family delivers highest-first
	// (numorder.go).
	for i, r := range res {
		require.Equal(t, fmt.Sprintf("/lim/file%02d.txt", 24-i), r.Path)
	}
}

// randomName builds names mixing words, camelCase, digits, extensions,
// and case, with i embedded so every generated name is unique.
func randomName(rng *rand.Rand, i int) string {
	words := []string{"alpha", "beta", "Data", "report", "IMG", "cache", "Node", "test", "zz"}
	exts := []string{".txt", ".go", ".png", ".md", ""}
	w1 := words[rng.Intn(len(words))]
	w2 := words[rng.Intn(len(words))]
	ext := exts[rng.Intn(len(exts))]
	switch rng.Intn(3) {
	case 0:
		return fmt.Sprintf("%s_%s_%d%s", w1, w2, i, ext)
	case 1:
		return fmt.Sprintf("%s%s%d%s", w1, strings.ToUpper(w2[:1])+w2[1:], i, ext)
	default:
		return fmt.Sprintf("%s-%d%s", w1, i, ext)
	}
}

// TestQueryFuzzyDisabledMatchesNaiveReference is the toggle-off
// equivalence guard: with the fuzzy tier disabled the engine must be
// behavior-identical to the pre-fuzzy substring engine, modeled by the
// unchanged naiveQuery reference. (The fuzzy-enabled default is
// cross-checked against the extended reference in fuzzy_test.go.)
func TestQueryFuzzyDisabledMatchesNaiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	s := NewStore()
	var ref []refEntry
	parents := []string{"/r", "/r/sub", "/r/sub/deep", "/other", "/other/Very/Deep/Nested"}
	for i := 0; i < 3000; i++ {
		parent := parents[rng.Intn(len(parents))]
		addBoth(t, s, &ref, parent, randomName(rng, i), rng.Intn(5) == 0)
	}

	queries := []string{"a", "data", "DATA", "img", "report", "zz", "-1", ".txt", "alphabeta", "0", "e", "nomatchxyz"}
	// Add substrings sliced out of real names.
	for i := 0; i < 15; i++ {
		name := ref[rng.Intn(len(ref))].name
		lo := rng.Intn(len(name))
		hi := lo + 1 + rng.Intn(len(name)-lo)
		queries = append(queries, name[lo:hi])
	}

	off := QueryOptions{FuzzyDisabled: true}
	for _, q := range queries {
		if strings.IndexByte(q, 0) >= 0 {
			continue
		}
		want := naiveQuery(ref, q, len(ref))
		got := s.QueryWith(q, len(ref), off)
		require.Equal(t, want, got, "query %q (full result list)", q)

		wantTop := naiveQuery(ref, q, 9)
		gotTop := s.QueryWith(q, 9, off)
		require.Equal(t, wantTop, gotTop, "query %q (limit 9)", q)
	}
}

// TestQueryNonASCIISlowPath drives the rune slow path in name mode:
// non-ASCII queries against mixed ASCII/unicode names, cross-checked
// against the naive reference (which shares the fold definition).
func TestQueryNonASCIISlowPath(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	addBoth(t, s, &ref, "/u", "\u00c4hnlich.txt", false)
	addBoth(t, s, &ref, "/u", "\u00e4hnlich.txt", false)
	addBoth(t, s, &ref, "/u", "plain.txt", false)
	addBoth(t, s, &ref, "/u", "M\u00fcsic", true)
	addBoth(t, s, &ref, "/u/M\u00fcsic", "S\u00f6ng.mp3", false)
	addBoth(t, s, &ref, "/u", "Stra\u1e9eE", false)

	// Prefix match on a folded first rune, original casing returned.
	res := s.Query("\u00e4hnl", 10)
	require.Equal(t, []string{"/u/\u00c4hnlich.txt", "/u/\u00e4hnlich.txt"}, pathsOf(res))

	for _, q := range []string{
		"\u00e4hnl", "\u00c4HNLICH.TXT", "m\u00fcsic", "s\u00f6ng",
		"stra\u00dfe", "Stra\u1e9eE", "\u00f6", "\u00e4hnlich.txt", "nix\u00e4",
	} {
		require.Equal(t, naiveQueryFuzzy(ref, q, len(ref)), s.Query(q, len(ref)),
			"non-ASCII query %q", q)
	}

	// The slow path shares the sharding machinery: push past one shard.
	big := buildSynthStore(13, minShardEntries*2+7)
	mustAdd(t, big, "/bench", "\u00c4hnlich_deep.txt", false)
	res = big.Query("\u00e4hnlich_de", 10)
	require.Equal(t, []string{"/bench/\u00c4hnlich_deep.txt"}, pathsOf(res))
}

func TestQueryTombstonesSkipped(t *testing.T) {
	s := NewStore()
	buildSampleTree(t, s)
	require.Len(t, s.Query("txt", 10), 3)

	s.RemoveByPath("/root/docs")
	res := s.Query("txt", 10)
	require.Len(t, res, 1)
	require.Equal(t, "/root/top.txt", res[0].Path)

	// Everything gone: nil result.
	s.RemoveByPath("/root")
	require.Nil(t, s.Query("txt", 10))
}

// TestQueryParallelShardsMatchLinearScan pushes the store well past the
// single-shard threshold and cross-checks the parallel result set
// against a trivial linear scan over the fold reference. Fuzzy is
// disabled here so the reference stays a pure substring scan; the
// fuzzy-enabled shard consistency lives in fuzzy_test.go.
func TestQueryParallelShardsMatchLinearScan(t *testing.T) {
	st := buildSynthStore(7, 3*minShardEntries+123)
	for _, q := range []string{"data", "zzqx", "a", "_1", "qqnomatch"} {
		pat, ascii := foldPattern(q)
		qs := string(pat)
		want := make(map[string]bool)
		st.ForEachLive(func(id int32) bool {
			if strings.Contains(testFold(st.Name(id), ascii), qs) {
				want[st.EntryPath(id)] = true
			}
			return true
		})
		res := st.QueryWith(q, st.Len(), QueryOptions{FuzzyDisabled: true})
		got := make(map[string]bool, len(res))
		for _, r := range res {
			got[r.Path] = true
		}
		require.Equal(t, len(want), len(got), "query %q hit count", q)
		require.Equal(t, want, got, "query %q hit set", q)
	}
}

func TestCompareJoined(t *testing.T) {
	cases := []struct {
		da, na, db, nb string
		want           int
	}{
		{"/a", "x", "/a", "x", 0},
		{"/a", "x", "/a", "y", -1},
		{"/a", "y", "/a", "x", 1},
		{"/a", "x", "/a/x", "y", -1}, // "/a/x" is a prefix of "/a/x/y": shorter first
		{"/a", "x", "/a/b", "x", 1},  // pure lexicographic: "/a/x" > "/a/b/x" at byte 3
		{"/", "x", "/x", "y", -1},    // root dir: "/x" vs "/x/y", no doubled separator
		{"/ab", "c", "/a", "bc", 1},  // "/ab/c" > "/a/bc" at byte 2
	}
	for _, tc := range cases {
		got := compareJoined(tc.da, []byte(tc.na), tc.db, []byte(tc.nb))
		require.Equal(t, tc.want, sign(got), "compareJoined(%q,%q vs %q,%q)", tc.da, tc.na, tc.db, tc.nb)
	}
}

func sign(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	default:
		return 0
	}
}
