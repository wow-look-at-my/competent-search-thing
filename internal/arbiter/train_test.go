package arbiter

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mkFile / mkTab build synthetic impression rows.
func mkFile(q, ext string, class int) Row {
	return Row{Kind: KindFile, Class: class, EffClass: class, Depth: 3, Ext: ext, Query: q, Hour: 12}
}

func mkTab(q string, score, sourceRank int) Row {
	return Row{Kind: KindPlugin, Plugin: "firefox-tabs", Score: score, SourceRank: sourceRank, Query: q, Hour: 12}
}

// mkImps builds n identical joined impressions at one-minute steps.
func mkImps(n, picked int, rows func(i int) []Row) []Impression {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	out := make([]Impression, n)
	for i := range out {
		out[i] = Impression{
			TS:     base.Add(time.Duration(i) * time.Minute),
			Query:  "rep",
			Joined: true,
			Rows:   rows(i),
			Picked: picked,
		}
	}
	return out
}

func TestTrainRefusesBelowMinPicks(t *testing.T) {
	imps := mkImps(MinPicks-1, 1, func(int) []Row {
		return []Row{mkFile("rep", ".txt", 1), mkTab("rep", 85, 0)}
	})
	out := Train(imps)
	require.Nil(t, out.Model)
	require.Equal(t, MinPicks-1, out.Picks)
	require.Contains(t, out.Reason, fmt.Sprintf("%d joined picks", MinPicks-1))
}

func TestTrainIgnoresUnusableImpressions(t *testing.T) {
	// Unjoined records, single-row impressions, and out-of-range picks
	// all fall out of the usable count.
	imps := mkImps(MinPicks+50, 1, func(int) []Row {
		return []Row{mkFile("rep", ".txt", 1), mkTab("rep", 85, 0)}
	})
	for i := range imps {
		switch i % 3 {
		case 0:
			imps[i].Joined = false
		case 1:
			imps[i].Rows = imps[i].Rows[:1]
			imps[i].Picked = 0
		case 2:
			imps[i].Picked = 5
		}
	}
	out := Train(imps)
	require.Nil(t, out.Model)
	require.Equal(t, 0, out.Picks)
}

func TestTrainRefusesUnbeatableBaseline(t *testing.T) {
	// The user always picked the top row: the delivered order's
	// holdout accuracy is a perfect 1.0, no model can STRICTLY beat
	// it, and the gate keeps the arbiter inert -- there is nothing to
	// fix.
	imps := mkImps(MinPicks+40, 0, func(int) []Row {
		return []Row{mkFile("rep", ".txt", 0), mkFile("rep", ".txt", 1), mkTab("rep", 60, 0)}
	})
	out := Train(imps)
	require.Nil(t, out.Model)
	require.Equal(t, 1.0, out.HoldoutBaseline)
	require.LessOrEqual(t, out.HoldoutModel, 1.0)
	require.Contains(t, out.Reason, "does not beat")
}

func TestTrainLearnsTabPreference(t *testing.T) {
	// The arbitration story: the delivered order always put the file
	// first, the user always picked the tab. Baseline holdout accuracy
	// is 0; the model must learn tab-over-file and activate.
	imps := mkImps(MinPicks+60, 2, func(int) []Row {
		return []Row{
			mkFile("rep", ".txt", 1),
			mkFile("rep", ".txt", 2),
			mkTab("rep", 85, 0),
		}
	})
	out := Train(imps)
	require.NotNil(t, out.Model, "reason: %s", out.Reason)
	require.Equal(t, MinPicks+60, out.Picks)
	require.Greater(t, out.HoldoutModel, out.HoldoutBaseline)
	require.Contains(t, out.Reason, "active:")
	require.Equal(t, MinPicks+60, out.Model.Picks())
	hm, hb := out.Model.HoldoutAccuracy()
	require.Equal(t, out.HoldoutModel, hm)
	require.Equal(t, out.HoldoutBaseline, hb)

	tab := out.Model.Score(mkTab("rep", 85, 0))
	file := out.Model.Score(mkFile("rep", ".txt", 1))
	require.Greater(t, tab, file, "the model must rank the habitually picked tab above the file")
}

