package index

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// The frecency/recency/noise ranking blend: internal/frecency's
// signals folded into the FILE result ordering. The blend runs at
// EXACTLY one stage -- selectTop's post-scan merge, over the at most
// workers*limit candidates the per-shard heaps produced -- never in
// the per-entry scan hot path, so a million-entry scan costs the same
// with the blend on. Per merged candidate:
//
//	frecency  = Signals.Boost(path)      decayed open count
//	recency   = recencyScore(age)        COLD candidates only (Boost
//	                                     == 0), age = now - the
//	                                     probe's max(atime, mtime),
//	                                     one budget-bounded batch stat
//	cwd       = Signals.CwdBoost(path)   focused-app working-dir
//	                                     proximity (weight lives in
//	                                     Signals.CwdWeight)
//	noise     = Signals.Penalty(path)    location noise in [0, 1]
//
// Ordering: the match class still dominates, EXCEPT the tier jump: a
// candidate whose decayed open count exceeds TierJump competes
// exactly ONE class up (the "file I always open" case; an
// often-opened substring match may outrank an untouched prefix
// match, but never an exact one from two tiers above). Within an
// effective class candidates order by the blended score
//
//	blended = matchScore/blendAlignDivisor
//	        + WeightFrecency*frecency + WeightRecency*recency
//	        + cwd - WeightNoise*noise
//
// descending, then the pre-blend tie-break chain (original class,
// alignment score, dirs first, shorter path, lexicographic path).
// matchScore is the fuzzy-tier alignment score (0 for every other
// class, where match quality is constant within the class and
// cancels); blendAlignDivisor maps it into blend units. A weight <= 0
// disables that signal's contribution; TierJump <= 0 disables the
// jump. With every signal zero the ordering is identical to the
// pre-blend engine, and an INACTIVE blend (nil, or zero-value
// Signals -- disabled in config, or no store wired) never runs at
// all: selectTop takes the exact pre-blend path, byte-identical
// ordering, pinned by TestBlendInactiveIsNoOp.
//
// Top-K constraint (deliberate): the blend sees only candidates that
// survived the per-shard pre-blend heaps. A boosted match rises
// within -- and one class above -- the merged set, but a candidate
// pruned before the merge (worse pre-blend than its entire shard's
// top limit) cannot be resurrected; that trade keeps the signals
// entirely off the scan hot path (TestBlendMergedSetOnly).
const (
	// DefaultBlendTierJump is the decayed-open-count threshold past
	// which a candidate competes one match class up (config
	// search.frecency.tierJumpCount).
	DefaultBlendTierJump = 3.0
	// DefaultBlendRecencyBudget bounds the cold-candidate stat pass:
	// paths not statted in time simply contribute no recency this
	// query (they land in the probe's cache for the next one).
	DefaultBlendRecencyBudget = 15 * time.Millisecond
	// blendRecencyHorizonHours is where the recency score reaches 0:
	// 30 days. See recencyScore.
	blendRecencyHorizonHours = 720
	// blendAlignDivisor maps fuzzy alignment points into blend units:
	// 64 points -- about three well-matched pattern units -- weigh as
	// much as one recorded open at the default weights.
	blendAlignDivisor = 64.0
)

// Blend configures the frecency ranking blend for one query (see the
// package comment above). The zero value and nil are both inactive.
// A Blend handed to Manager.SetBlend must be treated as immutable --
// swap a fresh copy to change anything (queries read it without
// copying).
type Blend struct {
	// Signals is the frecency signal bundle. A zero-value Signals
	// (nil Store, nil Probe, empty Cwd) deactivates the whole blend.
	Signals frecency.Signals
	// WeightFrecency scales the decayed open count; <= 0 disables it.
	WeightFrecency float64
	// WeightRecency scales the cold-start recency score in [0, 1];
	// <= 0 disables it (no stat pass runs).
	WeightRecency float64
	// WeightNoise scales the location-noise penalty in [0, 1]; <= 0
	// disables it. (The cwd weight lives in Signals.CwdWeight.)
	WeightNoise float64
	// TierJump is the decayed-open-count threshold for competing one
	// match class up; <= 0 disables tier jumping.
	TierJump float64
	// RecencyBudget bounds the cold-candidate stat pass; <= 0 selects
	// DefaultBlendRecencyBudget.
	RecencyBudget time.Duration
	// Now is the clock recency ages are measured against (tests);
	// nil means time.Now.
	Now func() time.Time

	// trace, when non-nil, receives the delivered rows' ranking
	// components (see signalstrace.go). Only ever set on the
	// per-query copies traceBlend builds -- never on a Blend handed
	// to Manager.SetBlend, so the immutability contract above is
	// untouched.
	trace *[]ResultSignals
}

