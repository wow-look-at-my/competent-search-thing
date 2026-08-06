# internal/telemetry

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/telemetry` -- the ALWAYS-ON local ranking log (config
search.telemetry carries only maxSizeKB -- deliberately no off
switch, the log is private by staying on the machine; wired by
internal/app's
telemetry.go, fed by internal/index's signalstrace.go seam), pure
(stdlib only) and exhaustively unit-tested. One JSON line per PICK
at <configDir>/telemetry.jsonl: Record {v 1, ts RFC3339 (both
stamped by the store when unset, injectable clock), query (always
recorded in full), blendActive, joined (impression found in the
app's ring), refined (reserved, always false), shown, picked}.
ShownRow marshals KIND-SHAPED (custom MarshalJSON): file rows carry
path + the full feature vector explicitly -- class/effClass/align/
boost/recency/cwd/penalty/isDir/depth/ext, zeros included so class
0 = exact is never ambiguous with omitted -- while plugin rows
carry exactly rank/kind/plugin/score/title (full-fidelity capture;
the title is the rendered row title the frontend reports). Store: append-only JSONL (deliberate divergence from
history.json's whole-file rewrite -- immutable append-heavy
records), O_APPEND single-write() lines so appends never interleave
mid-record, mutex-guarded, 0600 (re-tightened best-effort per
open), MkdirAll parent, nil-receiver-safe no-ops; rotation = an
append that would cross MaxSizeKB (New repairs <= 0 to 65536)
renames the live file over telemetry.jsonl.1 first, so the disk cap
is two generations, an empty live file never rotates, and a torn
final line is reader-skipped (loss-tolerant log, not a ledger; no
Load step, only offline tooling reads). The wire half:
PickReport{query, shown []ShownRef{kind, path|plugin+score+title},
picked PickedRef{rank, action, revealed}} -- row IDENTITIES plus
the plugin-row titles (the one display field only the frontend
knows), so a frontend can never forge feature values --
and ValidatePickReport (bounded sizes 256 rows/4096-byte strings
incl. titles -- wire-abuse defense, never redaction --
per-kind field consistency incl. abs paths + plugin-id shape +
score 0..100, in-range rank, open/reveal + revealed-flag
consistency for file picks, charset-gated action kind for plugin
picks). Deliberately NO schema in schemas/ (internal single-party
format, the history.json / frecency.json precedent).
