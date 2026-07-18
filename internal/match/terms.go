package match

import "strings"

// Term is one pre-folded match term: the folded pattern plus the
// folding regime FoldPattern chose for it. The regime matters for
// semantics, not just speed -- an ASCII pattern folds haystack BYTES
// (never decoding stored UTF-8), a rune pattern folds haystack RUNES
// -- so it must travel with the pattern.
type Term struct {
	// Pat is the folded pattern (FoldPattern output).
	Pat string
	// ASCII selects the folding regime Pat was prepared for.
	ASCII bool
}

// NewTerm folds one raw string into a Term.
func NewTerm(s string) Term {
	pat, ascii := FoldPattern(s)
	return Term{Pat: string(pat), ASCII: ascii}
}

// Terms splits a raw query into its match terms: whitespace-separated
// fields (strings.Fields), each folded via FoldPattern, empties
// dropped. Multi-term semantics everywhere in the app: ALL terms must
// match, in any order ("fire fox" matches "Firefox"). A query that is
// all whitespace yields no terms.
func Terms(query string) []Term {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return nil
	}
	out := make([]Term, len(fields))
	for i, f := range fields {
		out[i] = NewTerm(f)
	}
	return out
}

// Units returns the term's comparison units: bytes for the ASCII
// regime, runes otherwise (PatternUnits of Pat).
func (t Term) Units() []int32 { return PatternUnits(t.Pat, t.ASCII) }

// RuneLen returns the term's length in comparison units.
func (t Term) RuneLen() int {
	if t.ASCII {
		return len(t.Pat)
	}
	n := 0
	for i := 0; i < len(t.Pat); {
		_, sz := DecodeRuneAt(t.Pat, i)
		i += sz
		n++
	}
	return n
}

// PatternUnits lowers a pre-folded pattern into comparison units:
// bytes for the ASCII regime, runes otherwise.
func PatternUnits(pat string, ascii bool) []int32 {
	if ascii {
		units := make([]int32, len(pat))
		for i := 0; i < len(pat); i++ {
			units[i] = int32(pat[i])
		}
		return units
	}
	units := make([]int32, 0, len(pat))
	for _, r := range pat {
		units = append(units, int32(r))
	}
	return units
}

// SubseqASCII reports whether the pre-folded ASCII pattern is a
// subsequence of s (all pattern bytes present in order, gaps allowed),
// folding each haystack byte through FoldTable.
func SubseqASCII[T []byte | string](s T, pat string) bool {
	if len(pat) > len(s) {
		return false
	}
	j := 0
	for i := 0; i < len(s) && j < len(pat); i++ {
		if FoldTable[s[i]] == pat[j] {
			j++
		}
	}
	return j == len(pat)
}

// SubseqFold reports whether the pattern (as folded rune units) is a
// subsequence of s's FoldRune-folded runes. Invalid UTF-8 decodes as
// U+FFFD per byte, matching FoldContains.
func SubseqFold[T []byte | string](s T, patUnits []int32) bool {
	j := 0
	for i := 0; i < len(s) && j < len(patUnits); {
		r, n := DecodeRuneAt(s, i)
		if int32(FoldRune(r)) == patUnits[j] {
			j++
		}
		i += n
	}
	return j == len(patUnits)
}

// Subseq reports whether term t is a subsequence of s in t's regime.
func Subseq[T []byte | string](s T, t Term) bool {
	if t.ASCII {
		return SubseqASCII(s, t.Pat)
	}
	return SubseqFold(s, PatternUnits(t.Pat, false))
}
