package arbiter

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNilSafety(t *testing.T) {
	var s *Store
	require.Nil(t, s.Current())
	require.Equal(t, "", s.Reason())
	s.SetOutcome(TrainOutcome{}) // must not panic

	var m *Model
	require.Equal(t, 0.0, m.Score(fullFileRow()))
	require.Equal(t, 0.0, m.FileDelta(fullFileRow()))
	require.Equal(t, 0, m.Picks())
	hm, hb := m.HoldoutAccuracy()
	require.Equal(t, 0.0, hm)
	require.Equal(t, 0.0, hb)
	require.Nil(t, m.Weights())
}

func TestStoreSwap(t *testing.T) {
	s := NewStore()
	require.Nil(t, s.Current(), "a fresh store is inactive")
	require.Equal(t, "not trained yet", s.Reason())

	m, err := NewModel(make([]float64, FeatureDim))
	require.NoError(t, err)
	s.SetOutcome(TrainOutcome{Model: m, Reason: "active: test"})
	require.Same(t, m, s.Current())
	require.Equal(t, "active: test", s.Reason())

	s.SetOutcome(TrainOutcome{Reason: "inactive: gate"})
	require.Nil(t, s.Current(), "a refused outcome deactivates the store")
	require.Equal(t, "inactive: gate", s.Reason())
}

func TestNewModelValidation(t *testing.T) {
	_, err := NewModel(make([]float64, 3))
	require.Error(t, err)
	bad := make([]float64, FeatureDim)
	bad[5] = math.NaN()
	_, err = NewModel(bad)
	require.Error(t, err)
	inf := make([]float64, FeatureDim)
	inf[0] = math.Inf(1)
	_, err = NewModel(inf)
	require.Error(t, err)

	w := make([]float64, FeatureDim)
	w[idxBias] = 2
	m, err := NewModel(w)
	require.NoError(t, err)
	w[idxBias] = 99 // the model copied its weights
	require.Equal(t, 2.0, m.Weights()[idxBias])
}

func TestFileDeltaClampAndScope(t *testing.T) {
	// A huge extension weight clamps at the band bound; a huge BIAS
	// weight contributes nothing -- FileDelta scores only the
	// file-varying block, so per-query constants can never eat the
	// clamp's headroom or shift the whole file list.
	w := make([]float64, FeatureDim)
	w[idxExtBase+hashBucket(".md", extBuckets)] = 100
	w[idxExtBase+hashBucket(".txt", extBuckets)] = -100
	w[idxBias] = 1000
	w[idxIsFile] = 1000
	m, err := NewModel(w)
	require.NoError(t, err)

	md := Row{Kind: KindFile, Ext: ".md"}
	txt := Row{Kind: KindFile, Ext: ".txt"}
	require.Equal(t, FileDeltaClamp, m.FileDelta(md))
	require.Equal(t, -FileDeltaClamp, m.FileDelta(txt))
	require.Greater(t, m.Score(md), FileDeltaClamp,
		"the full score (cross-source comparisons) is deliberately unclamped")

	small := make([]float64, FeatureDim)
	small[idxIsDir] = 0.5
	sm, err := NewModel(small)
	require.NoError(t, err)
	require.Equal(t, 0.5, sm.FileDelta(Row{Kind: KindFile, IsDir: true}),
		"in-band deltas pass through unclamped")
	require.Equal(t, 0.0, sm.FileDelta(Row{Kind: KindPlugin, Plugin: "x"}),
		"plugin rows carry no file-varying features")
}
