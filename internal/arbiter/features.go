package arbiter

import (
	"strings"
	"unicode/utf8"
)

// Row kinds, matching the telemetry log's shown-row kinds (the wire
// strings are the contract; internal/telemetry is deliberately not
// imported -- see read.go).
const (
	KindFile   = "file"
	KindPlugin = "plugin"
)

// Row is one delivered row's model input -- the unified projection of
// a telemetry shown-row (training) and of a live row at the two serve
// seams (a blend candidate's ResultSignals, or a plugin emission
// row). ONE feature definition (visitFeatures) consumes it on both
// sides, so train and serve can never disagree.
type Row struct {
	// Kind is KindFile or KindPlugin.
	Kind string

	// File rows: the ranking components at impression time (the
	// telemetry feature vector / index.ResultSignals).
	Class    int     // match class ordinal (exact 0 .. fuzzy 3)
	EffClass int     // tier-jumped effective class
	Align    int     // fuzzy alignment score (0 outside the fuzzy class)
	Boost    float64 // decayed open count
	Recency  float64 // cold-start recency score in [0, 1]
	Cwd      float64 // working-directory proximity boost
	Penalty  float64 // location-noise penalty in [0, 1]
	IsDir    bool
	Depth    int    // path component count
	Ext      string // final extension, dot included ("" for none)

	// Plugin rows.
	Plugin     string // provider id ("apps-search", "firefox-tabs", ...)
	Score      int    // engine wire score 0..100
	Priority   int    // emission priority (apps-search = 1 today)
	SourceRank int    // index among the SAME provider's rows

	// Shared query context. In a pairwise linear model a feature that
	// is identical for every row of an impression cancels out of every
	// comparison, so the query-shape and time features enter CROSSED
	// with the row kind -- which is exactly the "which source does
	// this query shape mean" signal the arbiter exists for.
	Query string
	Hour  int // local hour 0..23 (the time-of-day bucket input)
}

// Feature vector layout. Grouped one-hots and normalized scalars;
// every value lands in [0, 1] so one fixed learning rate serves all.
const (
	idxBias     = 0
	idxIsFile   = 1
	idxIsPlugin = 2

	// File-row block (the file-VARYING features FileDelta scores).
	idxClassBase = 3 // 4 one-hots: exact/prefix/substring/fuzzy
	idxJumped    = 7 // tier-jumped (EffClass < Class)
	idxAlign     = 8
	idxBoost     = 9
	idxRecency   = 10
	idxCwd       = 11
	idxPenalty   = 12
	idxIsDir     = 13
	idxDepthBase = 14 // depthBuckets one-hots
	idxExtBase   = idxDepthBase + depthBuckets

	// Plugin-row block.
	idxBuiltinBase    = idxExtBase + extBuckets // 4 known-builtin one-hots
	idxPluginHashBase = idxBuiltinBase + len(builtinIDs)
	idxPluginScore    = idxPluginHashBase + pluginBuckets
	idxPluginPriority = idxPluginScore + 1
	idxSourceRank     = idxPluginPriority + 1

	// Query-shape and time-of-day crosses (x2: file, plugin).
	idxQLenBase  = idxSourceRank + 1
	idxSpaceBase = idxQLenBase + 2*qlenBuckets
	idxSepBase   = idxSpaceBase + 2
	idxTodBase   = idxSepBase + 2

	// FeatureDim is the weight vector length.
	FeatureDim = idxTodBase + 2*todBuckets
)

// Bucket sizes and the file-varying block bounds (see
// Model.FileDelta).
const (
	depthBuckets  = 4
	extBuckets    = 16
	pluginBuckets = 8
	qlenBuckets   = 4
	todBuckets    = 4

	fileVaryLo = idxClassBase
	fileVaryHi = idxExtBase + extBuckets

	// maxSourceRank / maxPriority cap the two normalized plugin
	// scalars.
	maxSourceRank = 10
	maxPriority   = 3
)

// builtinIDs are the known in-tree providers that get their own
// feature each -- the sources cross-source arbitration is about.
// Everything else (external plugins, the remaining builtins) shares
// the pluginBuckets hash space.
var builtinIDs = [...]string{"apps-search", "windows", "firefox-tabs", "firefox-frequent"}

