package index

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// traceQueries covers every query mode the engine dispatches on:
// substring-heavy name mode, fuzzy-selective, multi-term, path mode,
// the non-ASCII rune regime, and an exact hit.
var traceQueries = []string{
	"report",
	"zqxr",
	"data report",
	"bench/data",
	"caf\u00e9",
	"zzqx_marker_0.bin",
}

// TestTraceNilIsByteIdentical is the seam's no-op pin, next to
// TestBlendInactiveIsNoOp: with Trace nil, QueryWith takes exactly the
// pre-seam code paths -- byte-identical results against the plain
// Query for every query mode, with and without a blend value present.
func TestTraceNilIsByteIdentical(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	for _, q := range traceQueries {
		want := st.Query(q, 50)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Trace: nil}),
			"nil trace must be a no-op for %q", q)
		require.Equal(t, want, st.QueryWith(q, 50, QueryOptions{Blend: nil, Trace: nil}),
			"nil blend + nil trace must be a no-op for %q", q)
	}
}

// TestTraceDoesNotChangeResults pins the other half of the contract: a
// NON-nil trace never changes the returned results or their order --
// inactive blend and active blend alike -- and the buffer parallels
// the results row for row.
func TestTraceDoesNotChangeResults(t *testing.T) {
	st := buildSynthStore(7, 50_000)
	now := time.Now()
	active := &Blend{
		Signals: frecency.Signals{
			Store: blendTestStore(t, now, map[string]int{}),
		},
		WeightFrecency: 1,
		WeightRecency:  1,
		WeightNoise:    1,
		TierJump:       3,
		Now:            func() time.Time { return now },
	}
	require.True(t, active.Active())
	require.False(t, (*Blend)(nil).Active())
	for _, b := range []*Blend{nil, active} {
		for _, q := range traceQueries {
			want := st.QueryWith(q, 50, QueryOptions{Blend: b})
			var trace []ResultSignals
			got := st.QueryWith(q, 50, QueryOptions{Blend: b, Trace: &trace})
			require.Equal(t, want, got,
				"a trace must never change results (blend active=%v, %q)", b.Active(), q)
			require.Len(t, trace, len(got),
				"trace must parallel the results (blend active=%v, %q)", b.Active(), q)
			for i, sig := range trace {
				require.Equal(t, got[i].Path, sig.Path, "trace row %d path (%q)", i, q)
				require.Equal(t, got[i].IsDir, sig.IsDir, "trace row %d isDir (%q)", i, q)
				require.Equal(t, len(sig.Path), int(sig.PathLen), "trace row %d pathLen (%q)", i, q)
			}
		}
	}
}

// TestTraceInactiveFillsBasics: with the blend inactive the trace
// carries class/alignment only -- EffClass == Class and every signal
// component zero (nothing else participated).
func TestTraceInactiveFillsBasics(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/a", "report", false)         // exact
	mustAdd(t, s, "/a", "reports.txt", false)    // prefix
	mustAdd(t, s, "/a", "big_report.txt", false) // substring
	mustAdd(t, s, "/a", "re_port.txt", false)    // fuzzy subsequence

	var trace []ResultSignals
	res := s.QueryWith("report", 10, QueryOptions{Trace: &trace})
	require.Len(t, res, 4)
	require.Len(t, trace, 4)
	require.Equal(t, []uint8{classExact, classPrefix, classSub, classFuzzy},
		[]uint8{trace[0].Class, trace[1].Class, trace[2].Class, trace[3].Class})
	for i, sig := range trace {
		require.Equal(t, sig.Class, sig.EffClass, "no blend, no tier jump (row %d)", i)
		require.Zero(t, sig.Boost, "row %d", i)
		require.Zero(t, sig.Recency, "row %d", i)
		require.Zero(t, sig.Cwd, "row %d", i)
		require.Zero(t, sig.Penalty, "row %d", i)
	}
	require.Positive(t, trace[3].Align, "the fuzzy row carries its alignment score")
	require.Zero(t, trace[0].Align, "non-fuzzy rows carry alignment 0")
}

// TestTraceBlendComponents: with the blend active the trace records
// the components exactly as they participated -- the boosted path's
// decayed count, its tier-jumped effective class, and the noise
// penalty of a noisy location.
func TestTraceBlendComponents(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/home/u/.cache/x", "big_report.txt", false) // substring + noisy
	mustAdd(t, s, "/home/u", "reports.txt", false)             // prefix, boosted past the jump

	now := time.Now()
	b := &Blend{
		Signals: frecency.Signals{
			Store: blendTestStore(t, now, map[string]int{"/home/u/reports.txt": 5}),
		},
		WeightFrecency: 1,
		WeightNoise:    1,
		TierJump:       3,
		Now:            func() time.Time { return now },
	}
	var trace []ResultSignals
	res := s.QueryWith("report", 10, QueryOptions{Blend: b, Trace: &trace})
	require.Len(t, res, 2)
	require.Len(t, trace, 2)

	byPath := map[string]ResultSignals{}
	for _, sig := range trace {
		byPath[sig.Path] = sig
	}
	boosted := byPath["/home/u/reports.txt"]
	require.Greater(t, boosted.Boost, 3.0, "the decayed open count is recorded")
	require.Equal(t, classPrefix, boosted.Class)
	require.Equal(t, classPrefix-1, boosted.EffClass, "the tier jump shows in EffClass")

	noisy := byPath["/home/u/.cache/x/big_report.txt"]
	require.Positive(t, noisy.Penalty, "the location-noise penalty is recorded")
	require.Zero(t, noisy.Boost)
	require.Equal(t, noisy.Class, noisy.EffClass)
}

// TestQueryTracedMatchesQuery pins the Manager-level wrapper: nil
// trace is exactly Query (same defaulted limit), and a non-nil trace
// parallels the returned rows.
func TestQueryTracedMatchesQuery(t *testing.T) {
	m := NewManager([]string{"/"}, nil, 3)
	for i := 0; i < 6; i++ {
		require.NoError(t, m.Add("/d", "report_"+string(rune('a'+i))+".txt", false))
	}
	want := m.Query("report", 0)
	require.Len(t, want, 3, "the configured maxResults default applies")
	require.Equal(t, want, m.QueryTraced("report", 0, nil))

	var trace []ResultSignals
	got := m.QueryTraced("report", 0, &trace)
	require.Equal(t, want, got)
	require.Len(t, trace, len(got))
	for i := range got {
		require.Equal(t, got[i].Path, trace[i].Path)
	}
}
