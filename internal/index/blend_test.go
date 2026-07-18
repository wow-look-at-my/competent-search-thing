package index

import (
	"io/fs"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// blendTestStore builds a memory-only frecency store on a fixed clock
// and records path open counts.
func blendTestStore(t *testing.T, now time.Time, opens map[string]int) *frecency.Store {
	t.Helper()
	st := frecency.New("", frecency.Options{Now: func() time.Time { return now }})
	for p, n := range opens {
		for i := 0; i < n; i++ {
			require.NoError(t, st.RecordOpen(p))
		}
	}
	return st
}

func resultPaths(res []Result) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Path
	}
	return out
}

// TestBlendInactiveIsNoOp pins the no-op contract byte-for-byte: a nil
// blend, a blend with zero-value Signals (the config disabled=true
// shape), and the plain Query path return IDENTICAL result slices for
// every query mode -- name, fuzzy, multi-term, path, non-ASCII --
// against a large synthetic store.
func TestBlendInactiveIsNoOp(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	queries := []string{
		"report",            // substring-heavy name mode
		"zqxr",              // fuzzy-selective (subsequence-only hits)
		"data report",       // multi-term
		"bench/data",        // path mode
		"caf\u00e9",         // non-ASCII rune regime
		"zzqx_marker_0.bin", // exact
	}
	zeroSig := &Blend{
		WeightFrecency: 1, WeightRecency: 1, WeightNoise: 1, TierJump: 3,
	}
	require.False(t, zeroSig.active(), "zero-value Signals must deactivate the blend")
	require.False(t, (*Blend)(nil).active())
	for _, q := range queries {
		want := st.Query(q, 50)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Blend: nil}), "nil blend must be a no-op for %q", q)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Blend: zeroSig}), "zero-signal blend must be a no-op for %q", q)
	}
}

// TestBlendActiveWithoutSignalsMatchesInactive pins the next-strongest
// property: an ACTIVE blend (a store is wired) whose signals are all
// silent -- empty store, no probe, no cwd, noise weight off -- orders
// byte-identically to the inactive engine, fuzzy alignment ordering
// included (the blended score degenerates to the alignment key).
func TestBlendActiveWithoutSignalsMatchesInactive(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	now := time.Now()
	b := &Blend{
		Signals:        frecency.Signals{Store: blendTestStore(t, now, nil)},
		WeightFrecency: 1,
		WeightRecency:  1,
		WeightNoise:    -1, // off: PathPenalty is live even with an empty store
		TierJump:       3,
		Now:            func() time.Time { return now },
	}
	require.True(t, b.active())
	for _, q := range []string{"report", "zqxr", "data report", "bench/data"} {
		require.Equal(t, st.Query(q, 50), st.QueryWith(q, 50, QueryOptions{Blend: b}),
			"silent active blend must order like the inactive engine for %q", q)
	}
}