// visitFeatures emits row r's nonzero features as (index, value)
// pairs. It is THE single feature definition: the trainer's dense
// vectors (Featurize) and the serve paths' sparse dots (dotRange)
// both consume it, pinned in parity by TestVisitMatchesFeaturize.
func visitFeatures(r Row, emit func(idx int, v float64)) {
	emit(idxBias, 1)
	kind := -1
	switch r.Kind {
	case KindFile:
		kind = 0
		emit(idxIsFile, 1)
		emit(idxClassBase+clampInt(r.Class, 0, 3), 1)
		if r.EffClass < r.Class {
			emit(idxJumped, 1)
		}
		if r.Align > 0 {
			emit(idxAlign, clamp01(float64(r.Align)/256))
		}
		if r.Boost > 0 {
			emit(idxBoost, saturate(r.Boost))
		}
		if r.Recency > 0 {
			emit(idxRecency, clamp01(r.Recency))
		}
		if r.Cwd > 0 {
			emit(idxCwd, saturate(r.Cwd))
		}
		if r.Penalty > 0 {
			emit(idxPenalty, clamp01(r.Penalty))
		}
		if r.IsDir {
			emit(idxIsDir, 1)
		}
		emit(idxDepthBase+depthBucket(r.Depth), 1)
		emit(idxExtBase+hashBucket(strings.ToLower(r.Ext), extBuckets), 1)
	case KindPlugin:
		kind = 1
		emit(idxIsPlugin, 1)
		if b := builtinIndex(r.Plugin); b >= 0 {
			emit(idxBuiltinBase+b, 1)
		} else {
			emit(idxPluginHashBase+hashBucket(r.Plugin, pluginBuckets), 1)
		}
		if r.Score > 0 {
			emit(idxPluginScore, clamp01(float64(r.Score)/100))
		}
		if r.Priority > 0 {
			emit(idxPluginPriority, float64(clampInt(r.Priority, 0, maxPriority))/maxPriority)
		}
		if r.SourceRank > 0 {
			emit(idxSourceRank, float64(clampInt(r.SourceRank, 0, maxSourceRank))/maxSourceRank)
		}
	default:
		return // unknown kinds carry no kind-crossed features
	}
	q := strings.TrimSpace(r.Query)
	emit(idxQLenBase+kind*qlenBuckets+qlenBucket(q), 1)
	if strings.ContainsRune(q, ' ') {
		emit(idxSpaceBase+kind, 1)
	}
	if strings.ContainsAny(q, "/\\") {
		emit(idxSepBase+kind, 1)
	}
	emit(idxTodBase+kind*todBuckets+clampInt(r.Hour, 0, 23)/6, 1)
}

// Featurize builds r's dense feature vector -- the trainer's shape
// (the serve paths use the sparse visitor directly).
func Featurize(r Row) []float64 {
	x := make([]float64, FeatureDim)
	visitFeatures(r, func(i int, v float64) { x[i] = v })
	return x
}

// depthBucket buckets a path component count: <=3, 4..6, 7..9, >=10.
func depthBucket(d int) int {
	switch {
	case d <= 3:
		return 0
	case d <= 6:
		return 1
	case d <= 9:
		return 2
	default:
		return 3
	}
}

// qlenBucket buckets a trimmed query's rune count: <=2, 3..5, 6..11,
// >=12.
func qlenBucket(q string) int {
	n := utf8.RuneCountInString(q)
	switch {
	case n <= 2:
		return 0
	case n <= 5:
		return 1
	case n <= 11:
		return 2
	default:
		return 3
	}
}

// builtinIndex returns id's slot in builtinIDs, or -1.
func builtinIndex(id string) int {
	for i, b := range builtinIDs {
		if id == b {
			return i
		}
	}
	return -1
}

// hashBucket maps s onto [0, n) via FNV-1a (stable across runs and
// platforms -- the buckets are part of the learned weights' meaning).
func hashBucket(s string, n int) int {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return int(h % uint32(n))
}

// saturate maps a non-negative unbounded signal onto [0, 1) as
// v/(1+v).
func saturate(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return v / (1 + v)
}

// clamp01 clamps to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// clampInt clamps to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