// active reports whether the blend participates in ranking at all.
// Nil, or a zero-value Signals -- frecency disabled in config, or the
// app never wired a store -- means selectTop takes the exact
// pre-blend path.
func (b *Blend) active() bool {
	if b == nil {
		return false
	}
	return b.Signals.Store != nil || b.Signals.Probe != nil || b.Signals.Cwd != ""
}

// recencyScore maps a last-touch age onto [0, 1], log-scaled so the
// signal differentiates hours and days, not microseconds: ~1.0 within
// the hour, ~0.9 after one hour, ~0.5 after a day, ~0.2 after a week,
// 0 at 30 days and beyond (blendRecencyHorizonHours). Exactly:
//
//	score = 1 - log(1+ageHours) / log(1+720)
//
// clamped to [0, 1]; a zero or negative age (clock skew) scores 1.
func recencyScore(age time.Duration) float64 {
	if age <= 0 {
		return 1
	}
	v := 1 - math.Log1p(age.Hours())/math.Log1p(blendRecencyHorizonHours)
	if v < 0 {
		return 0
	}
	return v
}

// selectBlended is selectTop's blend-active tail: it computes the
// signals for the merged candidates, orders them by effective class
// then blended score then the pre-blend chain, and builds Results for
// the best limit entries. len(all) > 0; the merged set is at most
// workers*limit entries, so the per-path work here (one EntryPath,
// map lookups, at most one bounded stat batch) is far off the scan
// path.
func (s *Store) selectBlended(all []cand, limit int, b *Blend) []Result {
	n := len(all)
	paths := make([]string, n)
	boost := make([]float64, n)
	var cold []string
	for i, c := range all {
		paths[i] = s.EntryPath(c.id)
		boost[i] = b.Signals.Boost(paths[i])
		if boost[i] == 0 {
			cold = append(cold, paths[i])
		}
	}

	// One budget-bounded batch stat for the cold candidates only: a
	// path with open history ranks by that history, not by its atime.
	var rec map[string]time.Time
	if len(cold) > 0 && b.WeightRecency > 0 && b.Signals.Probe != nil {
		budget := b.RecencyBudget
		if budget <= 0 {
			budget = DefaultBlendRecencyBudget
		}
		rec = b.Signals.Recency(context.Background(), cold, budget)
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	nowT := now()

	// A requested signals trace (signalstrace.go) captures each
	// component exactly as it participates below -- indices into
	// arrays this stage computes anyway, nothing new evaluated. With
	// tr nil (the normal case) the captures compile to nothing.
	tr := b.traceBuf()
	var trRec, trCwd, trPen []float64
	if tr != nil {
		trRec = make([]float64, n)
		trCwd = make([]float64, n)
		trPen = make([]float64, n)
	}

	eff := make([]uint8, n)
	blended := make([]float64, n)
	for i, c := range all {
		d := float64(c.score) / blendAlignDivisor
		if w := b.WeightFrecency; w > 0 {
			d += w * boost[i]
		}
		if w := b.WeightRecency; w > 0 && boost[i] == 0 {
			if ts, ok := rec[paths[i]]; ok {
				r := recencyScore(nowT.Sub(ts))
				d += w * r
				if tr != nil {
					trRec[i] = r
				}
			}
		}
		cw := b.Signals.CwdBoost(paths[i])
		d += cw
		if tr != nil {
			trCwd[i] = cw
		}
		if w := b.WeightNoise; w > 0 {
			p := b.Signals.Penalty(paths[i])
			d -= w * p
			if tr != nil {
				trPen[i] = p
			}
		}
		blended[i] = d
		eff[i] = c.class
		if b.TierJump > 0 && boost[i] > b.TierJump && eff[i] > 0 {
			eff[i]--
		}
	}

	ord := make([]int, n)
	for i := range ord {
		ord[i] = i
	}
	sort.SliceStable(ord, func(x, y int) bool {
		i, j := ord[x], ord[y]
		if eff[i] != eff[j] {
			return eff[i] < eff[j]
		}
		if blended[i] != blended[j] {
			return blended[i] > blended[j]
		}
		return s.candCompare(all[i], all[j]) < 0
	})
	if len(ord) > limit {
		ord = ord[:limit]
	}
	out := make([]Result, len(ord))
	for x, i := range ord {
		out[x] = Result{Path: paths[i], Name: s.Name(all[i].id), IsDir: all[i].isDir}
	}
	if tr != nil {
		sig := make([]ResultSignals, len(ord))
		for x, i := range ord {
			c := all[i]
			sig[x] = ResultSignals{
				Path:     paths[i],
				Class:    c.class,
				EffClass: eff[i],
				Align:    c.score,
				Boost:    boost[i],
				Recency:  trRec[i],
				Cwd:      trCwd[i],
				Penalty:  trPen[i],
				IsDir:    c.isDir,
				PathLen:  c.pathLen,
			}
		}
		*tr = sig
	}
	return out
}
