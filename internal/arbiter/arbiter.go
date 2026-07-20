// Package arbiter holds the PURE half of learned composition
// arbitration: a small pairwise-trained logistic model, learned from
// the user's own recorded picks, that decides which SOURCE a query
// means when the same name matches a file, a browser tab, and an app
// (config search.arbiter -- opt-in, zero value = OFF, the
// preview.enabled privacy precedent; the app wiring lives in
// internal/app arbiter.go).
//
// Recall stays deterministic: the engine and the plugin registry
// produce exactly today's rows, and the model only re-orders/places
// what was already delivered, before paint, through two seams:
//
//   - Within the file list: a clamped additive delta on the index
//     blend (internal/index Blend.Model, the Blend.Prior pattern).
//     The delta is bounded by FileDeltaClamp, strictly below the
//     blend's own one-class-band equivalence (the tier-jump
//     threshold), and class inversion is additionally impossible by
//     construction -- the blend orders by effective class first.
//   - Across sources: the app layer scores each plugin emission's
//     rows, re-orders rows within their section by model score, and
//     may promote a section above the file results when its best row
//     outscores the best file row of the same query.
//
// Training is PAIRWISE SGD on the telemetry log's pick records (the
// picked row should outscore every other shown row of the same
// impression), deterministic given the log (fixed seed, fixed
// hyperparameters), with an ACTIVATION GATE: the model participates
// only past MinPicks joined picks AND a time-split holdout check --
// oldest 80% trains, newest 20% validates, and the model must beat
// the logged baseline order's pairwise accuracy. Below the gate, or
// on any read/train failure, the model is nil and everything
// degrades to today's exact behavior.
//
// Conventions mirror internal/priors: pure stdlib, RWMutex store,
// nil-receiver/zero-value no-ops, immutable swapped state, tolerant
// log reading (the on-disk JSONL format is the data contract).
package arbiter

import (
	"fmt"
	"math"
	"sync"
)

// FileDeltaClamp bounds the within-file-list model delta in blend
// units. The blend's primary sort key is the effective match class,
// so NO additive term can invert a class decision structurally; the
// clamp additionally keeps the model's within-class say strictly
// below one class band's worth of signal -- the blend equates one
// class promotion with a decayed open count above the tier-jump
// threshold (index.DefaultBlendTierJump = 3.0), and the clamp sits
// strictly under it (about two recorded opens' worth of boost, and
// well below the exact-query prior's 6.0 saturation).
const FileDeltaClamp = 2.0

// Model is one immutable trained generation: a single weight vector
// over the FeatureDim features plus the training metadata the app's
// one log line reports. Build with Train (production) or NewModel
// (tests); never mutate a published Model.
type Model struct {
	w            []float64
	picks        int
	holdoutModel float64
	holdoutBase  float64
}

// NewModel wraps a raw weight vector (tests and future persistence).
// The vector must have exactly FeatureDim finite entries.
func NewModel(w []float64) (*Model, error) {
	if len(w) != FeatureDim {
		return nil, fmt.Errorf("arbiter: weight vector has %d entries, want %d", len(w), FeatureDim)
	}
	cp := make([]float64, len(w))
	for i, v := range w {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("arbiter: weight %d is not finite", i)
		}
		cp[i] = v
	}
	return &Model{w: cp}, nil
}

// Score is the model's full pick-preference score for one row --
// comparable across rows of the SAME impression only (the pairwise
// objective learns differences, not absolutes). Nil-safe: a nil
// model scores everything 0.
func (m *Model) Score(r Row) float64 {
	if m == nil {
		return 0
	}
	return m.dotRange(r, 0, FeatureDim)
}

// FileDelta is the within-file-list blend term for one file row: the
// model's score over the file-VARYING feature block only (class,
// alignment, boost, recency, cwd, penalty, isDir, depth, extension),
// clamped to [-FileDeltaClamp, +FileDeltaClamp]. Per-query constants
// (bias, kind, the query-shape crosses) are excluded -- they shift
// every file row equally, so including them would only eat the
// clamp's headroom without ever changing the order. Nil-safe.
func (m *Model) FileDelta(r Row) float64 {
	if m == nil {
		return 0
	}
	v := m.dotRange(r, fileVaryLo, fileVaryHi)
	if v > FileDeltaClamp {
		return FileDeltaClamp
	}
	if v < -FileDeltaClamp {
		return -FileDeltaClamp
	}
	return v
}

// dotRange is the sparse dot product of the weights against r's
// features with indices in [lo, hi) -- no allocation, the serve-path
// shape (its parity with the dense trainer vectors is pinned by
// TestVisitMatchesFeaturize).
func (m *Model) dotRange(r Row, lo, hi int) float64 {
	var s float64
	visitFeatures(r, func(i int, v float64) {
		if i >= lo && i < hi {
			s += m.w[i] * v
		}
	})
	return s
}

// Picks reports how many joined picks trained this model.
func (m *Model) Picks() int {
	if m == nil {
		return 0
	}
	return m.picks
}

// HoldoutAccuracy reports the activation gate's evidence: the
// model's pairwise accuracy on the newest-20% holdout and the logged
// baseline order's accuracy on the same pairs.
func (m *Model) HoldoutAccuracy() (model, baseline float64) {
	if m == nil {
		return 0, 0
	}
	return m.holdoutModel, m.holdoutBase
}

// Weights returns a defensive copy of the weight vector (tests,
// diagnostics).
func (m *Model) Weights() []float64 {
	if m == nil {
		return nil
	}
	return append([]float64(nil), m.w...)
}

// Store serves the current model generation to the two application
// seams. All methods are safe for concurrent use and
// nil-receiver-safe (a nil *Store -- the feature disabled -- no-ops
// everything). A nil current model means INACTIVE: both seams then
// change nothing at all.
type Store struct {
	mu     sync.RWMutex
	model  *Model
	reason string
}

// NewStore creates an empty (inactive) store.
func NewStore() *Store { return &Store{reason: "not trained yet"} }

// SetOutcome atomically installs a training outcome: the new model
// generation (nil = the gate refused, the store turns/stays
// inactive) and the human-readable reason for the app's log lines.
func (s *Store) SetOutcome(o TrainOutcome) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.model = o.Model
	s.reason = o.Reason
	s.mu.Unlock()
}

// Current returns the active model generation, or nil while the
// activation gate keeps the arbiter inert. Nil-safe.
func (s *Store) Current() *Model {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// Reason returns the latest training outcome's human-readable
// summary (why the model is active or inert). Nil-safe.
func (s *Store) Reason() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reason
}