// TestBlendBoostOrdersWithinTier: within one match class the decayed
// open count reorders candidates that pre-blend tie-breaks would order
// the other way.
func TestBlendBoostOrdersWithinTier(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report_one.txt", false)
	mustAdd(t, s, "/a", "report_two.txt", false)
	now := time.Now()
	b := &Blend{
		Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/a/report_two.txt": 2})},
		WeightFrecency: 1,
		WeightNoise:    1,
		TierJump:       3,
		Now:            func() time.Time { return now },
	}
	// Pre-blend: _one before _two (lexicographic). Boost flips it.
	require.Equal(t, []string{"/a/report_one.txt", "/a/report_two.txt"}, resultPaths(s.Query("report", 10)))
	require.Equal(t, []string{"/a/report_two.txt", "/a/report_one.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: b})))

	// A negative frecency weight disables the signal: pre-blend order.
	off := *b
	off.WeightFrecency = -1
	require.Equal(t, []string{"/a/report_one.txt", "/a/report_two.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: &off})))
}

// TestBlendTierJump: a candidate whose decayed open count exceeds the
// threshold competes exactly ONE class up -- an often-opened substring
// match outranks an untouched prefix match but never an exact match
// two tiers above.
func TestBlendTierJump(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)       // exact
	mustAdd(t, s, "/a", "reports.txt", false)  // prefix
	mustAdd(t, s, "/a", "myreport.txt", false) // substring
	now := time.Now()
	mk := func(opens int, tierJump float64) *Blend {
		return &Blend{
			Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/a/myreport.txt": opens})},
			WeightFrecency: 1,
			WeightNoise:    1,
			TierJump:       tierJump,
			Now:            func() time.Time { return now },
		}
	}

	// Boost 4 > threshold 3: the substring match competes in the
	// prefix class and its blended score wins there; exact stays on
	// top (one class up, never two).
	require.Equal(t, []string{"/a/report", "/a/myreport.txt", "/a/reports.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: mk(4, 3)})))

	// Boost 2 <= threshold: no jump, class order untouched.
	require.Equal(t, []string{"/a/report", "/a/reports.txt", "/a/myreport.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: mk(2, 3)})))

	// A negative threshold disables jumping even for huge counts.
	require.Equal(t, []string{"/a/report", "/a/reports.txt", "/a/myreport.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: mk(50, -1)})))
}

// TestBlendNoisePenalty: same class, same depth apart from the noise
// component -- the clean location wins; disabling the weight restores
// the pre-blend order.
func TestBlendNoisePenalty(t *testing.T) {
	s := NewStore()
	// Both dirs are 12 characters (equal path length) and the noise
	// dir sorts lexicographically FIRST, so the pre-blend engine
	// ranks it first and the flip below is the penalty's doing.
	mustAdd(t, s, "/home/u/node_modules", "report_a.txt", false)
	mustAdd(t, s, "/home/u/workspace_ab", "report_b.txt", false)
	now := time.Now()
	b := &Blend{
		Signals:     frecency.Signals{Store: blendTestStore(t, now, nil)},
		WeightNoise: 1,
		Now:         func() time.Time { return now },
	}
	require.Equal(t, []string{"/home/u/node_modules/report_a.txt", "/home/u/workspace_ab/report_b.txt"},
		resultPaths(s.Query("report", 10)), "pre-blend: equal length, lexicographic")
	require.Equal(t, []string{"/home/u/workspace_ab/report_b.txt", "/home/u/node_modules/report_a.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: b})))

	off := *b
	off.WeightNoise = -1
	require.Equal(t, resultPaths(s.Query("report", 10)),
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: &off})))
}

// TestBlendCwdBoost: results under the focused app's working directory
// rank up; the weight lives in Signals.CwdWeight and <= 0 disables it
// inside CwdBoost itself.
func TestBlendCwdBoost(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/home/u/aaa", "report_a.txt", false)
	mustAdd(t, s, "/home/u/proj", "report_b.txt", false)
	now := time.Now()
	b := &Blend{
		Signals: frecency.Signals{
			Store:     blendTestStore(t, now, nil),
			Cwd:       "/home/u/proj",
			CwdWeight: 1,
		},
		Now: func() time.Time { return now },
	}
	require.Equal(t, []string{"/home/u/aaa/report_a.txt", "/home/u/proj/report_b.txt"},
		resultPaths(s.Query("report", 10)), "pre-blend: lexicographic")
	require.Equal(t, []string{"/home/u/proj/report_b.txt", "/home/u/aaa/report_a.txt"},
		resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: b})))
}

// fakeInfo is a minimal os.FileInfo whose Sys is nil, so fileRecency
// falls back to ModTime on every OS.
type fakeInfo struct{ mod time.Time }

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// TestBlendRecencyColdOnly: the recency stat pass runs for candidates
// WITHOUT open history only -- a just-touched cold file ranks above an
// untouched one, and a boosted path is never statted.
func TestBlendRecencyColdOnly(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report_hot.txt", false)   // boosted: must not be statted
	mustAdd(t, s, "/a", "report_old.txt", false)   // cold, touched a month ago
	mustAdd(t, s, "/a", "report_fresh.txt", false) // cold, touched now
	now := time.Now()
	var mu sync.Mutex
	statted := map[string]int{}
	mtimes := map[string]time.Time{
		"/a/report_old.txt":   now.Add(-29 * 24 * time.Hour),
		"/a/report_fresh.txt": now.Add(-time.Minute),
	}
	probe := frecency.NewProbe(frecency.ProbeOptions{
		Lstat: func(path string) (os.FileInfo, error) {
			mu.Lock()
			statted[path]++
			mu.Unlock()
			if m, ok := mtimes[path]; ok {
				return fakeInfo{mod: m}, nil
			}
			return nil, fs.ErrNotExist
		},
		Now: func() time.Time { return now },
	})
	b := &Blend{
		Signals: frecency.Signals{
			Store: blendTestStore(t, now, map[string]int{"/a/report_hot.txt": 1}),
			Probe: probe,
		},
		WeightFrecency: 1,
		WeightRecency:  1,
		RecencyBudget:  time.Second, // generous: the fake stats are instant
		Now:            func() time.Time { return now },
	}
	got := resultPaths(s.QueryWith("report", 10, QueryOptions{Blend: b}))
	// hot: boost 1.0; fresh: recency ~1.0 minus epsilon; old: ~0.03.
	require.Equal(t, []string{"/a/report_hot.txt", "/a/report_fresh.txt", "/a/report_old.txt"}, got)
	mu.Lock()
	defer mu.Unlock()
	require.NotContains(t, statted, "/a/report_hot.txt", "boosted paths must not be statted")
	require.Contains(t, statted, "/a/report_old.txt")
	require.Contains(t, statted, "/a/report_fresh.txt")

	// A negative recency weight skips the stat pass entirely.
	probe2Calls := 0
	b2 := *b
	b2.WeightRecency = -1
	b2.Signals.Probe = frecency.NewProbe(frecency.ProbeOptions{
		Lstat: func(string) (os.FileInfo, error) { probe2Calls++; return nil, fs.ErrNotExist },
	})
	_ = s.QueryWith("report", 10, QueryOptions{Blend: &b2})
	require.Zero(t, probe2Calls, "recency off must run no stats")
}

// TestBlendPathMode: the blend applies to path-mode queries through
// the same selectTop stage.
func TestBlendPathMode(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/proj/a", "log.txt", false)
	mustAdd(t, s, "/proj/b", "log.txt", false)
	now := time.Now()
	b := &Blend{
		Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/proj/b/log.txt": 2})},
		WeightFrecency: 1,
		Now:            func() time.Time { return now },
	}
	// "/proj" is a path-mode query (separator); both full paths prefix-
	// match with the same class and path length.
	require.Equal(t, []string{"/proj/a/log.txt", "/proj/b/log.txt"}, resultPaths(s.Query("/proj", 10)))
	require.Equal(t, []string{"/proj/b/log.txt", "/proj/a/log.txt"},
		resultPaths(s.QueryWith("/proj", 10, QueryOptions{Blend: b})))
}

// TestBlendMergedSetOnly pins the documented top-K constraint: the
// blend reorders the candidates that survived the per-shard pre-blend
// heaps -- a boosted candidate INSIDE the merged set rises past the
// limit cut, but one pruned before the merge cannot be resurrected.
func TestBlendMergedSetOnly(t *testing.T) {
	s := NewStore()
	for _, n := range []string{"aa", "bb", "cc"} {
		mustAdd(t, s, "/a", n+"_report.txt", false)
	}
	now := time.Now()
	mk := func(path string) *Blend {
		return &Blend{
			Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{path: 2})},
			WeightFrecency: 1,
			Now:            func() time.Time { return now },
		}
	}
	// limit 2: the single shard's heap keeps the pre-blend best two
	// (aa, bb). A boost on bb reorders them...
	require.Equal(t, []string{"/a/bb_report.txt", "/a/aa_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/bb_report.txt")})))
	// ...while cc, pruned by the heap before the blend ever ran,
	// stays invisible however boosted (the documented constraint).
	require.Equal(t, []string{"/a/aa_report.txt", "/a/bb_report.txt"},
		resultPaths(s.QueryWith("report", 2, QueryOptions{Blend: mk("/a/cc_report.txt")})))
}

