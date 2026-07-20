package arbiter

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Training: pairwise logistic SGD over the pick log -- in every
// impression the picked row should outscore each other shown row --
// deterministic given the log (fixed seed, fixed hyperparameters,
// fixed iteration structure), followed by the ACTIVATION GATE:
//
//  1. At least MinPicks JOINED picks must exist (unjoined records
//     carry structurally-zero file features and never train).
//  2. Weights must be finite.
//  3. On a time split -- oldest 80% trains, newest 20% holds out --
//     the model's holdout pairwise accuracy must STRICTLY beat the
//     logged baseline order's accuracy on the same pairs (the
//     delivered flat order already ranked the picked row somewhere;
//     a model that cannot outpredict what was shown has nothing to
//     add and stays inert). When the gate passes, the shipped model
//     retrains on ALL picks with the same hyperparameters -- the
//     holdout validated the procedure, so the final fit uses every
//     record.
const (
	// MinPicks gates activation: below this many joined picks the
	// arbiter is inert (the app layer reports the reason once).
	MinPicks = 200
	// RetrainEvery is how many NEW picks accumulate between automatic
	// retrains (the app layer counts RecordPick calls).
	RetrainEvery = 50

	// Fixed training hyperparameters -- deliberately internal, like
	// internal/priors' tuning constants: the config switch is the
	// whole knob.
	trainSeed       = 20260720
	trainEpochs     = 12
	learnRate       = 0.2
	l2Lambda        = 1e-4
	holdoutFraction = 0.2
)

// TrainOutcome is one training run's result. Model is nil whenever
// the activation gate refused; Reason always carries the
// human-readable summary the app's log line reports.
type TrainOutcome struct {
	Model           *Model
	Picks           int     // usable (joined) picks found
	HoldoutModel    float64 // model pairwise accuracy on the holdout
	HoldoutBaseline float64 // logged-order accuracy on the same pairs
	Reason          string
}

// Train runs the full deterministic pipeline over the log's
// impressions (oldest first; ReadLogFile's order) and applies the
// activation gate. It never fails: any refusal is an inert outcome
// with the reason spelled out.
func Train(imps []Impression) TrainOutcome {
	usable := usableImpressions(imps)
	if len(usable) < MinPicks {
		return TrainOutcome{
			Picks:  len(usable),
			Reason: fmt.Sprintf("inactive: %d joined picks in the log, %d required", len(usable), MinPicks),
		}
	}
	// Time split: oldest 80% trains, newest 20% validates. The reader
	// yields file order (oldest first); a stable TS sort repairs
	// interleaved generations without disturbing equal stamps.
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].TS.Before(usable[j].TS) })
	nHold := int(math.Ceil(float64(len(usable)) * holdoutFraction))
	if nHold < 1 {
		nHold = 1
	}
	train, hold := usable[:len(usable)-nHold], usable[len(usable)-nHold:]

	w := trainSGD(train)
	if !finite(w) {
		return TrainOutcome{Picks: len(usable), Reason: "inactive: training produced non-finite weights"}
	}
	mAcc := modelAccuracy(w, hold)
	bAcc := baselineAccuracy(hold)
	out := TrainOutcome{Picks: len(usable), HoldoutModel: mAcc, HoldoutBaseline: bAcc}
	if mAcc <= bAcc {
		out.Reason = fmt.Sprintf(
			"inactive: holdout pairwise accuracy %.3f does not beat the delivered order's %.3f (%d picks)",
			mAcc, bAcc, len(usable))
		return out
	}
	final := trainSGD(usable)
	if !finite(final) {
		out.Reason = "inactive: full-data training produced non-finite weights"
		return out
	}
	out.Model = &Model{w: final, picks: len(usable), holdoutModel: mAcc, holdoutBase: bAcc}
	out.Reason = fmt.Sprintf(
		"active: %d picks, holdout pairwise accuracy %.3f vs delivered order %.3f",
		len(usable), mAcc, bAcc)
	return out
}

// usableImpressions filters to the records training may consume:
// joined (real feature values), at least two rows (a pair needs a
// negative), and an in-range pick.
func usableImpressions(imps []Impression) []Impression {
	out := make([]Impression, 0, len(imps))
	for _, imp := range imps {
		if !imp.Joined || len(imp.Rows) < 2 || imp.Picked < 0 || imp.Picked >= len(imp.Rows) {
			continue
		}
		out = append(out, imp)
	}
	return out
}

// trainSGD fits one weight vector by pairwise logistic SGD:
// minimize log(1+exp(-(w.(x_picked - x_other)))) over every
// (picked, other) pair of every impression, with L2 regularization.
// Deterministic: the fixed-seed generator drives the per-epoch
// impression order and nothing else does.
func trainSGD(imps []Impression) []float64 {
	w := make([]float64, FeatureDim)
	rng := rand.New(rand.NewSource(trainSeed))
	// One reusable dense-vector slab keeps the per-epoch featurize
	// pass allocation-free past the first impression of maximal size.
	var slab [][]float64
	for ep := 0; ep < trainEpochs; ep++ {
		for _, ii := range rng.Perm(len(imps)) {
			imp := imps[ii]
			slab = fillFeatures(slab, imp.Rows)
			xp := slab[imp.Picked]
			for j := range imp.Rows {
				if j == imp.Picked {
					continue
				}
				xj := slab[j]
				var z float64
				for k := range xp {
					z += w[k] * (xp[k] - xj[k])
				}
				g := learnRate * sigmoid(-z)
				decay := 1 - learnRate*l2Lambda
				for k := range w {
					w[k] = w[k]*decay + g*(xp[k]-xj[k])
				}
			}
		}
	}
	return w
}

// fillFeatures fills the reusable slab's first len(rows) vectors with
// the rows' dense features, growing the slab as needed, and returns
// it at FULL length (callers index the first len(rows) entries).
func fillFeatures(slab [][]float64, rows []Row) [][]float64 {
	for len(slab) < len(rows) {
		slab = append(slab, make([]float64, FeatureDim))
	}
	for i, r := range rows {
		x := slab[i]
		for k := range x {
			x[k] = 0
		}
		visitFeatures(r, func(idx int, v float64) { x[idx] = v })
	}
	return slab
}

// modelAccuracy is the fraction of holdout (picked, other) pairs the
// model orders correctly (picked strictly above; ties lose).
func modelAccuracy(w []float64, hold []Impression) float64 {
	var correct, total float64
	var slab [][]float64
	for _, imp := range hold {
		slab = fillFeatures(slab, imp.Rows)
		scores := make([]float64, len(imp.Rows))
		for i := range imp.Rows {
			var s float64
			for k, v := range slab[i] {
				s += w[k] * v
			}
			scores[i] = s
		}
		for j := range imp.Rows {
			if j == imp.Picked {
				continue
			}
			total++
			if scores[imp.Picked] > scores[j] {
				correct++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return correct / total
}

// baselineAccuracy scores the delivered flat order on the same
// pairs: a pair is correct when the picked row was ranked above the
// other row (rank == slice index by the log contract).
func baselineAccuracy(hold []Impression) float64 {
	var correct, total float64
	for _, imp := range hold {
		for j := range imp.Rows {
			if j == imp.Picked {
				continue
			}
			total++
			if imp.Picked < j {
				correct++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return correct / total
}

// sigmoid is the standard logistic function.
func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// finite reports whether every weight is a finite number.
func finite(w []float64) bool {
	for _, v := range w {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
