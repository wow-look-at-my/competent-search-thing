package match

import (
	"sort"
	"strings"
)

// The candidate ranking pipeline: the single choke point every result
// source routes through. Sources hand over raw Candidates -- which
// deliberately have NO score and NO position fields -- and get back
// Ranked rows, whose score/tier/positions can only be minted here
// (unexported fields, no constructor). The wire scores it emits are
// the app-wide canonical bands:
//
//	triggered:  86..100  (86 + 0.14*hint; a claimed source's results)
//	exact:      83 (+/- hint nudge, max 2)
//	prefix:     73
//	word-start: 63
//	substring:  53
//	fuzzy:      16..46 + nudge (16 + 30*normalized alignment score)
//	listed:     50 (a targeted source with an empty query lists all)
//
// Bands never overlap: triggered > exact > prefix > word-start >
// substring > listed > fuzzy, whatever the hints. The "hint" is an
// external plugin's self-score, demoted to an intra-tier nudge -- it
// can reorder rows WITHIN a tier, never lift them across tiers.
const (
	scoreTriggeredBase = 86.0
	scoreTriggeredSpan = 14.0
	scoreExact         = 83.0
	scorePrefix        = 73.0
	scoreWordStart     = 63.0
	scoreSubstring     = 53.0
	scoreFuzzyBase     = 16.0
	scoreFuzzySpan     = 30.0
	// ScoreListed is the flat score of rows a targeted source lists
	// for an empty query (the old DefaultScore behavior).
	ScoreListed = 50.0
	// hintSpread bounds the intra-tier self-score nudge: +/- 2 around
	// the band center for hints 100/0.
	hintSpread = 2.0
	// hintNeutral is the hint value that nudges nothing.
	hintNeutral = 50.0
)

// Candidate is one raw row from a result source. BY CONSTRUCTION it
// carries no score, tier, or match positions -- a source physically
// cannot ship self-ranked rows; Rank mints those onto Ranked.
type Candidate struct {
	// Display is the string the UI renders as the row title; match
	// positions are computed against it.
	Display string
	// Texts is the ordered match-field list (Texts[0] is the primary
	// field, usually Display). A term matches the candidate when it
	// matches ANY field; earlier fields win ties.
	Texts []string
	// TieBreak orders rows within equal rank, higher first (visit
	// counts, last-accessed times). Zero for sources without one.
	TieBreak int64
	// SortKey is the final deterministic tie-break (a URL, an exec
	// line), compared ascending.
	SortKey string
	// Hint is an external plugin's self-score (0..100), demoted to the
	// intra-tier nudge; nil (builtins) is neutral.
	Hint *float64
	// Payload is the source's opaque row data, returned on Ranked
	// untouched.
	Payload any
}

// Ranked is one engine-ranked row. Only Rank can produce a valid one:
// every field is unexported and there is no constructor.
type Ranked struct {
	cand      Candidate
	tier      Tier
	field     int // worst best-field index (0 = every term hit Texts[0])
	score     float64
	positions []Range
	minted    bool
}

// Candidate returns the source's candidate (Payload included).
func (r Ranked) Candidate() Candidate { return r.cand }

// Tier returns the engine-assigned tier.
func (r Ranked) Tier() Tier { return r.tier }

// Score returns the engine-minted wire score (0..100 band mapping).
func (r Ranked) Score() float64 { return r.score }

// Positions returns the per-character match ranges on Display (rune
// [start,end) pairs), nil when none were computed.
func (r Ranked) Positions() []Range { return r.positions }

// Minted reports whether this row came out of Rank (always true for
// any Ranked obtainable outside this package; the zero value is not).
func (r Ranked) Minted() bool { return r.minted }