// TestRecencyScore pins the documented shape of the age mapping: 1 at
// or before zero, log-decay through the hour/day/week bands, 0 at the
// 30-day horizon and beyond, monotonically non-increasing.
func TestRecencyScore(t *testing.T) {
	require.Equal(t, 1.0, recencyScore(0))
	require.Equal(t, 1.0, recencyScore(-time.Hour))
	require.InDelta(t, 0.895, recencyScore(time.Hour), 0.01)
	require.InDelta(t, 0.51, recencyScore(24*time.Hour), 0.01)
	require.InDelta(t, 0.22, recencyScore(7*24*time.Hour), 0.01)
	require.InDelta(t, 0.0, recencyScore(30*24*time.Hour), 1e-9)
	require.Equal(t, 0.0, recencyScore(90*24*time.Hour))
	prev := 2.0
	for h := 0; h <= 24*31; h++ {
		v := recencyScore(time.Duration(h) * time.Hour)
		require.LessOrEqual(t, v, prev, "recencyScore must be non-increasing (h=%d)", h)
		prev = v
	}
}

// TestBlendFuzzyAlignmentStillOrders: within the fuzzy class the
// alignment score keeps ordering signal-less candidates (the blended
// score carries score/blendAlignDivisor), and a recorded-open delta
// on the worse-aligned candidate outweighs it.
func TestBlendFuzzyAlignmentStillOrders(t *testing.T) {
	s := NewStore()
	// Query "rpt": both names hold r..p..t as a subsequence but never
	// contiguously (classFuzzy). The boundary-bonused zz_ name aligns
	// far better than the scattered aa_ one, and lexicographic order
	// DISAGREES with alignment order, so the pre-blend ranking below
	// proves alignment decides.
	mustAdd(t, s, "/a", "aa_rxxxpxxxt.txt", false) // scattered: low alignment, lex first
	mustAdd(t, s, "/a", "zz_r_p_t_run.txt", false) // boundary matches: high alignment
	pre := resultPaths(s.Query("rpt", 10))
	require.Equal(t, []string{"/a/zz_r_p_t_run.txt", "/a/aa_rxxxpxxxt.txt"}, pre,
		"pre-blend: the better-aligned name ranks first despite sorting lexicographically later")

	now := time.Now()
	silent := &Blend{
		Signals: frecency.Signals{Store: blendTestStore(t, now, nil)},
		Now:     func() time.Time { return now },
	}
	require.Equal(t, pre, resultPaths(s.QueryWith("rpt", 10, QueryOptions{Blend: silent})),
		"no signals: alignment ordering preserved through the blended score")

	boosted := &Blend{
		Signals:        frecency.Signals{Store: blendTestStore(t, now, map[string]int{"/a/aa_rxxxpxxxt.txt": 2})},
		WeightFrecency: 1,
		Now:            func() time.Time { return now },
	}
	require.Equal(t, []string{"/a/aa_rxxxpxxxt.txt", "/a/zz_r_p_t_run.txt"},
		resultPaths(s.QueryWith("rpt", 10, QueryOptions{Blend: boosted})),
		"two recorded opens outweigh the alignment difference")
}
