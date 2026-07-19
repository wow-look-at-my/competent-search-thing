// Package priors holds the PURE half of pick-memory ranking priors:
// small lookup tables, learned from the user's own recorded picks,
// that the index blend consults as ONE additive per-candidate term
// (internal/index blend.go's Prior seam; the app wiring lives in
// internal/app priors.go, the knob at config search.priors -- opt-in,
// zero value = OFF, the preview.enabled privacy precedent).
//
// Three tables, rebuilt wholesale from local files and swapped
// atomically (queries never see a half-built table):
//
//   - Exact-query pick memory: normalized query -> {path -> decayed
//     pick weight} (frecency-style exponential decay, 14-day
//     half-life). Picked the same row for the same query before, and
//     that query pins the row near the top of its match class. The
//     strongest signal -- near-deterministic when present -- and the
//     only one that needs real query->pick pairs, so it has NO
//     bootstrap.
//   - Per-extension pick rate: smoothed (picks+alpha)/(impressions+
//     alpha*k) over the telemetry log's shown/picked file rows,
//     applied as a small log-odds nudge ("this user picks .md files").
//   - Per-directory-prefix pick rate: the same smoothing keyed by the
//     first dirPrefixComponents components of the row's directory
//     ("things under /home/me/projects get picked").
//
// Data sources (both read tolerantly, never written):
//
//   - <configDir>/telemetry.jsonl (+ .jsonl.1), the opt-in ranking
//     telemetry log. The JSONL line format is the data contract (one
//     record per pick: ts, query, shown rows incl. file paths, picked
//     row); unknown fields are ignored, malformed lines skipped,
//     non-file picks contribute impressions only. This package
//     deliberately does NOT import a telemetry package -- the on-disk
//     format is the interface.
//   - <configDir>/frecency.json ({"v":1,"entries":{path:{c,t}}}) as
//     the BOOTSTRAP: while the telemetry log holds fewer than
//     minTelemetryPicks file picks, extension/dir-prefix
//     distributions derived from the decayed open counts fill the two
//     rate tables, so the feature nudges from day one.
//
// Memory is hard-capped: the exact-query table is bounded by
// maxQueries, maxRowsPerQuery, and an approximate exactBudgetBytes
// byte budget (lowest decayed weights evicted first); the rate tables
// by maxExts/maxDirPrefixes keys of bounded length. Worst case sums
// comfortably under 1 MiB.
//
// Conventions mirror internal/frecency: RWMutex (reads share),
// injectable clock, nil-receiver and zero-value no-ops, immutable
// swapped state, missing files = empty tables + nil error.
package priors

