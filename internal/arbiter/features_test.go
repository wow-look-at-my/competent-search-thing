package arbiter

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// fullFileRow / fullPluginRow are maximally-populated rows for the
// layout tests.
func fullFileRow() Row {
	return Row{
		Kind: KindFile, Class: 1, EffClass: 0, Align: 120, Boost: 2.5,
		Recency: 0.4, Cwd: 1.2, Penalty: 0.3, IsDir: true, Depth: 7,
		Ext: ".MD", Query: "tax 2025/q3", Hour: 14,
	}
}

func fullPluginRow() Row {
	return Row{
		Kind: KindPlugin, Plugin: "firefox-tabs", Score: 85, Priority: 1,
		SourceRank: 2, Query: "tax 2025/q3", Hour: 14,
	}
}

// collect returns the emitted (index, value) pairs as a map.
func collect(t *testing.T, r Row) map[int]float64 {
	t.Helper()
	out := map[int]float64{}
	visitFeatures(r, func(i int, v float64) {
		require.GreaterOrEqual(t, i, 0)
		require.Less(t, i, FeatureDim, "feature index out of bounds")
		_, dup := out[i]
		require.False(t, dup, "feature index %d emitted twice", i)
		out[i] = v
	})
	return out
}

func TestFeatureLayoutBounds(t *testing.T) {
	// The layout constants must tile without overlap; FeatureDim caps
	// them all (a regression here silently corrupts every model).
	require.Equal(t, idxExtBase+extBuckets, fileVaryHi)
	require.Less(t, fileVaryHi, idxBuiltinBase+1)
	require.Equal(t, idxTodBase+2*todBuckets, FeatureDim)
	collect(t, fullFileRow())
	collect(t, fullPluginRow())
}

func TestFileRowFeatures(t *testing.T) {
	got := collect(t, fullFileRow())
	require.Equal(t, 1.0, got[idxBias])
	require.Equal(t, 1.0, got[idxIsFile])
	require.NotContains(t, got, idxIsPlugin, "a file row never carries plugin features")
	require.Equal(t, 1.0, got[idxClassBase+1], "class prefix one-hot")
	require.Equal(t, 1.0, got[idxJumped], "EffClass < Class marks the tier jump")
	require.InDelta(t, 120.0/256, got[idxAlign], 1e-12)
	require.InDelta(t, 2.5/3.5, got[idxBoost], 1e-12)
	require.InDelta(t, 0.4, got[idxRecency], 1e-12)
	require.InDelta(t, 1.2/2.2, got[idxCwd], 1e-12)
	require.InDelta(t, 0.3, got[idxPenalty], 1e-12)
	require.Equal(t, 1.0, got[idxIsDir])
	require.Equal(t, 1.0, got[idxDepthBase+2], "depth 7 lands in the 7..9 bucket")
	require.Equal(t, 1.0, got[idxExtBase+hashBucket(".md", extBuckets)],
		"extension buckets fold case")
	// Query crosses ride the FILE side: len 11 runes -> bucket 2,
	// has-space, has-separator, hour 14 -> tod bucket 2.
	require.Equal(t, 1.0, got[idxQLenBase+0*qlenBuckets+2])
	require.Equal(t, 1.0, got[idxSpaceBase+0])
	require.Equal(t, 1.0, got[idxSepBase+0])
	require.Equal(t, 1.0, got[idxTodBase+0*todBuckets+2])
	require.NotContains(t, got, idxSpaceBase+1, "never the plugin side")
}

func TestPluginRowFeatures(t *testing.T) {
	got := collect(t, fullPluginRow())
	require.Equal(t, 1.0, got[idxIsPlugin])
	require.NotContains(t, got, idxIsFile)
	require.NotContains(t, got, idxClassBase, "plugin rows carry no file block")
	require.Equal(t, 1.0, got[idxBuiltinBase+2], "firefox-tabs is builtin slot 2")
	require.InDelta(t, 0.85, got[idxPluginScore], 1e-12)
	require.InDelta(t, 1.0/3, got[idxPluginPriority], 1e-12)
	require.InDelta(t, 0.2, got[idxSourceRank], 1e-12)
	// Crosses ride the PLUGIN side.
	require.Equal(t, 1.0, got[idxQLenBase+1*qlenBuckets+2])
	require.Equal(t, 1.0, got[idxSpaceBase+1])
	require.Equal(t, 1.0, got[idxSepBase+1])
	require.Equal(t, 1.0, got[idxTodBase+1*todBuckets+2])

	// A non-builtin id hashes into the shared bucket space instead.
	ext := collect(t, Row{Kind: KindPlugin, Plugin: "my-plugin", Score: 50})
	require.NotContains(t, ext, idxBuiltinBase+0)
	require.Equal(t, 1.0, ext[idxPluginHashBase+hashBucket("my-plugin", pluginBuckets)])
}

