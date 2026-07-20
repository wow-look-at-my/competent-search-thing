package app

import (
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/telemetry"
)

// The ranking-log wiring: config search.telemetry bounds the
// ALWAYS-ON local impression/pick log at <configDir>/telemetry.jsonl
// (internal/telemetry). There is deliberately no off switch -- the
// log is private by staying on the machine, and deleting the file is
// always safe (recording just starts fresh); the only way the layer
// stays nil is the unresolvable-config-dir degrade (a log with
// nowhere to live).
//
//   - Search requests the ranking-signals trace (index.ResultSignals
//     via Manager.QueryTraced) and stashes the delivered impression
//     in a small query ring; with a nil layer (the degrade path),
//     Search is exactly Manager.Query, byte for byte.
//   - The frontend calls RecordPick after an activation actually ran
//     (beside its commitHistory calls); the report carries row
//     IDENTITIES only, and the app re-validates everything echoed
//     back (defense in depth, the RunPluginAction stance) then joins
//     the file rows to their impression-time signals from the ring.
//     Feature values NEVER come from the frontend.
//   - Appends run async off the activation path (the recordOpen
//     pattern: telWG + one-shot error log); Shutdown drains them.
//   - A nil layer (disabled, or pre-Startup) no-ops everything, so
//     the frontend needs no config fetch and newTestApp needs no
//     extra wiring.
const telemetryFileName = "telemetry.jsonl"

// telemetryRingSize is how many recent query impressions the layer
// keeps for the pick join. Picks follow their query within one bar
// interaction, so a handful tolerates the async window; an evicted
// entry logs the pick without file features, flagged, harmless.
const telemetryRingSize = 8

// telImpression is one stashed delivered impression: the trimmed
// query, whether the blend ordered it, and the per-path signals.
type telImpression struct {
	query       string
	blendActive bool
	byPath      map[string]index.ResultSignals
}

// telemetryLayer is the running log's state: the append-only store
// plus the impression ring. Built once by startTelemetry; nil only
// on the unresolvable-config-dir degrade path.
type telemetryLayer struct {
	store *telemetry.Store

	mu   sync.Mutex
	ring [telemetryRingSize]*telImpression
	next int
}

// startTelemetry brings the always-on layer up once, at Startup. An
// unresolvable config dir degrades the log to off with one line (a
// log with nowhere to live is pointless) -- the ONLY path that
// leaves the layer nil.
func (a *App) startTelemetry() {
	tc := a.opt.Telemetry
	dir, err := config.Dir()
	if err != nil {
		log.Printf("telemetry: %v (ranking log disabled)", err)
		return
	}
	l := &telemetryLayer{
		store: telemetry.New(filepath.Join(dir, telemetryFileName), tc.MaxSizeKB),
	}
	a.telMu.Lock()
	a.tel = l
	a.telMu.Unlock()
	log.Printf("telemetry: ranking log on (local-only, never leaves this machine; %s)", l.store.Path())
}

// applyTelemetry is the live-apply engine's search.telemetry hook
// (the applyStats teardown-plus-rebuild shape): rebuild the
// always-on layer with the incoming size bound -- maxSizeKB is the
// section's only knob now. The impression ring restarts empty, so a
// pick racing the apply logs Joined=false -- the documented,
// harmless eviction behavior -- and in-flight async appends hold
// their own store reference, draining through telWG as before. An
// unresolvable config dir is a real apply error here (unlike
// startTelemetry's quiet degrade): the save/apply report should say
// why the log went dark.
func (a *App) applyTelemetry(next *config.Config) error {
	tc := next.Search.Telemetry
	dir, err := config.Dir()
	if err != nil {
		a.telMu.Lock()
		a.tel = nil
		a.telMu.Unlock()
		return err
	}
	l := &telemetryLayer{
		store: telemetry.New(filepath.Join(dir, telemetryFileName), tc.MaxSizeKB),
	}
	a.telMu.Lock()
	a.tel = l
	a.telMu.Unlock()
	log.Printf("telemetry: ranking log rebuilt (size cap %d KiB; local-only at %s)", tc.MaxSizeKB, l.store.Path())
	return nil
}

// telLayer returns the telemetry layer; nil before Startup or after
// the unresolvable-config-dir degrade.
func (a *App) telLayer() *telemetryLayer {
	a.telMu.Lock()
	defer a.telMu.Unlock()
	return a.tel
}

// queryWithTelemetry runs Search's index query. With the layer off
// (the default) it is exactly Manager.Query; enabled, it requests the
// signals trace and stashes the delivered impression for a later
// RecordPick join. The stash is a per-query map build over at most
// maxResults rows -- microseconds, and only when opted in.
func (a *App) queryWithTelemetry(q string) []Result {
	l := a.telLayer()
	// An ACTIVE arbiter also wants the delivered impression -- its
	// emission seam compares plugin rows against the best file row of
	// the same query. Inactive (the normal case) it requests nothing,
	// keeping the default path exactly Manager.Query.
	arb := a.activeArbLayer()
	if l == nil && arb == nil {
		return a.manager.Query(q, 0)
	}
	var trace []index.ResultSignals
	res := a.manager.QueryTraced(q, 0, &trace)
	if l != nil {
		l.stash(q, a.manager.Blend().Active(), trace)
	}
	if arb != nil {
		arb.stash(q, trace)
	}
	return res
}