import (
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Tuning constants. Deliberately internal -- config exposes only the
// search.priors.enabled switch; these defaults are the feature.
const (
	// halfLife is the exact-query pick weight half-life: two weeks,
	// matching frecency's open-count decay.
	halfLife = 14 * 24 * time.Hour
	// alpha and smoothK are the additive smoothing of the rate
	// tables: rate = (picks + alpha) / (impressions + alpha*smoothK),
	// so an unseen key sits exactly at the 1/smoothK baseline and
	// early counts move it slowly.
	alpha   = 1.0
	smoothK = 20.0
	// exactWeight scales the exact-query term: weight * w/(1+w) with
	// w the decayed pick count, so one fresh pick contributes
	// exactWeight/2 blend units (several recorded opens' worth --
	// dominant within the class) and the term saturates below
	// exactWeight.
	exactWeight = 6.0
	// rateWeight scales each rate table's log-odds nudge; with
	// rateLogClamp it bounds one table's contribution to
	// +-rateWeight*rateLogClamp blend units -- a nudge on the
	// penalty/recency scale, never a takeover.
	rateWeight   = 0.2
	rateLogClamp = 1.5
	// minTelemetryPicks gates the frecency bootstrap: below this many
	// telemetry file picks the rate tables also fold in the
	// frecency.json distributions.
	minTelemetryPicks = 20
	// dirPrefixComponents is how many leading directory components
	// form a dir-prefix key.
	dirPrefixComponents = 3
	// Capacity caps (see the package comment's memory math).
	maxQueries       = 2048
	maxRowsPerQuery  = 4
	maxExts          = 512
	maxDirPrefixes   = 2048
	exactBudgetBytes = 512 << 10
	// Per-string sanity bounds: anything longer is skipped rather
	// than stored (rates still count such rows; only the exact table
	// stores strings).
	maxQueryBytes = 256
	maxPathBytes  = 1024
	// approxQueryOverhead / approxRowOverhead are the per-entry
	// bookkeeping estimates the byte budget uses on top of the string
	// bytes (map headers, the entry struct).
	approxQueryOverhead = 64
	approxRowOverhead   = 48
)

// baselineRate is the smoothed rate of a key never seen: alpha /
// (alpha * smoothK) = 1/smoothK. The log-odds nudge is measured
// against it, so unknown keys contribute exactly zero.
const baselineRate = 1.0 / smoothK

// exactEntry is one (query, path) pick memory: a count decayed lazily
// to read time, the frecency.Store shape.
type exactEntry struct {
	c float64
	t time.Time
}

// rateCell accumulates one key's pick/impression counts.
type rateCell struct {
	picks       float64
	impressions float64
}

// Tables is one immutable generation of the three lookup tables.
// Build with BuildTables and hand to Store.SetTables; never mutate a
// published Tables.
type Tables struct {
	exact map[string]map[string]exactEntry
	ext   map[string]rateCell
	dir   map[string]rateCell
}

// Counts reports the table sizes (diagnostics and the app's one
// startup log line). Nil-safe.
func (t *Tables) Counts() (queries, exts, dirs int) {
	if t == nil {
		return 0, 0, 0
	}
	return len(t.exact), len(t.ext), len(t.dir)
}

// empty reports whether the tables carry nothing at all.
func (t *Tables) empty() bool {
	if t == nil {
		return true
	}
	return len(t.exact) == 0 && len(t.ext) == 0 && len(t.dir) == 0
}

// rateTerm is one rate table's contribution for key: the clamped
// log-odds of the smoothed rate against the unseen-key baseline,
// scaled by rateWeight. A missing key is exactly zero.
func rateTerm(m map[string]rateCell, key string) float64 {
	cell, ok := m[key]
	if !ok {
		return 0
	}
	rate := (cell.picks + alpha) / (cell.impressions + alpha*smoothK)
	v := math.Log(rate / baselineRate)
	if v > rateLogClamp {
		v = rateLogClamp
	} else if v < -rateLogClamp {
		v = -rateLogClamp
	}
	return rateWeight * v
}

// decayedWeight is c halved once per halfLife elapsed between t and
// now (non-positive elapse decays nothing, the frecency stance).
func decayedWeight(c float64, t, now time.Time) float64 {
	dt := now.Sub(t)
	if dt <= 0 {
		return c
	}
	return c * math.Exp2(-float64(dt)/float64(halfLife))
}

// normalizeQuery is the exact-query key normalization: trimmed and
// case-folded. Both the builder (record queries) and the lookup
// (live queries) go through it, so the two sides can never disagree.
func normalizeQuery(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}

// extKey is the per-extension table key for path: the final
// extension, lowercased, dot included; "" for none (extensionless
// files and directories form their own class).
func extKey(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// dirPrefixKey is the per-directory-prefix table key: the first
// dirPrefixComponents components of path's DIRECTORY (the final
// segment never keys the table, or every file would be its own key).
// Both separators split, mirroring frecency's penalty classifier; ""
// for paths without a directory part.
func dirPrefixKey(path string) string {
	end := 0
	components := 0
	sawByte := false
	for i := 0; i < len(path); i++ {
		if path[i] == '/' || path[i] == '\\' {
			if sawByte {
				components++
				end = i
				if components == dirPrefixComponents {
					return path[:end]
				}
			}
			sawByte = false
			continue
		}
		sawByte = true
	}
	// Fewer than dirPrefixComponents separators after the last named
	// component: the trailing segment is the base name, so the key is
	// everything before it (what end last marked).
	return path[:end]
}

// Options configures a Store. The zero value means time.Now.
type Options struct {
	// Now is the clock decay is measured against (tests).
	Now func() time.Time
}

// Store serves the current Tables generation to the ranking blend.
// All methods are safe for concurrent use and nil-receiver-safe (a
// nil *Store -- the feature disabled -- no-ops everything).
type Store struct {
	mu     sync.RWMutex
	tables *Tables
	now    func() time.Time
}

// New creates an empty store (PriorFunc returns nil until SetTables
// installs a non-empty generation).
func New(opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{now: opts.Now}
}

// SetTables atomically swaps in a new table generation. The caller
// hands over ownership: t must not be mutated afterwards.
func (s *Store) SetTables(t *Tables) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.tables = t
	s.mu.Unlock()
}

// Counts reports the current generation's table sizes. Nil-safe.
func (s *Store) Counts() (queries, exts, dirs int) {
	if s == nil {
		return 0, 0, 0
	}
	s.mu.RLock()
	t := s.tables
	s.mu.RUnlock()
	return t.Counts()
}

// PriorFunc resolves the prior lookup for one query: called once per
// query, it snapshots the current table generation and returns a
// closure the blend consults once per merged candidate -- pure map
// lookups and a little float math, no locks, no allocation on the
// per-candidate path. A nil store or empty tables return nil (the
// blend then adds no term at all).
func (s *Store) PriorFunc(query string) func(path string) float64 {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	t := s.tables
	now := s.now()
	s.mu.RUnlock()
	if t.empty() {
		return nil
	}
	var rows map[string]exactEntry
	if len(t.exact) > 0 {
		rows = t.exact[normalizeQuery(query)]
	}
	return func(path string) float64 {
		var v float64
		if rows != nil {
			if e, ok := rows[path]; ok {
				w := decayedWeight(e.c, e.t, now)
				v += exactWeight * w / (1 + w)
			}
		}
		if len(t.ext) > 0 {
			v += rateTerm(t.ext, extKey(path))
		}
		if len(t.dir) > 0 {
			v += rateTerm(t.dir, dirPrefixKey(path))
		}
		return v
	}
}
