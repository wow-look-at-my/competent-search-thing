// Package match is the shared search-matching engine: ONE case-folding
// definition, ONE tier ladder (exact / prefix / word-start / substring
// / subsequence-fuzzy), ONE multi-term semantics (whitespace-split
// terms, ALL must match, order-free), ONE position-aware fuzzy scorer,
// ONE per-character match-position implementation, and ONE candidate
// ranking pipeline (rank.go) that mints the only score/position values
// the UI ever sees.
//
// Consumers:
//   - internal/index keeps its blob-scan machinery (anchored scans over
//     the contiguous name blob) but folds, classifies, scores, and
//     reports positions exclusively through this package.
//   - internal/plugin's builtin providers are candidate SOURCES: they
//     hand raw rows to Rank and cannot score or position anything
//     themselves (Candidate has no score/position fields; Ranked can
//     only be minted here).
//
// Folding has two regimes, chosen per pattern by FoldPattern:
//
//   - ASCII fast path (all-ASCII pattern): the pattern is folded
//     byte-wise through FoldTable ('A'-'Z' -> lowercase, every other
//     byte identity) and matched with byte compares folding the
//     haystack side through the same table.
//   - Rune slow path (pattern with any non-ASCII byte): the pattern is
//     folded per rune with FoldRune (unicode.ToLower) and matched
//     rune-wise, folding each haystack rune the same way.
//
// SEMANTICS (pinned by internal/index/fold_test.go and the tests
// here): for all-ASCII patterns against all-ASCII data this is
// byte-for-byte the strings.ToLower behavior. An ASCII pattern does
// NOT match the two Unicode runes whose unicode.ToLower IS an ASCII
// letter -- U+0130 (dotted capital I -> 'i') and U+212A (Kelvin sign
// -> 'k') -- because the ASCII fast path never decodes stored UTF-8;
// patterns CONTAINING such runes take the rune path and match both
// forms. Invalid UTF-8 compares as U+FFFD per byte.
package match

import (
	"unicode"
	"unicode/utf8"
)

// FoldTable maps 'A'-'Z' to their lowercase forms and every other byte
// to itself: the single ASCII case-folding definition. Treat it as
// read-only.
var FoldTable = buildFoldTable()

func buildFoldTable() (t [256]byte) {
	for i := range t {
		t[i] = byte(i)
	}
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = byte(c) + ('a' - 'A')
	}
	return t
}

// FoldRune is the rune-path folding definition: Unicode simple
// lowercase mapping, matching what strings.ToLower applies per rune.
func FoldRune(r rune) rune { return unicode.ToLower(r) }

// FoldPattern lowers a query string into its match pattern. An
// all-ASCII query folds byte-wise through FoldTable and reports
// ascii=true (the byte fast path); anything else folds per rune with
// FoldRune (invalid UTF-8 becomes U+FFFD, like strings.ToLower) and
// reports false (the rune slow path). The returned slice is freshly
// allocated and mutable.
func FoldPattern(q string) (pat []byte, ascii bool) {
	for i := 0; i < len(q); i++ {
		if q[i] >= utf8.RuneSelf {
			pat = make([]byte, 0, len(q))
			for _, r := range q {
				pat = utf8.AppendRune(pat, FoldRune(r))
			}
			return pat, false
		}
	}
	pat = make([]byte, len(q))
	for i := 0; i < len(q); i++ {
		pat[i] = FoldTable[q[i]]
	}
	return pat, true
}

