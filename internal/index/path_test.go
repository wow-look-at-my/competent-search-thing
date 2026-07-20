package index

import (
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pathsOf projects results to their Path fields.
func pathsOf(results []Result) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.Path)
	}
	return out
}

// naivePathQuery is the independent, obviously-correct reference for
// path-mode queries: fold the full path (via testFold, sharing the
// fold definition with the engine), classify with plain strings
// operations (exact, then suffix, then prefix, then substring), sort
// with the documented total order, truncate.
func naivePathQuery(entries []refEntry, q string, limit int) []Result {
	pat, ascii := foldPattern(q)
	ql := string(pat)
	type scored struct {
		refEntry
		class uint8
	}
	var matches []scored
	for _, e := range entries {
		lower := testFold(e.path, ascii)
		if !strings.Contains(lower, ql) {
			continue
		}
		class := classPathSub
		switch {
		case lower == ql:
			class = classPathExact
		case strings.HasSuffix(lower, ql):
			class = classPathSuffix
		case strings.HasPrefix(lower, ql):
			class = classPathPrefix
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
		return nil
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Result, len(matches))
	for i, m := range matches {
		out[i] = Result{Path: m.path, Name: m.name, IsDir: m.isDir}
	}
	// Ranges through the same engine helper the store uses, on the
	// separator-normalized pattern (pat from the top of the function;
	// ql was materialized before this mutation).
	for i, b := range pat {
		if b == '/' {
			pat[i] = sepByte
		}
	}
	fillPathRanges(out, string(pat), ascii)
	return out
}

// buildPathTree fills s (and ref) with the /etc-flavoured fixture used
// by the explicit path-mode expectations.
func buildPathTree(t *testing.T, s *Store, ref *[]refEntry) {
	t.Helper()
	addBoth(t, s, ref, "/", "etc", true)
	addBoth(t, s, ref, "/etc", "hosts", false)
	addBoth(t, s, ref, "/etc", "hostname", false)
	addBoth(t, s, ref, "/etc", "hosts.d", true)
	addBoth(t, s, ref, "/etc/hosts.d", "extra", false)
	addBoth(t, s, ref, "/", "backup", true)
	addBoth(t, s, ref, "/backup", "etc", true)
	addBoth(t, s, ref, "/backup/etc", "hosts", false)
	addBoth(t, s, ref, "/", "var", true)
	addBoth(t, s, ref, "/var", "etcetera", true)
	addBoth(t, s, ref, "/var/etcetera", "hosts", false)
	addBoth(t, s, ref, "/", "old", true)
	addBoth(t, s, ref, "/old", "etc", true)
	addBoth(t, s, ref, "/old/etc", "hosts.bak", false)
}

func TestPathQueryExplicitTree(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	buildPathTree(t, s, &ref)

	cases := []struct {
		q    string
		want []string
	}{
		{
			// Exact path first, then path-suffix, then path-prefix
			// (dir before file), then plain substring.
			q: "/etc/hosts",
			want: []string{
				"/etc/hosts",
				"/backup/etc/hosts",
				"/etc/hosts.d",
				"/etc/hosts.d/extra",
				"/old/etc/hosts.bak",
			},
		},
		{
			// Boundary substring: /var/etcetera/hosts must NOT match
			// ("etc" only occurs without a following separator there).
			q: "etc/ho",
			want: []string{
				"/etc/hosts.d",
				"/etc/hosts",
				"/etc/hostname",
				"/backup/etc/hosts",
				"/etc/hosts.d/extra",
				"/old/etc/hosts.bak",
			},
		},
		{
			// Trailing separator: descendants of any *etc dir at any
			// depth, but never the etc dirs themselves.
			q: "etc/",
			want: []string{
				"/etc/hosts.d",
				"/etc/hosts",
				"/etc/hostname",
				"/backup/etc/hosts",
				"/etc/hosts.d/extra",
				"/old/etc/hosts.bak",
			},
		},
		{
			// Root child: the dir entry /etc itself is the exact match;
			// then suffix dirs, prefix descendants, plain substrings.
			q: "/etc",
			want: []string{
				"/etc",
				"/old/etc",
				"/backup/etc",
				"/etc/hosts.d",
				"/etc/hosts",
				"/etc/hostname",
				"/etc/hosts.d/extra",
				"/var/etcetera",
				"/backup/etc/hosts",
				"/old/etc/hosts.bak",
				"/var/etcetera/hosts",
			},
		},
		{
			// Straddle starting mid-component.
			q: "tc/hos",
			want: []string{
				"/etc/hosts.d",
				"/etc/hosts",
				"/etc/hostname",
				"/backup/etc/hosts",
				"/etc/hosts.d/extra",
				"/old/etc/hosts.bak",
			},
		},
		{
			// "/" matches every live entry: all are path-prefix matches,
			// directories first, shorter paths first, then lexicographic.
			q: "/",
			want: []string{
				"/etc", "/old", "/var", "/backup",
				"/old/etc", "/backup/etc", "/etc/hosts.d", "/var/etcetera",
				"/etc/hosts", "/etc/hostname", "/backup/etc/hosts",
				"/etc/hosts.d/extra", "/old/etc/hosts.bak",
				"/var/etcetera/hosts",
			},
		},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, pathsOf(s.Query(tc.q, len(ref))), "query %q", tc.q)
		// The naive reference must agree with every explicit table.
		require.Equal(t, tc.want, pathsOf(naivePathQuery(ref, tc.q, len(ref))),
			"naive reference disagrees with table for %q", tc.q)
	}

	// Case-insensitivity: identical results and ranking.
	require.Equal(t, s.Query("etc/ho", 20), s.Query("ETC/HO", 20))
	require.Equal(t, s.Query("/etc/hosts", 20), s.Query("/Etc/HOSTS", 20))

	// Limits are respected ("/" ranks dirs by path length, then lex).
	require.Equal(t, []string{"/etc", "/old", "/var"}, pathsOf(s.Query("/", 3)))
}

