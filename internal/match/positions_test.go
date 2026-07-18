package match

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergePositions(t *testing.T) {
	require.Nil(t, mergePositions(nil))
	require.Equal(t, []Range{{2, 3}}, mergePositions([]int{2}))
	require.Equal(t, []Range{{0, 3}}, mergePositions([]int{0, 1, 2}))
	require.Equal(t, []Range{{0, 2}, {4, 5}}, mergePositions([]int{4, 0, 1}))
	require.Equal(t, []Range{{0, 2}}, mergePositions([]int{1, 0, 1, 0}), "duplicates collapse")
}

func TestPositionsLiteralTiers(t *testing.T) {
	// Prefix: the leading runes.
	require.Equal(t, []Range{{0, 4}}, Positions("Firefox", Terms("fire"), true))
	// Exact: the whole string.
	require.Equal(t, []Range{{0, 4}}, Positions("Fire", Terms("fire"), true))
	// Word start: the occurrence that earned the tier, not the first
	// substring occurrence.
	require.Equal(t, []Range{{10, 14}}, Positions("Campfired Fireplace", Terms("fire"), true))
	// Substring: the first occurrence.
	require.Equal(t, []Range{{4, 8}}, Positions("campfire", Terms("fire"), true))
	// No match: nil.
	require.Nil(t, Positions("GIMP", Terms("fire"), true))
	// Multi-term union; adjacent ranges merge.
	require.Equal(t, []Range{{0, 7}}, Positions("Firefox", Terms("fire fox"), true))
	// Disjoint terms.
	require.Equal(t, []Range{{0, 2}, {13, 19}}, Positions("My Documents Backup", Terms("my backup"), true))
	// A term that only matched another field contributes nothing.
	require.Equal(t, []Range{{0, 4}}, Positions("Fire", Terms("fire zebra"), true))
}

func TestPositionsFuzzy(t *testing.T) {
	got := Positions("Firefox", Terms("firefx"), true)
	require.Equal(t, []Range{{0, 5}, {6, 7}}, got, "f-i-r-e-f matched, o skipped, x matched")

	// Fuzzy positions favor the structurally best alignment: for "fb"
	// against "f oo_bar" the DP picks the boundary 'b', not merely any
	// subsequence.
	got = Positions("foo_bar", Terms("fb"), true)
	require.Equal(t, []Range{{0, 1}, {4, 5}}, got)

	// Fuzzy disabled: no positions from subsequence-only terms.
	require.Nil(t, Positions("Firefox", Terms("firefx"), false))
}

func TestPositionsRuneSpace(t *testing.T) {
	// Positions are RUNE indices, not byte offsets.
	require.Equal(t, []Range{{1, 5}}, Positions("\u00c4hnlich", Terms("hnli"), true))
	require.Equal(t, []Range{{0, 2}}, Positions("\u00c4h oh", Terms("\u00e4h"), true))
	// An astral (4-byte) rune before the match still counts as ONE rune.
	require.Equal(t, []Range{{2, 6}}, Positions("\U0001f600 fire", Terms("fire"), true))
	// Rune-regime fuzzy: unit indices are rune indices already.
	require.Equal(t, []Range{{0, 2}, {7, 8}}, Positions("M\u00fcsic-S\u00f6ng", Terms("m\u00fc\u00f6"), true))
	// A rune term whose fold changes byte length still spans the right
	// runes of the TARGET (U+212A Kelvin folds to 1-byte 'k').
	require.Equal(t, []Range{{0, 3}}, Positions("\u212aelvin", Terms("\u212ael"), true))
}

// scoreOfAlignment recomputes the score of a concrete alignment
// (matched positions) under the documented model: base + bonuses
// (consecutive floor) - capped gap penalties. It is the independent
// reference for AlignPositions' score/position consistency.
func scoreOfAlignment(pos []int, units []int32, bonus []int8) int32 {
	score := int32(0)
	for i, p := range pos {
		b := int32(bonus[p])
		if i > 0 {
			gap := p - pos[i-1] - 1
			if gap == 0 {
				if b < BonusConsecutive {
					b = BonusConsecutive
				}
			} else {
				pen := GapOpen + int32(gap-1)*GapExtend
				if pen > GapCap {
					pen = GapCap
				}
				score -= pen
			}
		}
		score += ScoreMatch + b
	}
	return score
}