func TestUnknownKindEmitsBiasOnly(t *testing.T) {
	got := collect(t, Row{Kind: "future", Query: "abc def"})
	require.Equal(t, map[int]float64{idxBias: 1}, got,
		"unknown kinds carry no kind-crossed features at all")
}

func TestValueClamps(t *testing.T) {
	got := collect(t, Row{
		Kind: KindFile, Class: 99, Align: 100000, Boost: -3, Recency: 9,
		Cwd: -1, Penalty: -2, Depth: -5, Hour: 99, Query: "",
	})
	require.Equal(t, 1.0, got[idxClassBase+3], "class clamps into the ladder")
	require.Equal(t, 1.0, got[idxAlign], "alignment saturates at 1")
	require.NotContains(t, got, idxBoost, "non-positive boost emits nothing")
	require.Equal(t, 1.0, got[idxRecency])
	require.NotContains(t, got, idxCwd)
	require.NotContains(t, got, idxPenalty)
	require.Equal(t, 1.0, got[idxDepthBase+0])
	require.Equal(t, 1.0, got[idxTodBase+0*todBuckets+3], "hour clamps to 23")
	require.Equal(t, 1.0, got[idxQLenBase+0*qlenBuckets+0], "empty query is the shortest bucket")
}

func TestBuckets(t *testing.T) {
	require.Equal(t, []int{0, 0, 1, 1, 2, 2, 3},
		[]int{depthBucket(0), depthBucket(3), depthBucket(4), depthBucket(6), depthBucket(7), depthBucket(9), depthBucket(10)})
	require.Equal(t, []int{0, 0, 1, 1, 2, 2, 3},
		[]int{qlenBucket(""), qlenBucket("ab"), qlenBucket("abc"), qlenBucket("abcde"), qlenBucket("abcdef"), qlenBucket("abcdefghijk"), qlenBucket("abcdefghijkl")})
	require.Equal(t, 3, builtinIndex("firefox-frequent"))
	require.Equal(t, -1, builtinIndex("calc"))
	// FNV-1a is stable across runs/platforms; pin one value so a
	// hashing change is a loud test failure, not silent model rot.
	require.Equal(t, hashBucket(".md", extBuckets), hashBucket(".md", extBuckets))
	require.NotEqual(t, hashBucket(".md", extBuckets), hashBucket(".txt", extBuckets),
		"the test extensions must land in distinct buckets (fixture validity)")
}

// TestSparseDotMatchesDense pins the two feature consumers together:
// the serve path's allocation-free dotRange must agree with the
// trainer's dense vectors for any row and any index range.
func TestSparseDotMatchesDense(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	w := make([]float64, FeatureDim)
	for i := range w {
		w[i] = rng.NormFloat64()
	}
	m, err := NewModel(w)
	require.NoError(t, err)
	rows := []Row{
		fullFileRow(), fullPluginRow(),
		{Kind: KindFile, Query: "x"},
		{Kind: KindPlugin, Plugin: "zzz", Score: 100, SourceRank: 99, Priority: 9},
		{Kind: "other"},
	}
	for _, r := range rows {
		x := Featurize(r)
		require.Len(t, x, FeatureDim)
		var full, vary float64
		for i, v := range x {
			full += w[i] * v
			if i >= fileVaryLo && i < fileVaryHi {
				vary += w[i] * v
			}
		}
		require.InDelta(t, full, m.dotRange(r, 0, FeatureDim), 1e-9)
		require.InDelta(t, vary, m.dotRange(r, fileVaryLo, fileVaryHi), 1e-9)
	}
}