func TestTrainLearnsQueryShapeCross(t *testing.T) {
	// Query-shape arbitration: multi-word queries pick the tab,
	// short single-word queries pick the file. A row-independent
	// query feature would cancel out of every pair; the kind-crossed
	// features let the model separate the two shapes.
	spaced := "open tabs now" // has-space, len bucket 3
	short := "rp"             // no space, len bucket 0
	n := MinPicks + 100
	imps := make([]Impression, 0, 2*n)
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		imps = append(imps, Impression{
			TS: ts, Query: spaced, Joined: true, Picked: 1,
			Rows: []Row{mkFile(spaced, ".txt", 1), mkTab(spaced, 85, 0)},
		})
		imps = append(imps, Impression{
			TS: ts.Add(30 * time.Second), Query: short, Joined: true, Picked: 0,
			Rows: []Row{mkFile(short, ".txt", 1), mkTab(short, 85, 0)},
		})
	}
	out := Train(imps)
	require.NotNil(t, out.Model, "reason: %s", out.Reason)

	m := out.Model
	spacedGap := m.Score(mkTab(spaced, 85, 0)) - m.Score(mkFile(spaced, ".txt", 1))
	shortGap := m.Score(mkTab(short, 85, 0)) - m.Score(mkFile(short, ".txt", 1))
	require.Greater(t, spacedGap, 0.0, "multi-word queries mean the tab")
	require.Less(t, shortGap, 0.0, "short single-word queries mean the file")
}

func TestTrainLearnsExtensionPreference(t *testing.T) {
	// The within-file story: equal-class .txt and .md rows, the user
	// always picks the .md at rank 1. FileDelta must then favor .md,
	// bounded by the clamp.
	imps := mkImps(MinPicks+40, 1, func(int) []Row {
		return []Row{mkFile("rep", ".txt", 1), mkFile("rep", ".md", 1)}
	})
	out := Train(imps)
	require.NotNil(t, out.Model, "reason: %s", out.Reason)
	md := out.Model.FileDelta(mkFile("rep", ".md", 1))
	txt := out.Model.FileDelta(mkFile("rep", ".txt", 1))
	require.Greater(t, md, txt)
	for _, d := range []float64{md, txt} {
		require.LessOrEqual(t, d, FileDeltaClamp)
		require.GreaterOrEqual(t, d, -FileDeltaClamp)
	}
}

func TestTrainDeterministic(t *testing.T) {
	mk := func() []Impression {
		return mkImps(MinPicks+30, 2, func(i int) []Row {
			return []Row{
				mkFile("rep", ".txt", 1),
				mkFile("rep", ".md", i%3),
				mkTab("rep", 60+i%40, 0),
			}
		})
	}
	a := Train(mk())
	b := Train(mk())
	require.NotNil(t, a.Model)
	require.NotNil(t, b.Model)
	require.Equal(t, a.Model.Weights(), b.Model.Weights(),
		"training must be deterministic given the log")
	require.Equal(t, a.HoldoutModel, b.HoldoutModel)
	require.Equal(t, a.Reason, b.Reason)
}

func TestTrainTimeSplitUsesNewestForHoldout(t *testing.T) {
	// Train and holdout disagree on purpose: the OLDEST 80% picks the
	// tab, the NEWEST 20% picks the file (rank 0, so the delivered
	// order scores a perfect 1.0 there). A model fit on the oldest
	// slice learned the OPPOSITE preference and cannot reach 1.0, so
	// the gate must refuse -- which pins that the split is by TIME
	// (a random split would mix the two regimes into both halves).
	n := MinPicks + 100
	rows := func(int) []Row { return []Row{mkFile("rep", ".txt", 1), mkTab("rep", 85, 0)} }
	imps := mkImps(n, 1, rows) // oldest: pick the tab (rank 1)
	cut := n - n/5
	for i := cut; i < n; i++ {
		imps[i].Picked = 0 // newest 20%: pick the file
	}
	out := Train(imps)
	require.Nil(t, out.Model, "a preference flip in the newest slice must fail the holdout gate")
	require.Contains(t, out.Reason, "does not beat")
	require.Equal(t, 1.0, out.HoldoutBaseline, "holdout must be the newest (file-picking) slice")
	require.Less(t, out.HoldoutModel, 1.0)
}