// stash records one delivered impression, evicting the oldest ring
// slot.
func (l *telemetryLayer) stash(query string, blendActive bool, sig []index.ResultSignals) {
	byPath := make(map[string]index.ResultSignals, len(sig))
	for _, s := range sig {
		byPath[s.Path] = s
	}
	imp := &telImpression{query: query, blendActive: blendActive, byPath: byPath}
	l.mu.Lock()
	l.ring[l.next] = imp
	l.next = (l.next + 1) % len(l.ring)
	l.mu.Unlock()
}

// lookup returns the newest stashed impression for query, or nil.
func (l *telemetryLayer) lookup(query string) *telImpression {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.ring)
	for i := 0; i < n; i++ {
		imp := l.ring[((l.next-1-i)%n+n)%n]
		if imp != nil && imp.query == query {
			return imp
		}
	}
	return nil
}

// RecordPick logs one delivered-impression-plus-pick record to the
// local ranking log. The frontend calls it fire-and-forget after an
// activation actually ran (beside AddHistory); with a nil layer (the
// config-dir degrade) or a blank query it is a silent no-op, so the
// frontend needs no config fetch and always calls it.
// Everything the frontend echoes back is re-validated here (defense
// in depth, like RunPluginAction), the file rows are joined to their
// impression-time ranking signals from the query ring -- the report
// itself can never inject feature values (plugin-row titles, which
// only the frontend knows, are the one display field it contributes)
// -- and the append runs async off the activation path.
func (a *App) RecordPick(rep telemetry.PickReport) error {
	l := a.telLayer()
	if l == nil {
		return nil
	}
	q := strings.TrimSpace(rep.Query)
	if q == "" {
		// Blank queries are never telemetry material (the AddHistory
		// standard: cheat-sheet picks carry no ranking information).
		return nil
	}
	if err := telemetry.ValidatePickReport(rep); err != nil {
		return err
	}
	rec := l.buildRecord(q, rep)
	a.telWG.Add(1)
	go func() {
		defer a.telWG.Done()
		if err := l.store.Append(rec); err != nil {
			a.telErrOnce.Do(func() {
				log.Printf("telemetry: appending a pick record: %v (further write errors suppressed)", err)
			})
			return
		}
		// The pick is on disk: count it toward the arbiter's
		// every-N retrain (a no-op while that layer is off).
		a.noteArbiterPick()
	}()
	return nil
}

// buildRecord assembles the v1 log record from a VALIDATED report:
// row identities from the report, feature values exclusively from the
// stashed impression (zero, with Joined false, when the ring no
// longer holds the query). The store stamps ts/v on append.
func (l *telemetryLayer) buildRecord(q string, rep telemetry.PickReport) telemetry.Record {
	imp := l.lookup(q)
	rec := telemetry.Record{
		Query:       q,
		BlendActive: imp != nil && imp.blendActive,
		Joined:      imp != nil,
		Shown:       make([]telemetry.ShownRow, len(rep.Shown)),
	}
	for i, ref := range rep.Shown {
		row := telemetry.ShownRow{Rank: i, Kind: ref.Kind}
		if ref.Kind == telemetry.KindFile {
			row.Path = ref.Path
			row.Depth = pathDepth(ref.Path)
			row.Ext = filepath.Ext(ref.Path)
			if imp != nil {
				if sig, ok := imp.byPath[ref.Path]; ok {
					row.Class = int(sig.Class)
					row.EffClass = int(sig.EffClass)
					row.Align = int(sig.Align)
					row.Boost = sig.Boost
					row.Recency = sig.Recency
					row.Cwd = sig.Cwd
					row.Penalty = sig.Penalty
					row.IsDir = sig.IsDir
				}
			}
		} else {
			row.Plugin = ref.Plugin
			row.Score = ref.Score
			row.Title = ref.Title
		}
		rec.Shown[i] = row
	}
	target := rep.Shown[rep.Picked.Rank]
	rec.Picked = telemetry.PickedRow{
		Rank:     rep.Picked.Rank,
		Kind:     target.Kind,
		Path:     target.Path,
		Plugin:   target.Plugin,
		Action:   rep.Picked.Action,
		Revealed: rep.Picked.Revealed,
	}
	return rec
}

// pathDepth counts a path's non-empty components ('/' and '\' both
// split -- the frecency penalty.go convention).
func pathDepth(p string) int {
	return len(strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}))
}
