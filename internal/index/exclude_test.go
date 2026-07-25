package index

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// refMatchAny is the PRE-fast-path reference: the old Excluder halves
// were exactly this loop over every pattern of the kind. The literal
// set added since must not change a single answer.
func refMatchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, s); ok {
			return true
		}
	}
	return false
}

// splitRef reproduces NewExcluder's base/full split independently, so
// the reference does not borrow the code under test.
func splitRef(patterns []string) (base, full []string) {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if strings.ContainsRune(p, '/') || strings.ContainsRune(p, filepath.Separator) {
			full = append(full, p)
		} else {
			base = append(base, p)
		}
	}
	return base, full
}

// excludeCorpus mixes literal patterns (the shipped defaults' shape)
// with every filepath.Match metacharacter, including patterns whose
// literal-looking text still must take the glob path.
var excludeCorpus = []string{
	// Literals: base names and full paths.
	".git", "node_modules", ".cache", "__pycache__", "lost+found",
	"/proc", "/sys", "/var/tmp", "/System/Volumes/Data",
	// Globs.
	"*.tmp", "?ache", "[abc]ache", "/home/*/secret", "/opt/*",
	// Escapes: literal-looking but metacharacter-bearing.
	`weird\*name`, `br[a]cket`,
	// A pattern that is a prefix of another, and a dotfile glob.
	".ca*", "node_modules_old",
}

// excludeCandidates covers hits, near-misses, and the strings the
// metacharacter patterns are meant to catch.
var excludeCandidates = []string{
	".git", ".gitignore", "git", "node_modules", "node_modules_old",
	".cache", ".cach", ".caches", "__pycache__", "lost+found",
	"cache", "hache", "aache", "bache", "cach",
	"report.go", "notes.tmp", ".tmp", "tmp",
	"/proc", "/proc/1", "/sys", "/system", "/var/tmp", "/var/tmpx",
	"/home/u/secret", "/home/u/v/secret", "/opt/x", "/opt/x/y",
	"/System/Volumes/Data", "weird*name", `weird\*name`, "br[a]cket", "bracket",
	"", "a", "/",
}

// TestExcluderMatchesReferenceSemantics is the drift gate for the
// literal fast path: for every candidate, both halves must answer
// exactly what a plain filepath.Match loop over the same patterns
// answers.
func TestExcluderMatchesReferenceSemantics(t *testing.T) {
	ex, err := NewExcluder(excludeCorpus)
	require.NoError(t, err)
	refBase, refFull := splitRef(excludeCorpus)

	for _, s := range excludeCandidates {
		require.Equal(t, refMatchAny(refBase, s), ex.MatchBase(s),
			"MatchBase(%q) diverges from the filepath.Match reference", s)
		require.Equal(t, refMatchAny(refFull, s), ex.MatchFull(s),
			"MatchFull(%q) diverges from the filepath.Match reference", s)
		require.Equal(t, ex.MatchBase(s) || ex.MatchFull(s), ex.Match(s, s),
			"Match must stay exactly MatchBase||MatchFull for %q", s)
	}
}

// TestIsLiteralPatternImpliesEquality pins the claim the fast path
// rests on: a pattern with no filepath.Match metacharacter matches
// exactly itself and nothing else.
func TestIsLiteralPatternImpliesEquality(t *testing.T) {
	for _, p := range excludeCorpus {
		if p == "" || !isLiteralPattern(p) {
			continue
		}
		for _, s := range excludeCandidates {
			ok, err := filepath.Match(p, s)
			require.NoError(t, err, "pattern %q", p)
			require.Equal(t, p == s, ok,
				"literal pattern %q must match %q iff they are equal", p, s)
		}
	}
	// And the classification itself: every metacharacter routes to the
	// glob path, so no escape sequence is ever answered by equality.
	for _, p := range []string{"*", "a*", "?", "a?", "[", "a[b]", `\`, `a\b`} {
		require.False(t, isLiteralPattern(p), "%q carries a metacharacter", p)
	}
	for _, p := range []string{"a", ".git", "/proc", "a b", "a+b", "a.b", "a-b"} {
		require.True(t, isLiteralPattern(p), "%q carries no metacharacter", p)
	}
}

// TestExcluderHasFullPatterns pins the walker's fast-path gate across
// both storage halves: it must be true when EITHER a literal or a glob
// full-path pattern exists, and false otherwise.
func TestExcluderHasFullPatterns(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{"none", []string{".git", "*.tmp"}, false},
		{"literal full", []string{".git", "/proc"}, true},
		{"glob full", []string{".git", "/home/*/secret"}, true},
		{"both", []string{"/proc", "/home/*/secret"}, true},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex, err := NewExcluder(tc.patterns)
			require.NoError(t, err)
			require.Equal(t, tc.want, ex.HasFullPatterns())
		})
	}
}

// TestExcluderRejectsBadPattern keeps the up-front validation: a
// malformed pattern must be reported, not silently sorted into a
// bucket that never matches.
func TestExcluderRejectsBadPattern(t *testing.T) {
	_, err := NewExcluder([]string{"[unclosed"})
	require.Error(t, err)
	require.ErrorIs(t, err, filepath.ErrBadPattern)
}
