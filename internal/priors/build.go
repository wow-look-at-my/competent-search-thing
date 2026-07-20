package priors

import (
	"sort"
	"time"
)

// BuildTables derives one immutable Tables generation from the
// telemetry pick records (oldest first -- pass jsonl.1's records
// before the live file's) and, while the log holds fewer than
// minTelemetryPicks file picks, the frecency-derived bootstrap
// weights (path -> decayed open count; nil skips the bootstrap).
//
//   - Every shown file row counts one impression for its extension
//     and dir-prefix keys; every FILE pick counts one pick for both
//     (non-file picks contribute impressions only).
//   - A file pick with a non-empty logged query feeds the exact-query
//     table: the (query, path) weight decays to the pick's timestamp
//     and gains 1, frecency-style, so repeat picks compound and stale
//     ones fade.
//   - The bootstrap folds each frecency path's decayed count in as
//     picks against the total decayed count as impressions -- the
//     share of the user's opens going to that extension/prefix -- so
//     the rate tables nudge before any telemetry exists.
//
// Caps are enforced here (the store only ever swaps whole
// generations): per-query rows, query count, the exact-table byte
// budget, and the rate-table key counts. The result is never nil.
func BuildTables(recs []PickRecord, frecWeights map[string]float64, now time.Time) *Tables {
	t := &Tables{
		exact: map[string]map[string]exactEntry{},
		ext:   map[string]rateCell{},
		dir:   map[string]rateCell{},
	}
	filePicks := 0
	for _, rec := range recs {
		for _, p := range rec.ShownFiles {
			bump(t.ext, extKey(p), 0, 1)
			bump(t.dir, dirPrefixKey(p), 0, 1)
		}
		if rec.PickedPath == "" {
			continue
		}
		filePicks++
		bump(t.ext, extKey(rec.PickedPath), 1, 0)
		bump(t.dir, dirPrefixKey(rec.PickedPath), 1, 0)
		q := normalizeQuery(rec.Query)
		if q == "" || len(q) > maxQueryBytes || len(rec.PickedPath) > maxPathBytes {
			continue
		}
		ts := rec.TS
		if ts.IsZero() {
			ts = now
		}
		rows := t.exact[q]
		if rows == nil {
			rows = map[string]exactEntry{}
			t.exact[q] = rows
		}
		e := rows[rec.PickedPath]
		c := 1.0
		if !e.t.IsZero() {
			c = decayedWeight(e.c, e.t, ts) + 1
		}
		rows[rec.PickedPath] = exactEntry{c: c, t: ts}
	}

	if filePicks < minTelemetryPicks && len(frecWeights) > 0 {
		var total float64
		for _, w := range frecWeights {
			total += w
		}
		extBoot := map[string]float64{}
		dirBoot := map[string]float64{}
		for p, w := range frecWeights {
			extBoot[extKey(p)] += w
			dirBoot[dirPrefixKey(p)] += w
		}
		for k, w := range extBoot {
			bump(t.ext, k, w, total)
		}
		for k, w := range dirBoot {
			bump(t.dir, k, w, total)
		}
	}

	capRateTable(t.ext, maxExts)
	capRateTable(t.dir, maxDirPrefixes)
	capExactTable(t.exact, now)
	return t
}

// bump adds picks/impressions to m[key].
func bump(m map[string]rateCell, key string, picks, impressions float64) {
	cell := m[key]
	cell.picks += picks
	cell.impressions += impressions
	m[key] = cell
}

// capRateTable keeps at most max keys, preferring the most-seen ones
// (impressions, then picks, then key -- deterministic).
func capRateTable(m map[string]rateCell, max int) {
	if len(m) <= max {
		return
	}
	type kv struct {
		key  string
		cell rateCell
	}
	all := make([]kv, 0, len(m))
	for k, c := range m {
		all = append(all, kv{k, c})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.cell.impressions != b.cell.impressions {
			return a.cell.impressions > b.cell.impressions
		}
		if a.cell.picks != b.cell.picks {
			return a.cell.picks > b.cell.picks
		}
		return a.key < b.key
	})
	for _, e := range all[max:] {
		delete(m, e.key)
	}
}

// capExactTable enforces the exact-query caps: per-query rows first
// (highest decayed weight kept), then whole queries evicted lowest
// best-weight first until both the query count and the approximate
// byte budget hold.
func capExactTable(exact map[string]map[string]exactEntry, now time.Time) {
	type ranked struct {
		query string
		best  float64
		bytes int
	}
	all := make([]ranked, 0, len(exact))
	var totalBytes int
	for q, rows := range exact {
		if len(rows) > maxRowsPerQuery {
			type rw struct {
				path string
				w    float64
			}
			rs := make([]rw, 0, len(rows))
			for p, e := range rows {
				rs = append(rs, rw{p, decayedWeight(e.c, e.t, now)})
			}
			sort.Slice(rs, func(i, j int) bool {
				if rs[i].w != rs[j].w {
					return rs[i].w > rs[j].w
				}
				return rs[i].path < rs[j].path
			})
			for _, r := range rs[maxRowsPerQuery:] {
				delete(rows, r.path)
			}
		}
		r := ranked{query: q, bytes: len(q) + approxQueryOverhead}
		for p, e := range rows {
			r.bytes += len(p) + approxRowOverhead
			if w := decayedWeight(e.c, e.t, now); w > r.best {
				r.best = w
			}
		}
		totalBytes += r.bytes
		all = append(all, r)
	}
	if len(all) <= maxQueries && totalBytes <= exactBudgetBytes {
		return
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].best != all[j].best {
			return all[i].best < all[j].best // weakest memories evict first
		}
		return all[i].query < all[j].query
	})
	for _, r := range all {
		if len(exact) <= maxQueries && totalBytes <= exactBudgetBytes {
			return
		}
		delete(exact, r.query)
		totalBytes -= r.bytes
	}
}