func TestPathQueryEdgeCases(t *testing.T) {
	s := NewStore()
	require.Nil(t, s.Query("/etc", 10), "empty store")

	var ref []refEntry
	buildPathTree(t, s, &ref)
	require.Nil(t, s.Query("//", 10), "doubled separator matches nothing")
	require.Nil(t, s.Query("etc/nosuch", 10), "no such boundary remainder")
	require.Nil(t, s.Query("zz/qq", 10), "no matches at all")
	require.Nil(t, s.Query("a\x00/b", 10), "NUL byte in pathful query")
	require.Nil(t, s.Query(strings.Repeat("x/", 600), 10), "over the length cap")
	require.Nil(t, s.Query("/etc", 0), "zero limit")
	require.Nil(t, s.Query("/etc", -1), "negative limit")
}

// TestPathQueryNameModeUnchanged pins the dispatch rule: queries
// without a separator keep the name-only semantics, byte for byte.
func TestPathQueryNameModeUnchanged(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	buildPathTree(t, s, &ref)

	// Name mode ranks by name class: the three exact "etc" dirs by path
	// length then lex, then the name-prefix match "etcetera".
	require.Equal(t, []string{"/etc", "/old/etc", "/backup/etc", "/var/etcetera"},
		pathsOf(s.Query("etc", 20)))

	for _, q := range []string{"etc", "hosts", "HOSTS", "e", "ho", "nomatchxyz"} {
		require.Equal(t, naiveQuery(ref, q, len(ref)), s.Query(q, len(ref)),
			"name-mode query %q", q)
	}
}

func TestPathQueryMultiSepStraddle(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/", "usr", true)
	mustAdd(t, s, "/usr", "share", true)
	mustAdd(t, s, "/usr/share", "doc", true)
	mustAdd(t, s, "/usr/share/doc", "readme", false)
	mustAdd(t, s, "/usr/share", "docker", true)
	mustAdd(t, s, "/usr/share/docker", "readme", false)

	// The query spans two directory levels plus a name prefix; the
	// near-miss under docker/ must not match.
	require.Equal(t, []string{"/usr/share/doc/readme"},
		pathsOf(s.Query("share/doc/read", 10)))

	// Suffix vs substring across multiple levels.
	require.Equal(t, []string{
		"/usr/share/doc",
		"/usr/share/docker",
		"/usr/share/doc/readme",
		"/usr/share/docker/readme",
	}, pathsOf(s.Query("usr/share/doc", 10)))

	// Full-path exact including every level.
	require.Equal(t, []string{"/usr/share/doc/readme"},
		pathsOf(s.Query("/usr/share/doc/readme", 10)))
}

func TestPathQueryMixedCaseAndUnicode(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	addBoth(t, s, &ref, "/", "Users", true)
	addBoth(t, s, &ref, "/Users", "Alice", true)
	addBoth(t, s, &ref, "/Users/Alice", "Notes.txt", false)
	addBoth(t, s, &ref, "/", "M\u00fcsic", true)
	addBoth(t, s, &ref, "/M\u00fcsic", "S\u00f6ng.mp3", false)

	// Mixed-case directories match case-insensitively; results carry
	// the original casing.
	require.Equal(t, []string{"/Users/Alice", "/Users/Alice/Notes.txt"},
		pathsOf(s.Query("users/alice", 10)))
	require.Equal(t, []string{"/Users/Alice", "/Users/Alice/Notes.txt"},
		pathsOf(s.Query("/USERS/ALICE", 10)))

	// Unicode folding parity: the engine's per-component scan-time
	// folding (fold.go rune path) must agree with the naive model's
	// whole-path fold.
	unicodeQueries := []string{
		"m\u00fcsic/s\u00f6",
		"/m\u00fcsic",
		"M\u00dcSIC/S\u00d6NG.MP3",
		"m\u00fcsic/x",
		"\u00fcsic/s",
	}
	for _, q := range unicodeQueries {
		require.Equal(t, naivePathQuery(ref, q, len(ref)), s.Query(q, len(ref)),
			"unicode query %q", q)
	}
	require.Equal(t, []string{"/M\u00fcsic/S\u00f6ng.mp3"},
		pathsOf(s.Query("m\u00fcsic/s\u00f6", 10)))
}

