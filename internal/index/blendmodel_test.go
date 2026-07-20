package index

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// The learned-arbitration seam (Blend.Model, wired from
// internal/arbiter by the app layer): invariants extending the
// blendprior pin family -- an absent/inert model changes nothing, the
// model term reorders within an effective class only, and it composes
// with the Prior and Trace seams without either knowing of it.

// TestBlendModelAbsentIsNoOp: a Model alone activates the blend, and
// a resolver answering nil for every query -- the activation-gate-
// unpassed production state -- is byte-identical to the plain engine
// across every query mode.
func TestBlendModelAbsentIsNoOp(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	queries := []string{
		"report", "zqxr", "data report", "bench/data", "zzqx_marker_0.bin",
	}
	zeroSig := &Blend{
		WeightFrecency: 1, WeightRecency: 1, WeightNoise: 1, TierJump: 3,
	}
	require.False(t, zeroSig.active(), "nil Model must not activate a zero-signal blend")

	nilResolver := &Blend{Model: func(string) func(string, ResultSignals) float64 { return nil }}
	require.True(t, nilResolver.active(), "a Model alone activates the blend")
	for _, q := range queries {
		want := st.Query(q, 50)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Blend: nilResolver}),
			"nil-resolving model must be a no-op for %q", q)
	}
}

// TestBlendModelZeroIsNoOp: a resolver whose per-candidate func
// returns 0 for everything orders byte-identically to no model at
// all -- with and without live frecency signals.
func TestBlendModelZeroIsNoOp(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	zero := func(string) func(string, ResultSignals) float64 {
		return func(string, ResultSignals) float64 { return 0 }
	}
	now := time.Now()
	for _, q := range []string{"report", "zqxr", "data report", "bench/data"} {
		require.Equal(t, st.Query(q, 50),
			st.QueryWith(q, 50, QueryOptions{Blend: &Blend{Model: zero}}),
			"zero model alone must order like the plain engine for %q", q)

		withSignals := &Blend{
			Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/bench/data_7.bin": 2})},
			WeightFrecency: 1,
			Now:            func() time.Time { return now },
		}
		withBoth := *withSignals
		withBoth.Model = zero
		require.Equal(t,
			st.QueryWith(q, 50, QueryOptions{Blend: withSignals}),
			st.QueryWith(q, 50, QueryOptions{Blend: &withBoth}),
			"zero model must not disturb a live blend for %q", q)
	}
}

// TestBlendModelOrdersWithinClass: the model delta reorders
// candidates inside one match class and NEVER lifts a candidate past
// a better class, however large the value -- effClass stays the
// primary sort key, so the caller-side clamp is defense in depth, not
// the class guarantee.
func TestBlendModelOrdersWithinClass(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)         // exact
	mustAdd(t, s, "/a", "report_one.txt", false) // prefix
	mustAdd(t, s, "/a", "report_two.txt", false) // prefix
	model := &Blend{Model: func(q string) func(string, ResultSignals) float64 {
		return func(path string, _ ResultSignals) float64 {
			if path == "/a/report_two.txt" {
				return 1000 // absurd on purpose: still no class lift
			}
			return 0
		}
	}}
	require.Equal(t, []string{"/a/report", "/a/report_one.txt", "/a/report_two.txt"},
		resultPaths(s.Query("report", 10)), "pre-model order")
	require.Equal(t, []string{"/a/report", "/a/report_two.txt", "/a/report_one.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: model})),
		"the model reorders within the prefix class; the exact match stays on top")
}

// TestBlendModelReceivesQueryAndSignals: the resolver runs once per
// query with the raw query string, and the per-candidate func sees
// the path plus the candidate's ranking components as they
// participated.
func TestBlendModelReceivesQueryAndSignals(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)
	mustAdd(t, s, "/a", "report_one.txt", false)
	var mu sync.Mutex
	var queries []string
	sigs := map[string]ResultSignals{}
	b := &Blend{Model: func(q string) func(string, ResultSignals) float64 {
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		return func(path string, sig ResultSignals) float64 {
			mu.Lock()
			sigs[path] = sig
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
	exact := sigs["/a/report"]
	require.Equal(t, "/a/report", exact.Path)
	require.Equal(t, uint8(0), exact.Class, "the exact match's class reaches the model")
	require.Equal(t, exact.Class, exact.EffClass)
	require.False(t, exact.IsDir)
	prefix := sigs["/a/report_one.txt"]
	require.Equal(t, uint8(1), prefix.Class)
}

// TestBlendModelComposesWithPriorAndTrace: all three per-query seams
// ride ONE blend copy -- the prior and model terms both apply
// (additively), and a requested trace still fills one ResultSignals
// per delivered row in the delivered order.
func TestBlendModelComposesWithPriorAndTrace(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report_one.txt", false)
	mustAdd(t, s, "/a", "report_two.txt", false)
	mustAdd(t, s, "/a", "report_three.txt", false)
	b := &Blend{
		// The prior lifts _two by a lot; the model lifts _three by
		// more: both terms must be live in the same query.
		Prior: func(string) func(string) float64 {
			return func(path string) float64 {
				if path == "/a/report_two.txt" {
					return 5
				}
				return 0
			}
		},
		Model: func(string) func(string, ResultSignals) float64 {
			return func(path string, _ ResultSignals) float64 {
				if path == "/a/report_three.txt" {
					return 8
				}
				return 0
			}
		},
	}
	var trace []ResultSignals
	res := s.QueryWith("report", 10, QueryOptions{Blend: b, Trace: &trace})
	require.Equal(t, []string{"/a/report_three.txt", "/a/report_two.txt", "/a/report_one.txt"},
		resultPaths(res), "model and prior terms compose additively")
	require.Len(t, trace, len(res))
	for i, sig := range trace {
		require.Equal(t, res[i].Path, sig.Path, "trace rows parallel the delivered order")
	}
}

// TestBlendModelMergedSetOnly mirrors TestBlendPriorMergedSetOnly:
// recall is decided before the blend, so the model cannot resurrect
// a candidate the per-shard heaps already pruned.
func TestBlendModelMergedSetOnly(t *testing.T) {
	s := NewStore()
	for _, n := range []string{"aa", "bb", "cc"} {
		mustAdd(t, s, "/a", n+"_report.txt", false)
	}
	mk := func(path string) *Blend {
		return &Blend{Model: func(string) func(string, ResultSignals) float64 {
			return func(p string, _ ResultSignals) float64 {
				if p == path {
					return 100
				}
				return 0
			}
		}}
	}
	// A model term on a merge survivor reorders the delivered pair...
	require.Equal(t, []string{"/a/bb_report.txt", "/a/aa_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/bb_report.txt")})))
	// ...while the shard-pruned candidate stays invisible however
	// large its delta (pruning is pre-blend, the documented contract).
	require.Equal(t, []string{"/a/aa_report.txt", "/a/bb_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/cc_report.txt")})))
}
