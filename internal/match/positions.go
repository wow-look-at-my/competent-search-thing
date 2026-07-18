package match

import "sort"

// Per-character match positions, in RUNE indices of the DISPLAY
// string the UI renders (unicode-safe; JS converts rune indices to
// UTF-16 offsets on its side). Positions are computed lazily -- only
// for the rows that survive ranking -- so the hot scan paths never pay
// for them. Ranges are half-open [start, end) rune pairs, sorted and
// merged.

// Range is one half-open [start, end) rune range.
type Range = [2]int

// Positions returns the merged highlight ranges for terms over the
// display string: the union of each term's match positions, where a
// term contributes the occurrence that earned its best ladder tier
// (prefix start, the word-start occurrence, the first substring
// occurrence, or the optimal fuzzy alignment). Terms that do not match
// the display string at all (they may have matched another field of a
// multi-field candidate) contribute nothing. Returns nil when nothing
// matched.
func Positions(display string, terms []Term, allowFuzzy bool) []Range {
	var idx []int
	for _, t := range terms {
		tier := MatchTerm(display, t, allowFuzzy)
		if tier == TierNone {
			continue
		}
		idx = append(idx, termPositions(display, t, tier)...)
	}
	return mergePositions(idx)
}

// mergePositions sorts, dedups, and merges adjacent rune indices into
// half-open ranges.
func mergePositions(idx []int) []Range {
	if len(idx) == 0 {
		return nil
	}
	sort.Ints(idx)
	var out []Range
	start, end := idx[0], idx[0]+1
	for _, i := range idx[1:] {
		if i < end { // duplicate
			continue
		}
		if i == end {
			end++
			continue
		}
		out = append(out, Range{start, end})
		start, end = i, i+1
	}
	return append(out, Range{start, end})
}

// termPositions returns the rune indices term t matched in target,
// given its already-classified tier (which must not be TierNone or
// TierTriggered).
func termPositions(target string, t Term, tier Tier) []int {
	switch tier {
	case TierExact, TierPrefix:
		return runeSpan(target, 0, t)
	case TierWordStart:
		if at := wordStartIndex(target, t); at >= 0 {
			return runeSpan(target, at, t)
		}
		return nil
	case TierSubstring:
		at := -1
		if t.ASCII {
			at = IndexASCII(target, t.Pat)
		} else {
			at = FoldIndex(target, t.Pat)
		}
		if at >= 0 {
			return runeSpan(target, at, t)
		}
		return nil
	case TierFuzzy:
		return fuzzyPositions(target, t)
	}
	return nil
}

// runeSpan converts a byte-offset match of term t starting at byte
// `at` of target into consecutive rune indices. The matched span
// covers t's unit count in runes: for ASCII terms every matched byte
// is a standalone ASCII rune (pattern bytes are ASCII and folding is
// byte-wise), for rune terms the span covers the fold-matched runes,
// whose count can differ from the PATTERN's rune count (one target
// rune can fold-match one pattern rune of a different byte length), so
// the span length is measured on the target via FoldPrefixLen.
func runeSpan(target string, at int, t Term) []int {
	matched := 0 // matched byte length in target
	if t.ASCII {
		matched = len(t.Pat)
	} else {
		matched = FoldPrefixLen(target[at:], t.Pat)
		if matched < 0 {
			return nil // defensive: caller classified a match here
		}
	}
	startRune := runeIndexOfByte(target, at)
	n := runeCount(target[at : at+matched])
	idx := make([]int, n)
	for i := range idx {
		idx[i] = startRune + i
	}
	return idx
}

// fuzzyPositions recovers the optimal fuzzy alignment's matched
// positions as rune indices of target.
func fuzzyPositions(target string, t Term) []int {
	pat := t.Units()
	if t.ASCII {
		units := make([]int32, len(target))
		bonus := make([]int8, len(target))
		PrepareASCII(target, units, bonus)
		_, pos := AlignPositions(pat, units, bonus)
		// Unit indices are byte offsets; matched bytes are ASCII
		// (pattern bytes never equal UTF-8 continuation bytes), each a
		// standalone rune.
		return bytesToRunes(target, pos)
	}
	units, bonus := PrepareFold(target, nil, nil)
	_, pos := AlignPositions(pat, units, bonus)
	return pos // rune-regime units ARE the target's runes, in order
}

// bytesToRunes converts ascending byte offsets of target into rune
// indices with one forward walk.
func bytesToRunes(target string, byteIdx []int) []int {
	if len(byteIdx) == 0 {
		return nil
	}
	out := make([]int, 0, len(byteIdx))
	ri, bi, k := 0, 0, 0
	for bi < len(target) && k < len(byteIdx) {
		if bi == byteIdx[k] {
			out = append(out, ri)
			k++ // byteIdx is strictly ascending: one hit per offset
		}
		_, n := DecodeRuneAt(target, bi)
		bi += n
		ri++
	}
	return out
}

