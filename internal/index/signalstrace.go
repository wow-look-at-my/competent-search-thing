package index

// The ranking-signals trace seam: an OPT-IN, per-query capture of the
// per-candidate components the final selection stage computes for the
// delivered rows. The wire Result deliberately never grows feature
// fields (it is a frontend contract crossing the Wails JSON bridge per
// keystroke); consumers that want the signals -- today the app layer's
// ranking telemetry, later any learned re-ranker's training capture --
// pass a buffer through QueryOptions.Trace and read it back after the
// query returns.
//
// Contract:
//
//   - A nil Trace is ZERO COST and byte-identical to today's engine:
//     no copies, no fills, the exact pre-seam code paths (pinned by
//     TestTraceNilIsByteIdentical next to TestBlendInactiveIsNoOp).
//   - A non-nil Trace NEVER changes the returned results or their
//     order -- it only records what the selection stage already
//     computed (pinned by TestTraceDoesNotChangeResults).
//   - On return the buffer holds one ResultSignals per returned
//     Result, in the same order. The fill happens only when a
//     selection stage ran: queries that return early (empty pattern,
//     empty store, zero candidates) leave the buffer untouched, so
//     callers pass a fresh buffer per query.
//   - With the blend INACTIVE the signal components (Boost, Recency,
//     Cwd, Penalty) are zero and EffClass == Class -- the blend never
//     computed them, and the trace records only what participated.
//
// Internally the buffer rides an unexported field on a PER-QUERY COPY
// of the Blend (traceBlend below), so the scan paths, the per-mode
// query functions, and every selectTop call site stay byte-identical
// -- only QueryWith, selectTop's assembly tail, and selectBlended know
// the seam exists. The shared Blend handed to Manager.SetBlend is
// never mutated (its immutability contract is untouched).

// ResultSignals is one delivered row's ranking components at
// impression time, parallel to the []Result the query returned.
type ResultSignals struct {
	// Path is the entry's absolute path (== the parallel Result.Path).
	Path string
	// Class is the pre-blend match class ordinal (exact 0, prefix 1,
	// substring 2, fuzzy 3; path mode shares the same ordinal space).
	Class uint8
	// EffClass is the tier-jumped effective class the blend ordered
	// by; == Class when the blend is inactive or no jump applied.
	EffClass uint8
	// Align is the fuzzy alignment score (0 outside the fuzzy class).
	Align int32
	// Boost is the decayed open count as it participated (0 with the
	// blend inactive or no open history).
	Boost float64
	// Recency is the cold-start recency score in [0, 1] as it
	// participated (0 when not probed: blend inactive, weight off, or
	// the candidate had open history).
	Recency float64
	// Cwd is the working-directory proximity boost as it participated.
	Cwd float64
	// Penalty is the location-noise penalty in [0, 1] as it
	// participated (0 when the noise weight is off).
	Penalty float64
	// IsDir mirrors the parallel Result.IsDir.
	IsDir bool
	// PathLen is the full path's byte length.
	PathLen int32
}

// Active reports whether this blend participates in ranking at all
// (nil-safe; see the unexported active). The app layer uses it to
// stamp telemetry records honestly: signal components exist only for
// queries an active blend ordered.
func (b *Blend) Active() bool { return b.active() }

// traceBlend returns the blend a query should run with: the caller's
// blend unchanged when no trace was requested (the zero-cost path),
// else a fresh per-query copy carrying the trace buffer. The copy is
// what keeps the seam additive: the shared, immutable Blend is never
// written, and no per-mode query function signature changes.
func traceBlend(opts QueryOptions) *Blend {
	if opts.Trace == nil {
		return opts.Blend
	}
	tb := Blend{}
	if opts.Blend != nil {
		tb = *opts.Blend
	}
	tb.trace = opts.Trace
	return &tb
}

// traceBuf returns the trace buffer riding this (per-query) blend
// copy; nil for untraced queries and nil blends.
func (b *Blend) traceBuf() *[]ResultSignals {
	if b == nil {
		return nil
	}
	return b.trace
}

// QueryTraced is Manager.Query with an optional ranking-signals trace:
// when trace is non-nil it receives one ResultSignals per returned
// Result, in order (see the package comment above; nil is exactly
// Query). Query itself is untouched, so every existing caller stays on
// the zero-cost path.
func (m *Manager) QueryTraced(q string, limit int, trace *[]ResultSignals) []Result {
	if limit <= 0 {
		limit = m.maxResults
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.QueryWith(q, limit, QueryOptions{
		FuzzyDisabled: m.fuzzyDisabled,
		Blend:         m.blend,
		Trace:         trace,
	})
}
