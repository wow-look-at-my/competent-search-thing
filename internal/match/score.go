package match

import "unicode"

// Position-aware fuzzy alignment scoring, shared by the file-index
// engine and the candidate ranking pipeline. Per matched pattern unit
// a base score plus positional bonuses -- string start, after a word
// boundary ('-', '_', '.', ' ' and letter<->digit transitions),
// camelCase lower->upper steps, and consecutive-run continuation --
// minus a capped affine gap penalty per gap. Targets up to MaxDPUnits
// units get an optimal-alignment DP (fzf-v2 style); longer targets
// fall back to a greedy first-occurrence alignment. Tests pin score
// ORDERINGS, never absolute values.
//
// Only their relative order is contractual: boundary > camel >
// transition keeps "foo_bar" ahead of "FooBar" ahead of unstructured
// scatter for an "fb"-style pattern, and the gap cap keeps one huge
// gap from drowning an otherwise good match.
const (
	ScoreMatch       = 16 // per matched pattern unit
	BonusStart       = 12 // match at the first unit of the target
	BonusBoundary    = 10 // match right after '-', '_', '.', ' '
	BonusCamel       = 7  // match at a lower->upper camelCase step
	BonusTransition  = 6  // match at a letter<->digit transition
	BonusConsecutive = 8  // match directly after the previous match
	GapOpen          = 3  // first skipped unit of a gap
	GapExtend        = 1  // each further skipped unit
	GapCap           = 9  // ceiling of one gap's total penalty
	// MaxDPUnits bounds the optimal-alignment DP; longer targets
	// (pathological -- real file names top out at 255 bytes) take the
	// greedy fallback.
	MaxDPUnits = 512
)

// nInf is the DP's minus-infinity: low enough that no chain of
// additive bonuses can ever raise an unreachable state above a real
// score, high enough that per-row arithmetic can never underflow
// int32.
const nInf = int32(-1) << 28

// PrepareASCII fills units and bonus (both pre-sized to len(s)) for
// the byte regime: units are FoldTable-folded bytes, bonuses classify
// the original bytes' positions.
func PrepareASCII[T []byte | string](s T, units []int32, bonus []int8) {
	var prev byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		units[i] = int32(FoldTable[c])
		bonus[i] = ASCIIBonus(prev, c, i == 0)
		prev = c
	}
}

// PrepareFold appends the rune-regime units and bonuses for s to the
// (caller-reset) slices and returns them: units are the target's
// FoldRune-folded runes, bonuses classify the original runes.
func PrepareFold[T []byte | string](s T, units []int32, bonus []int8) ([]int32, []int8) {
	var prev rune
	first := true
	for i := 0; i < len(s); {
		r, n := DecodeRuneAt(s, i)
		units = append(units, int32(FoldRune(r)))
		bonus = append(bonus, RuneBonus(prev, r, first))
		prev, first = r, false
		i += n
	}
	return units, bonus
}

// ASCIIBonus classifies the positional bonus for a match at a byte
// whose predecessor is prev (bytes of a multi-byte rune land in the
// no-bonus default; the ASCII regime never inspects them as runes).
func ASCIIBonus(prev, cur byte, first bool) int8 {
	if first {
		return BonusStart
	}
	switch prev {
	case '-', '_', '.', ' ':
		return BonusBoundary
	}
	if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
		return BonusCamel
	}
	pd := prev >= '0' && prev <= '9'
	cd := cur >= '0' && cur <= '9'
	pl := isASCIILetter(prev)
	cl := isASCIILetter(cur)
	if (pl && cd) || (pd && cl) {
		return BonusTransition
	}
	return 0
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// RuneBonus is ASCIIBonus over runes, using the Unicode categories; on
// ASCII input it agrees with ASCIIBonus exactly (pinned by tests).
func RuneBonus(prev, cur rune, first bool) int8 {
	if first {
		return BonusStart
	}
	switch prev {
	case '-', '_', '.', ' ':
		return BonusBoundary
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return BonusCamel
	}
	if (unicode.IsLetter(prev) && unicode.IsDigit(cur)) ||
		(unicode.IsDigit(prev) && unicode.IsLetter(cur)) {
		return BonusTransition
	}
	return 0
}

// DPState carries the four rolling DP rows, reusable across calls.
// The zero value is ready to use; a DPState is not goroutine-safe.
type DPState struct {
	rowH, rowP, curH, curP []int32
}

// Align scores an already-verified subsequence match of pat against
// the prepared target (units/bonus from PrepareASCII or PrepareFold,
// equal lengths): the optimal-alignment DP up to MaxDPUnits target
// units, the greedy leftmost alignment beyond.
func (d *DPState) Align(pat, units []int32, bonus []int8) int32 {
	if len(units) <= MaxDPUnits {
		return d.AlignDP(pat, units, bonus)
	}
	return AlignGreedy(pat, units, bonus)
}

