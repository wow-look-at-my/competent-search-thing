package index

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// The pick-memory prior seam (Blend.Prior, wired from internal/priors
// by the app layer): invariants mirroring the blend's own pins --
// absent/zero priors change nothing, priors reorder within a class
// only, and pruning stays pre-blend.

// TestBlendPriorAbsentIsNoOp extends the inactive-blend pin: a nil
// Prior keeps a zero-signal blend inactive (the exact pre-blend
// path), and a Prior whose resolver returns nil for every query adds
// no term -- byte-identical results across every query mode.
func TestBlendPriorAbsentIsNoOp(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	queries := []string{
		"report", "zqxr", "data report", "bench/data", "zzqx_marker_0.bin",
	}
	zeroSig := &Blend{
		WeightFrecency: 1, WeightRecency: 1, WeightNoise: 1, TierJump: 3,
	}
	require.False(t, zeroSig.active(), "nil Prior must not activate a zero-signal blend")

	nilResolver := &Blend{Prior: func(string) func(string) float64 { return nil }}
	require.True(t, nilResolver.active(), "a Prior alone activates the blend")
	for _, q := range queries {
		want := st.Query(q, 50)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Blend: nilResolver}),
			"nil-resolving prior must be a no-op for %q", q)
	}
}

// TestBlendPriorZeroIsNoOp: a resolver that answers every query with
// an all-zero lookup orders byte-identically to no prior at all --
// with and without live frecency signals.
func TestBlendPriorZeroIsNoOp(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	zero := func(string) func(string) float64 {
		return func(string) float64 { return 0 }
	}
	now := time.Now()
	for _, q := range []string{"report", "zqxr", "data report", "bench/data"} {
		require.Equal(t, st.Query(q, 50),
			st.QueryWith(q, 50, QueryOptions{Blend: &Blend{Prior: zero}}),
			"zero prior alone must order like the plain engine for %q", q)

		withSignals := &Blend{
			Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/bench/data_7.bin": 2})},
			WeightFrecency: 1,
			Now:            func() time.Time { return now },
		}
		withBoth := *withSignals
		withBoth.Prior = zero
		require.Equal(t,
			st.QueryWith(q, 50, QueryOptions{Blend: withSignals}),
			st.QueryWith(q, 50, QueryOptions{Blend: &withBoth}),
			"zero prior must not disturb a live blend for %q", q)
	}
}

// TestBlendPriorOrdersWithinClass: the prior term reorders candidates
// inside one match class and NEVER lifts a candidate past a better
// class, however large the value (no tier jump for priors).
func TestBlendPriorOrdersWithinClass(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)         // exact
	mustAdd(t, s, "/a", "report_one.txt", false) // prefix
	mustAdd(t, s, "/a", "report_two.txt", false) // prefix
	prior := &Blend{Prior: func(q string) func(string) float64 {
		return func(path string) float64 {
			if path == "/a/report_two.txt" {
				return 1000 // absurd on purpose: still no class lift
			}
			return 0
		}
	}}
	require.Equal(t, []string{"/a/report", "/a/report_one.txt", "/a/report_two.txt"},
		resultPaths(s.Query("report", 10)), "pre-prior order")
	require.Equal(t, []string{"/a/report", "/a/report_two.txt", "/a/report_one.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: prior})),
		"the prior reorders within the prefix class; the exact match stays on top")
}

// TestBlendPriorReceivesQuery: the resolver runs once per query with
// the raw query string, and the returned lookup sees candidate paths.
func TestBlendPriorReceivesQuery(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report_one.txt", false)
	mustAdd(t, s, "/a", "report_two.txt", false)
	var mu sync.Mutex
	var queries []string
	paths := map[string]int{}
	b := &Blend{Prior: func(q string) func(string) float64 {
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		return func(path string) float64 {
			mu.Lock()
			paths[path]++
			mu.Unlock()
			return 0
		}
	}}
	_ = s.QueryWith("report", 10, QueryOptions{Blend: b})
	_ = s.QueryWith("  report  ", 10, QueryOptions{Blend: b})
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"report", "  report  "}, queries,
		"one resolution per query, raw query string")
	require.Contains(t, paths, "/a/report_one.txt")
	require.Contains(t, paths, "/a/report_two.txt")
}

// TestBlendPriorMergedSetOnly mirrors TestBlendMergedSetOnly for the
// prior term: recall is decided before the blend, so a prior cannot
// resurrect a candidate the per-shard heaps already pruned.
func TestBlendPriorMergedSetOnly(t *testing.T) {
	s := NewStore()
	for _, n := range []string{"aa", "bb", "cc"} {
		mustAdd(t, s, "/a", n+"_report.txt", false)
	}
	mk := func(path string) *Blend {
		return &Blend{Prior: func(string) func(string) float64 {
			return func(p string) float64 {
				if p == path {
					return 100
				}
				return 0
			}
		}}
	}
	// A prior on a merge survivor reorders the delivered pair...
	require.Equal(t, []string{"/a/bb_report.txt", "/a/aa_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/bb_report.txt")})))
	// ...while the shard-pruned candidate stays invisible however
	// large its prior (pruning is pre-blend, the documented contract).
	require.Equal(t, []string{"/a/aa_report.txt", "/a/bb_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/cc_report.txt")})))
}