// UpperVariant returns the uppercase form of a folded lowercase
// letter, or c itself when the byte has no ASCII case twin.
func UpperVariant(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

// HasPrefixASCII reports whether s fold-starts with the pre-folded
// ASCII pattern.
func HasPrefixASCII[T []byte | string](s T, pat string) bool {
	if len(s) < len(pat) {
		return false
	}
	for i := 0; i < len(pat); i++ {
		if FoldTable[s[i]] != pat[i] {
			return false
		}
	}
	return true
}

// HasSuffixASCII reports whether s fold-ends with the pre-folded ASCII
// pattern.
func HasSuffixASCII(s, pat string) bool {
	return len(s) >= len(pat) && HasPrefixASCII(s[len(s)-len(pat):], pat)
}

// IndexASCII returns the byte index of the first fold-match of the
// pre-folded ASCII pattern in s, or -1. Plain per-position scan: this
// is the short-string form (provider fields, entry names); the blob
// engine in internal/index keeps its own anchored scan.
func IndexASCII[T []byte | string](s T, pat string) int {
	return indexASCIIFrom(s, pat, 0)
}

// indexASCIIFrom is IndexASCII starting the scan at byte offset from.
func indexASCIIFrom[T []byte | string](s T, pat string, from int) int {
	if len(pat) == 0 {
		if from > len(s) {
			return -1
		}
		return from
	}
	for i := from; i+len(pat) <= len(s); i++ {
		if HasPrefixASCII(s[i:], pat) {
			return i
		}
	}
	return -1
}

// ContainsASCII reports whether s fold-contains the pre-folded ASCII
// pattern.
func ContainsASCII[T []byte | string](s T, pat string) bool {
	return IndexASCII(s, pat) >= 0
}

// DecodeRuneAt decodes the rune starting at byte i of s, with exact
// utf8.DecodeRune semantics for invalid encodings (RuneError, size 1).
// The multi-byte case copies at most utf8.UTFMax bytes to a stack
// buffer, so both string and []byte callers stay allocation-free.
func DecodeRuneAt[T []byte | string](s T, i int) (rune, int) {
	if c := s[i]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	var buf [utf8.UTFMax]byte
	end := i + utf8.UTFMax
	if end > len(s) {
		end = len(s)
	}
	n := copy(buf[:], s[i:end])
	return utf8.DecodeRune(buf[:n])
}

// FoldPrefixLen returns the byte length of the prefix of s that
// fold-matches the pre-folded rune-path pattern (FoldPattern output),
// or -1 when s does not fold-start with it. A return of len(s)
// therefore means s fold-EQUALS the pattern.
func FoldPrefixLen[T []byte | string](s T, pat string) int {
	i := 0
	for j := 0; j < len(pat); {
		if i >= len(s) {
			return -1
		}
		pr, pn := DecodeRuneAt(pat, j)
		sr, sn := DecodeRuneAt(s, i)
		if FoldRune(sr) != pr {
			return -1
		}
		i += sn
		j += pn
	}
	return i
}

// FoldEquals reports whether s fold-equals the pre-folded pattern.
func FoldEquals[T []byte | string](s T, pat string) bool {
	return FoldPrefixLen(s, pat) == len(s)
}

// FoldIndex returns the byte index of the first rune boundary of s
// where the pre-folded rune-path pattern fold-matches, or -1.
// O(len(s) * len(pat)); rune-path only.
func FoldIndex[T []byte | string](s T, pat string) int {
	return foldIndexFrom(s, pat, 0)
}

// foldIndexFrom is FoldIndex starting at byte offset from (which must
// sit on a rune boundary).
func foldIndexFrom[T []byte | string](s T, pat string, from int) int {
	if len(pat) == 0 {
		if from > len(s) {
			return -1
		}
		return from
	}
	for i := from; i < len(s); {
		if FoldPrefixLen(s[i:], pat) >= 0 {
			return i
		}
		_, n := DecodeRuneAt(s, i)
		i += n
	}
	return -1
}

// FoldContains reports whether s fold-contains the pre-folded pattern
// at any rune boundary. O(len(s) * len(pat)); rune-path only.
func FoldContains[T []byte | string](s T, pat string) bool {
	if len(pat) == 0 {
		return true
	}
	return FoldIndex(s, pat) >= 0
}

// FoldHasSuffix reports whether s fold-ends with the pre-folded
// pattern, comparing runes backward from both ends.
func FoldHasSuffix(s, pat string) bool {
	i, j := len(s), len(pat)
	for j > 0 {
		if i == 0 {
			return false
		}
		pr, pn := utf8.DecodeLastRuneInString(pat[:j])
		sr, sn := utf8.DecodeLastRuneInString(s[:i])
		if FoldRune(sr) != pr {
			return false
		}
		i -= sn
		j -= pn
	}
	return true
}