// AlignDP computes the optimal alignment score of pat against the
// prepared target: affine gaps with a per-gap cap, consecutive bonus,
// O(m*n) time over four rolling rows. States per pattern unit q and
// target position p:
//
//	H[q][p] = best score with pat[q] matched exactly at p
//	P[q][p] = max over k < p of H[q][k] - gapPen(p-k), the "last match
//	          before p, gap running through p" state, where
//	          gapPen(g) = min(GapOpen + (g-1)*GapExtend, GapCap); the
//	          cap is realized by a running-max floor (see fillGapRow).
//
// The subsequence check has already passed, so a valid final state
// exists; the gap before the first match and after the last one is
// free (position preference is expressed by bonuses instead).
func (d *DPState) AlignDP(pat, units []int32, bonus []int8) int32 {
	n := len(units)
	m := len(pat)

	d.rowH = GrowI32(d.rowH, n)
	d.rowP = GrowI32(d.rowP, n)
	d.curH = GrowI32(d.curH, n)
	d.curP = GrowI32(d.curP, n)
	prevH, prevP, curH, curP := d.rowH, d.rowP, d.curH, d.curP

	for p := 0; p < n; p++ {
		if units[p] == pat[0] {
			curH[p] = ScoreMatch + int32(bonus[p])
		} else {
			curH[p] = nInf
		}
	}
	for q := 1; q < m; q++ {
		prevH, curH = curH, prevH
		prevP, curP = curP, prevP
		fillGapRow(prevH, prevP)
		curH[0] = nInf
		for p := 1; p < n; p++ {
			v := nInf
			if units[p] == pat[q] {
				b := int32(bonus[p])
				cb := b
				if cb < BonusConsecutive {
					cb = BonusConsecutive
				}
				v = prevH[p-1] + ScoreMatch + cb
				if w := prevP[p-1] + ScoreMatch + b; w > v {
					v = w
				}
			}
			curH[p] = v
		}
	}
	best := nInf
	for p := 0; p < n; p++ {
		if curH[p] > best {
			best = curH[p]
		}
	}
	d.rowH, d.rowP, d.curH, d.curP = prevH, prevP, curH, curP
	return best
}

// fillGapRow derives one pattern row's gap state P from its match
// state H (see AlignDP): P[t] chooses, per position, between opening a
// gap after a match at t-1, extending the running gap, and the capped
// floor under the best match seen so far.
func fillGapRow(h, gp []int32) {
	runMax := nInf
	prev := nInf
	gp[0] = nInf
	for t := 1; t < len(h); t++ {
		if h[t-1] > runMax {
			runMax = h[t-1]
		}
		v := h[t-1] - GapOpen
		if w := prev - GapExtend; w > v {
			v = w
		}
		if w := runMax - GapCap; w > v {
			v = w
		}
		gp[t] = v
		prev = v
	}
}

// AlignGreedy scores the first-occurrence (leftmost) alignment: the
// fallback for targets past the DP bound. Same bonus and gap model,
// single pass, not necessarily optimal.
func AlignGreedy(pat, units []int32, bonus []int8) int32 {
	score := int32(0)
	prevMatch := -1
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
		q++
	}
	return score
}

// TermScore returns the alignment score of term t against target,
// which must hold t as a subsequence in t's regime (any matching
// ladder tier implies that). Allocates scratch per call; hot paths
// (the file index) keep pooled scratch instead.
func TermScore(target string, t Term) int32 {
	var d DPState
	pat := t.Units()
	if t.ASCII {
		units := make([]int32, len(target))
		bonus := make([]int8, len(target))
		PrepareASCII(target, units, bonus)
		return d.Align(pat, units, bonus)
	}
	units, bonus := PrepareFold(target, nil, nil)
	return d.Align(pat, units, bonus)
}

// NormalizeScore maps an alignment score for a pattern of the given
// total unit count into 0..1 for band scaling: the divisor is the
// per-unit ceiling (ScoreMatch + BonusStart), negatives clamp to 0.
func NormalizeScore(score int32, units int) float64 {
	if units <= 0 || score <= 0 {
		return 0
	}
	max := float64(units) * float64(ScoreMatch+BonusStart)
	v := float64(score) / max
	if v > 1 {
		return 1
	}
	return v
}

// GrowI32 returns s resized to n, reallocating only when the capacity
// is short.
func GrowI32(s []int32, n int) []int32 {
	if cap(s) < n {
		return make([]int32, n)
	}
	return s[:n]
}

// GrowI8 is GrowI32 for int8 slices.
func GrowI8(s []int8, n int) []int8 {
	if cap(s) < n {
		return make([]int8, n)
	}
	return s[:n]
}