// RankOptions configures one Rank call.
type RankOptions struct {
	// Terms is the compiled query (Terms(stripped)).
	Terms []Term
	// FuzzyDisabled turns the fuzzy tier off (config
	// search.fuzzyDisabled): candidates then need every term at
	// TierSubstring or better.
	FuzzyDisabled bool
	// Limit caps the ranked list (<= 0: no cap).
	Limit int
	// Targeted marks a bang-targeted dispatch: with no terms every
	// candidate is listed at ScoreListed instead of nothing matching.
	// With terms, the text ladder gates and ranks as usual.
	Targeted bool
	// Claimed marks results of a source that claimed the whole query
	// (external prefix/regex trigger or bang): every candidate mints
	// at TierTriggered, ordered by hint, with no text gating and no
	// engine positions.
	Claimed bool
	// PreRanked marks sources whose candidate list is already
	// query-derived and ordered (bang suggestions): rows mint in given
	// order at descending triggered-band scores, no matching, no
	// positions.
	PreRanked bool
}

// Rank is THE mint: it matches, tiers, scores, orders, caps, and
// computes display positions for a candidate list. See RankOptions for
// the modes; the default (terms + no flags) is the normal text-matched
// fan-out.
func Rank(cands []Candidate, o RankOptions) []Ranked {
	var out []Ranked
	switch {
	case o.PreRanked:
		out = make([]Ranked, 0, len(cands))
		for i, c := range cands {
			s := 100.0 - float64(i)
			if s < scoreTriggeredBase {
				s = scoreTriggeredBase
			}
			out = append(out, Ranked{cand: c, tier: TierTriggered, score: s, minted: true})
		}
		return capRanked(out, o.Limit) // given order IS the ranking
	case o.Claimed:
		out = make([]Ranked, 0, len(cands))
		for _, c := range cands {
			out = append(out, Ranked{
				cand:   c,
				tier:   TierTriggered,
				score:  scoreTriggeredBase + scoreTriggeredSpan*hintValue(c.Hint)/100,
				minted: true,
			})
		}
	case len(o.Terms) == 0:
		if !o.Targeted {
			return nil // an untargeted source with no query lists nothing
		}
		out = make([]Ranked, 0, len(cands))
		for _, c := range cands {
			out = append(out, Ranked{cand: c, tier: TierTriggered, score: ScoreListed, minted: true})
		}
	default:
		allowFuzzy := !o.FuzzyDisabled
		for _, c := range cands {
			fr, ok := MatchFields(c.Texts, o.Terms, allowFuzzy)
			if !ok {
				continue
			}
			out = append(out, Ranked{
				cand:      c,
				tier:      fr.Tier,
				field:     fr.WorstField,
				score:     bandScore(fr, c.Hint),
				positions: Positions(c.Display, o.Terms, allowFuzzy),
				minted:    true,
			})
		}
	}
	sortRanked(out)
	return capRanked(out, o.Limit)
}

// bandScore maps a text-tier match onto the canonical wire bands, with
// the bounded hint nudge.
func bandScore(fr FieldsResult, hint *float64) float64 {
	nudge := hintSpread * (hintValue(hint) - hintNeutral) / hintNeutral
	switch fr.Tier {
	case TierExact:
		return scoreExact + nudge
	case TierPrefix:
		return scorePrefix + nudge
	case TierWordStart:
		return scoreWordStart + nudge
	case TierSubstring:
		return scoreSubstring + nudge
	default: // TierFuzzy
		return scoreFuzzyBase + scoreFuzzySpan*NormalizeScore(fr.Score, fr.Units) + nudge
	}
}

func hintValue(h *float64) float64 {
	if h == nil {
		return hintNeutral
	}
	v := *h
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// sortRanked orders rows best-first: tier, then worst matched field
// (Texts[0] hits beat later-field hits), then score (desc), then
// TieBreak (desc), then case-folded Display, then Display, then
// SortKey. SliceStable keeps source order as the final fallback.
func sortRanked(rs []Ranked) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.field != b.field {
			return a.field < b.field
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if a.cand.TieBreak != b.cand.TieBreak {
			return a.cand.TieBreak > b.cand.TieBreak
		}
		af, bf := strings.ToLower(a.cand.Display), strings.ToLower(b.cand.Display)
		if af != bf {
			return af < bf
		}
		if a.cand.Display != b.cand.Display {
			return a.cand.Display < b.cand.Display
		}
		return a.cand.SortKey < b.cand.SortKey
	})
}

func capRanked(rs []Ranked, limit int) []Ranked {
	if limit > 0 && len(rs) > limit {
		return rs[:limit]
	}
	return rs
}