// TestAlignPositionsConsistency: the recovered alignment is a valid
// subsequence alignment, its recomputed score equals the returned
// score, and that score equals the score-only DP's (optimality carried
// over) -- on randomized inputs.
func TestAlignPositionsConsistency(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	alphabet := "abcxyz_-. AB"
	var d DPState
	for iter := 0; iter < 3000; iter++ {
		var sb strings.Builder
		for i := 0; i < 1+rng.Intn(30); i++ {
			sb.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}
		target := sb.String()
		// Sample a subsequence as the pattern.
		var pb []byte
		for i := 0; i < len(target) && len(pb) < 5; i++ {
			if rng.Intn(3) == 0 && target[i] != ' ' {
				pb = append(pb, FoldTable[target[i]])
			}
		}
		if len(pb) == 0 {
			continue
		}
		pat := PatternUnits(string(pb), true)
		units := make([]int32, len(target))
		bonus := make([]int8, len(target))
		PrepareASCII(target, units, bonus)

		score, pos := AlignPositions(pat, units, bonus)
		require.Len(t, pos, len(pat), "target %q pat %q", target, pb)
		for i := range pos {
			if i > 0 {
				require.Greater(t, pos[i], pos[i-1], "ascending positions, target %q pat %q", target, pb)
			}
			require.Equal(t, pat[i], units[pos[i]], "matched units, target %q pat %q", target, pb)
		}
		require.Equal(t, score, scoreOfAlignment(pos, units, bonus),
			"returned score is the alignment's score, target %q pat %q", target, pb)
		require.Equal(t, d.Align(pat, units, bonus), score,
			"positions DP finds the optimal score, target %q pat %q", target, pb)
	}
}

func TestAlignPositionsGreedyFallback(t *testing.T) {
	target := "a" + strings.Repeat("x", MaxDPUnits) + "ab"
	units := make([]int32, len(target))
	bonus := make([]int8, len(target))
	PrepareASCII(target, units, bonus)
	pat := PatternUnits("ab", true)
	score, pos := AlignPositions(pat, units, bonus)
	require.Equal(t, AlignGreedy(pat, units, bonus), score)
	require.Equal(t, []int{0, len(target) - 1}, pos, "greedy leftmost alignment")

	// Degenerate inputs.
	s, p := AlignPositions(nil, units, bonus)
	require.Zero(t, s)
	require.Nil(t, p)
}

// TestScoreOrderingPins mirrors the index-side ordering pins on the
// shared scorer: structural matches beat scatter, and the DP beats
// greedy where a later consecutive run exists.
func TestScoreOrderingPins(t *testing.T) {
	score := func(target, q string) int32 { return TermScore(target, NewTerm(q)) }
	require.Greater(t, score("foo_bar", "fb"), score("FooBar", "fb"))
	require.Greater(t, score("FooBar", "fb"), score("fxxbyyy", "fb"))
	require.Greater(t, score("fxb", "fb"), score("xfxb", "fb"))
	require.Greater(t, score("xabx", "ab"), score("xa_bx", "ab"))
	require.Greater(t, score("x1ab", "ab"), score("xxab", "ab"))
	long := "a" + strings.Repeat("x", 40) + "b"
	longer := "a" + strings.Repeat("x", 200) + "b"
	require.Equal(t, score(long, "ab"), score(longer, "ab"))
	require.Greater(t, score("axxb", "ab"), score(long, "ab"))

	// Rune/byte regime parity on pure-ASCII input.
	pat, ascii := FoldPattern("drp1")
	require.True(t, ascii)
	asciiScore := TermScore("data_report_12.txt", Term{Pat: string(pat), ASCII: true})
	runeScore := TermScore("data_report_12.txt", Term{Pat: string(pat), ASCII: false})
	require.Equal(t, asciiScore, runeScore)
}
