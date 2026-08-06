# internal/arbiter

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/arbiter` -- the PURE half of learned composition
arbitration (config search.arbiter, ON by default -- disabled is a
debug escape hatch / kill switch; applied at
the index Blend.Model seam AND the app layer's plugin-emission
path, both wired by internal/app arbiter.go), pure stdlib and
headless-tested on the internal/priors conventions (RWMutex
Store, nil-receiver/zero-value = total no-op, immutable swapped
Model generations, tolerant log reading). ONE feature definition
(features.go): Row is the unified projection of a telemetry
shown-row (training) and a live row (serving) -- file rows carry
the ResultSignals components + depth/ext, plugin rows carry
id/score/priority/within-source rank -- visited sparsely
(visitFeatures) by both the serve-path dot products and the
trainer's dense vectors (parity pinned); FeatureDim = 69: bias +
kind + file class one-hots/jump/align/boost/recency/cwd/penalty/
isDir + depth buckets (4) + ext FNV buckets (16) + the four known
builtin source one-hots (apps-search/windows/firefox-tabs/
firefox-frequent) + other-plugin FNV buckets (8) + score/priority/
source-rank + the query-shape and time-of-day features CROSSED
with the row kind (qlen buckets x2, has-space x2, has-separator
x2, tod buckets x2 -- row-independent features cancel out of every
pairwise comparison, so "which source does this query shape mean"
must enter as kind crosses). read.go re-declares the telemetry
JSONL line shape with explicit tags (the priors no-import stance:
internal/telemetry is an append-only writer with a one-way
MarshalJSON and no reader; the on-disk format is the contract) --
missing/oversized files = (nil, nil), malformed/wrong-version/
unknown-kind lines skipped, SourceRank counted per plugin id,
Priority derived (prioritizedSource: apps-search + the two Firefox
web sources firefox-frequent/firefox-tabs = 1, every other id 0 --
deliberately ALWAYS 1 for the promotable sources even though
serving is tier-gated, the over-approximation apps-search has
always had; serve reads the emission's real value), Hour from the
pick ts. train.go: Train = pairwise logistic SGD (picked row must
outscore every other shown row of its impression; fixed seed
20260720, 12 epochs, lr 0.2, L2 1e-4 -- deterministic given the
log, pinned) + the ACTIVATION GATE: >= MinPicks (200) JOINED
picks, finite weights, and on the time split (oldest 80% train /
newest 20% holdout) the model's holdout pairwise accuracy must
STRICTLY beat the delivered order's accuracy on the same pairs --
pass = retrain on ALL picks and ship, refuse = TrainOutcome with
nil Model + a human-readable Reason either way (RetrainEvery = 50
is the app layer's new-picks retrain cadence). Model.Score = the
full-vector score (cross-source comparisons, same-impression rows
only); Model.FileDelta = the file-VARYING block only (per-query
constants would eat headroom without reordering), clamped to
+-FileDeltaClamp (2.0 blend units -- strictly under the blend's
one-class-band equivalence, the 3.0 tier-jump threshold, and the
exact-prior's 6.0 saturation; class inversion is additionally
impossible structurally, effClass being the primary sort key).
