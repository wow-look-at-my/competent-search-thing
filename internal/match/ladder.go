package match

import (
	"unicode"
	"unicode/utf8"
)

// The tier ladder: every result source in the app classifies matches
// through this ONE ordering. Lower values rank higher.
type Tier uint8

const (
	// TierTriggered is engine-assigned to results of a source that
	// CLAIMED the query (a bang target or a prefix/regex trigger
	// match): they rank above every text-matched tier because their
	// results answer the query rather than text-match it (a calculator
	// plugin's "42" for the query "6*7"). MatchTerm never returns it.
	TierTriggered Tier = iota
	// TierExact: the target fold-equals the term.
	TierExact
	// TierPrefix: the target fold-starts with the term.
	TierPrefix
	// TierWordStart: the term matches at a word start inside the
	// target (right after a rune that is not a letter or digit).
	TierWordStart
	// TierSubstring: the term matches elsewhere in the target.
	TierSubstring
	// TierFuzzy: the term is a subsequence (but not a substring) of
	// the target.
	TierFuzzy
	// TierNone: no match (the ok=false sentinel; sorts last).
	TierNone
)

// String returns the tier's wire/debug name.
func (t Tier) String() string {
	switch t {
	case TierTriggered:
		return "triggered"
	case TierExact:
		return "exact"
	case TierPrefix:
		return "prefix"
	case TierWordStart:
		return "word-start"
	case TierSubstring:
		return "substring"
	case TierFuzzy:
		return "fuzzy"
	}
	return "none"
}

// MatchTerm classifies how term t matches target, returning the best
// (strongest) text tier, or TierNone. allowFuzzy=false stops the
// ladder at TierSubstring (the search.fuzzyDisabled toggle). An empty
// target never matches; an empty term never occurs (Terms drops
// empties).
func MatchTerm(target string, t Term, allowFuzzy bool) Tier {
	if target == "" || t.Pat == "" {
		return TierNone
	}
	if t.ASCII {
		return matchTermASCII(target, t.Pat, allowFuzzy)
	}
	return matchTermFold(target, t.Pat, allowFuzzy)
}

func matchTermASCII(target, pat string, allowFuzzy bool) Tier {
	if HasPrefixASCII(target, pat) {
		if len(target) == len(pat) {
			return TierExact
		}
		return TierPrefix
	}
	at := IndexASCII(target, pat)
	if at >= 0 {
		if wordStartAtASCII(target, pat, at) >= 0 {
			return TierWordStart
		}
		return TierSubstring
	}
	if allowFuzzy && SubseqASCII(target, pat) {
		return TierFuzzy
	}
	return TierNone
}

func matchTermFold(target, pat string, allowFuzzy bool) Tier {
	if pl := FoldPrefixLen(target, pat); pl >= 0 {
		if pl == len(target) {
			return TierExact
		}
		return TierPrefix
	}
	at := FoldIndex(target, pat)
	if at >= 0 {
		if wordStartAtFold(target, pat, at) >= 0 {
			return TierWordStart
		}
		return TierSubstring
	}
	if allowFuzzy && SubseqFold(target, PatternUnits(pat, false)) {
		return TierFuzzy
	}
	return TierNone
}

// wordStartIndex returns the byte offset of the first occurrence of
// term t in target that starts a word (index 0, or right after a rune
// that is not a letter or digit), or -1. Shared word semantics for
// every provider: words are letter/digit runs, so spaces, hyphens,
// dots, and underscores all separate.
func wordStartIndex(target string, t Term) int {
	if t.ASCII {
		return wordStartAtASCII(target, t.Pat, IndexASCII(target, t.Pat))
	}
	return wordStartAtFold(target, t.Pat, FoldIndex(target, t.Pat))
}

// wordStartAtASCII walks occurrences of pat from the one at byte
// offset `first` (-1 = none) until one sits at a word start.
func wordStartAtASCII(target, pat string, first int) int {
	for at := first; at >= 0; at = indexASCIIFrom(target, pat, at+1) {
		if isWordStart(target, at) {
			return at
		}
	}
	return -1
}

// wordStartAtFold is wordStartAtASCII for the rune regime; occurrence
// scanning advances rune-wise.
func wordStartAtFold(target, pat string, first int) int {
	for at := first; at >= 0; {
		if isWordStart(target, at) {
			return at
		}
		_, n := DecodeRuneAt(target, at)
		at = foldIndexFrom(target, pat, at+n)
	}
	return -1
}

// isWordStart reports whether byte offset at of s begins a word: the
// string start, or a position whose preceding rune is not a letter or
// digit.
func isWordStart(s string, at int) bool {
	if at == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:at])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}