// TestPathQueryFoldSemantics pins the path-mode side of the fold.go
// semantics note: ASCII queries never decode stored UTF-8, so a dir
// containing U+0130 is only reachable through a query that itself
// takes the rune path.
func TestPathQueryFoldSemantics(t *testing.T) {
	s := NewStore()
	var ref []refEntry
	addBoth(t, s, &ref, "/", "\u0130stanbul", true)
	addBoth(t, s, &ref, "/\u0130stanbul", "gezi.txt", false)

	require.Nil(t, s.Query("/istanbul", 10),
		"ASCII fast path must not fold the dir's U+0130 to 'i'")
	require.Nil(t, s.Query("istanbul/ge", 10))

	require.Equal(t, []string{"/\u0130stanbul", "/\u0130stanbul/gezi.txt"},
		pathsOf(s.Query("/\u0130stanbul", 10)))
	require.Equal(t, []string{"/\u0130stanbul/gezi.txt"},
		pathsOf(s.Query("\u0130stanbul/ge", 10)))

	// The naive reference agrees on all four.
	for _, q := range []string{"/istanbul", "istanbul/ge", "/\u0130stanbul", "\u0130stanbul/ge"} {
		require.Equal(t, naivePathQuery(ref, q, len(ref)), s.Query(q, len(ref)), "query %q", q)
	}
}

// refFromStore snapshots every live entry as a refEntry.
func refFromStore(st *Store) []refEntry {
	var ref []refEntry
	st.ForEachLive(func(id int32) bool {
		ref = append(ref, refEntry{path: st.EntryPath(id), name: st.Name(id), isDir: st.IsDir(id)})
		return true
	})
	return ref
}

// TestPathQueryMatchesNaiveReferenceSynth drives the engine against
// the naive reference over a seeded random tree, with queries sliced
// out of real entry paths so dir/name boundary straddles are covered.
func TestPathQueryMatchesNaiveReferenceSynth(t *testing.T) {
	st := buildSynthStore(11, 20000)
	ref := refFromStore(st)
	rng := rand.New(rand.NewSource(99))

	queries := []string{"/", "bench/", "data/", "zz/qq", "/bench", "/bench/", "//"}
	// Suffix windows always cross the final dir/name join.
	for i := 0; i < 10; i++ {
		p := ref[rng.Intn(len(ref))].path
		lo := rng.Intn(strings.LastIndexByte(p, '/') + 1)
		queries = append(queries, p[lo:])
	}
	// Random windows anywhere in a path, kept only when pathful.
	for len(queries) < 40 {
		p := ref[rng.Intn(len(ref))].path
		lo := rng.Intn(len(p))
		hi := lo + 1 + rng.Intn(len(p)-lo)
		if q := p[lo:hi]; strings.IndexByte(q, '/') >= 0 {
			queries = append(queries, q)
		}
	}

	for _, q := range queries {
		want := naivePathQuery(ref, q, len(ref))
		require.Equal(t, want, st.Query(q, len(ref)), "query %q (full list)", q)
		require.Equal(t, naivePathQuery(ref, q, 9), st.Query(q, 9), "query %q (limit 9)", q)
	}
}

// TestPathQueryParallelShardsMatchNaive pushes the store well past the
// single-shard threshold (both the entry scan and the dir-plan build
// fan out) and cross-checks against the naive reference.
func TestPathQueryParallelShardsMatchNaive(t *testing.T) {
	st := buildSynthStore(7, 3*minShardEntries+123)
	ref := refFromStore(st)
	for _, q := range []string{"/bench", "bench/", "data/", "/", "a/b", "qq/zz"} {
		want := naivePathQuery(ref, q, st.Len())
		got := st.Query(q, st.Len())
		require.Equal(t, len(want), len(got), "query %q hit count", q)
		require.Equal(t, want, got, "query %q", q)
	}
}

func TestPathQueryAddRemove(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/data", "keep.txt", false)

	// A dir interned after the initial adds stays queryable in path mode.
	mustAdd(t, s, "/data/New Folder", "hit.txt", false)
	require.Equal(t, []string{"/data/New Folder/hit.txt"},
		pathsOf(s.Query("new folder/hi", 10)))

	// Tombstoned entries never match path queries.
	s.RemoveByPath("/data/New Folder/hit.txt")
	require.Nil(t, s.Query("new folder/hi", 10))

	// Resurrection makes the entry match again.
	mustAdd(t, s, "/data/New Folder", "hit.txt", false)
	require.Equal(t, []string{"/data/New Folder/hit.txt"},
		pathsOf(s.Query("new folder/hi", 10)))

	// Subtree removal drops every descendant from path results.
	mustAdd(t, s, "/data", "New Folder", true)
	s.RemoveByPath("/data/New Folder")
	require.Nil(t, s.Query("new folder/hi", 10))
	require.Nil(t, s.Query("data/new", 10))
	require.Equal(t, []string{"/data/keep.txt"}, pathsOf(s.Query("data/ke", 10)))
}
