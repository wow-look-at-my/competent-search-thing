package frecency

import (
	"context"
	"time"
)

// Signals bundles the ranking sources for the engine blend (the NEXT
// phase -- nothing consumes this yet). The intended semantics,
// implemented in the engine, are in the package doc: match quality +
// Boost + cold-start Recency + CwdBoost - Penalty, with a strong
// Boost allowed to jump ranking tiers. The zero value (nil Store,
// nil Probe, empty Cwd) degrades to no-ops -- Boost 0, CwdBoost 0,
// Recency nil, Penalty still pure and live -- the appctx.Cache
// pattern, so the engine never nil-checks.
type Signals struct {
	Store *Store
	Probe *Probe
	// Cwd is the focused app's derived working directory (DeriveCwd
	// at capture time; "" = none) and CwdWeight the score value of a
	// direct-child hit (0 = disabled).
	Cwd       string
	CwdWeight float64
}

// Boost is Store.Boost: the path's decayed open count, 0 without a
// store or history.
func (s Signals) Boost(path string) float64 {
	return s.Store.Boost(path)
}

// Penalty is PathPenalty: the pure location-noise score in
// [0, PenaltyMax].
func (s Signals) Penalty(path string) float64 {
	return PathPenalty(path)
}

// Recency is Probe.BatchRecency: per-path max(atime, mtime) within
// budget, nil without a probe.
func (s Signals) Recency(ctx context.Context, paths []string, budget time.Duration) map[string]time.Time {
	return s.Probe.BatchRecency(ctx, paths, budget)
}

// CwdBoost is the package-level CwdBoost over the captured Cwd and
// configured CwdWeight: 0 without both.
func (s Signals) CwdBoost(path string) float64 {
	return CwdBoost(path, s.Cwd, s.CwdWeight)
}
