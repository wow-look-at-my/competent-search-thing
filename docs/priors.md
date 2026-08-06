# internal/priors

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/priors` -- the PURE half of the pick-memory ranking
priors (config search.priors, ON by default -- disabled is a debug
escape hatch; consumed by
internal/index's Blend.Prior seam, wired by internal/app
priors.go), pure stdlib and headless-tested on the
internal/frecency conventions (RWMutex, injectable clock,
nil-receiver/zero-value = total no-op, immutable swapped state).
Three tables per generation (Tables, built by BuildTables and
swapped whole via Store.SetTables): exact-query pick memory
(normalized query -> path -> frecency-style decayed pick weight,
14d half-life; term = 6*w/(1+w), the dominant within-class signal),
per-extension and per-dir-prefix (first 3 DIRECTORY components,
both separators split) smoothed pick rates ((picks+1)/(imps+20)
applied as a clamped log-odds nudge vs the 1/20 unseen baseline,
+-0.3 max per table -- the penalty/recency scale). Data sources,
read-only + tolerant (missing = empty + nil error, malformed lines/
entries skipped, oversized files ignored): the telemetry.jsonl(.1)
JSONL FORMAT as a data contract (ReadTelemetryFile parses only
v/ts/query/shown file paths/picked kind+path; non-file picks =
impressions only; unknown fields -- the plugin-row titles included
-- are ignored; deliberately NO internal/telemetry import) and
frecency.json (ReadFrecencyWeights, the {v:1,entries:{path:{c,t}}}
shape re-declared) whose decayed-count distributions BOOTSTRAP the
two rate tables while the log holds < 20 file picks -- the
exact-query table never bootstraps. Memory hard-capped: 2048
queries x 4 rows under a 512 KiB approximate byte budget (lowest
decayed best-weight queries evicted first), 512/2048 rate keys
(most-seen kept). Store.PriorFunc(query) resolves ONCE per query
(table-generation snapshot, no locks or allocation on the
per-candidate path) and returns nil when nothing applies.