// runeIndexOfByte returns the rune index of byte offset b in s (b must
// sit on a rune boundary).
func runeIndexOfByte(s string, b int) int {
	return runeCount(s[:b])
}

func runeCount(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, sz := DecodeRuneAt(s, i)
		i += sz
		n++
	}
	return n
}

// AlignPositions is AlignDP with alignment recovery: it returns the
// optimal score AND the matched unit indices (ascending, one per
// pattern unit). Targets past MaxDPUnits take the greedy leftmost
// alignment (matching Align's dispatch). The subsequence property must
// already hold. Allocates its matrices per call: positions are only
// ever computed for the handful of displayed rows.
func AlignPositions(pat, units []int32, bonus []int8) (int32, []int) {
	n := len(units)
	m := len(pat)
	if m == 0 || n == 0 {
		return 0, nil
	}
	if n > MaxDPUnits {
		return greedyPositions(pat, units, bonus)
	}

	// Full H matrix plus, per row, the choice provenance:
	// hFromGap[q][p] records that H[q][p] descended from P[q-1][p-1]
	// (a gap) rather than H[q-1][p-1] (consecutive); pSrc[q][p] records
	// the position where pat[q] matched in the state P[q][p] descends
	// from.
	h := make([][]int32, m)
	hFromGap := make([][]bool, m)
	for q := range h {
		h[q] = make([]int32, n)
		hFromGap[q] = make([]bool, n)
	}
	pRow := make([]int32, n)
	pSrcRow := make([]int32, n)
	pSrc := make([][]int32, m) // pSrc[q] = provenance of P over row q's H

	for p := 0; p < n; p++ {
		if units[p] == pat[0] {
			h[0][p] = ScoreMatch + int32(bonus[p])
		} else {
			h[0][p] = nInf
		}
	}
	for q := 1; q < m; q++ {
		fillGapRowSrc(h[q-1], pRow, pSrcRow)
		pSrc[q-1] = append([]int32(nil), pSrcRow...)
		h[q][0] = nInf
		for p := 1; p < n; p++ {
			v := nInf
			fromGap := false
			if units[p] == pat[q] {
				b := int32(bonus[p])
				cb := b
				if cb < BonusConsecutive {
					cb = BonusConsecutive
				}
				v = h[q-1][p-1] + ScoreMatch + cb
				if w := pRow[p-1] + ScoreMatch + b; w > v {
					v = w
					fromGap = true
				}
			}
			h[q][p] = v
			hFromGap[q][p] = fromGap
		}
	}

	best, bestP := nInf, -1
	for p := 0; p < n; p++ {
		if h[m-1][p] > best {
			best, bestP = h[m-1][p], p
		}
	}
	if bestP < 0 {
		return best, nil // unreachable when the subsequence holds
	}
	pos := make([]int, m)
	pos[m-1] = bestP
	for q := m - 1; q > 0; q-- {
		p := pos[q]
		if hFromGap[q][p] {
			pos[q-1] = int(pSrc[q-1][p-1])
		} else {
			pos[q-1] = p - 1
		}
	}
	return best, pos
}

// fillGapRowSrc is fillGapRow plus provenance: src[t] is the position
// of the H cell the chosen P value descends from.
func fillGapRowSrc(h, gp []int32, src []int32) {
	runMax, runArg := nInf, int32(-1)
	prev, prevSrc := nInf, int32(-1)
	gp[0] = nInf
	src[0] = -1
	for t := 1; t < len(h); t++ {
		if h[t-1] > runMax {
			runMax, runArg = h[t-1], int32(t-1)
		}
		v, s := h[t-1]-GapOpen, int32(t-1)
		if w := prev - GapExtend; w > v {
			v, s = w, prevSrc
		}
		if w := runMax - GapCap; w > v {
			v, s = w, runArg
		}
		gp[t], src[t] = v, s
		prev, prevSrc = v, s
	}
}

// greedyPositions runs the greedy leftmost alignment recording its
// matched positions (the >MaxDPUnits fallback, mirroring AlignGreedy).
func greedyPositions(pat, units []int32, bonus []int8) (int32, []int) {
	score := int32(0)
	prevMatch := -1
	pos := make([]int, 0, len(pat))
	q := 0
	for p := 0; p < len(units) && q < len(pat); p++ {
		if units[p] != pat[q] {
			continue
		}
		b := int32(bonus[p])
		if q > 0 {
			if prevMatch == p-1 {
				if b < BonusConsecutive {
					b = BonusConsecutive
				}
			} else {
				pen := GapOpen + int32(p-prevMatch-2)*GapExtend
				if pen > GapCap {
					pen = GapCap
				}
				score -= pen
			}
		}
		score += ScoreMatch + b
		prevMatch = p
		pos = append(pos, p)
		q++
	}
	return score, pos
}
