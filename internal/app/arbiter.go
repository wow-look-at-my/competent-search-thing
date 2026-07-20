package app

import (
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wow-look-at-my/competent-search-thing/internal/arbiter"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// The learned-arbitration wiring: internal/arbiter's pairwise pick
// model, trained from the LOCAL ranking log and applied at the two
// composition seams (config search.arbiter -- ON by default, zero
// value = on per the tray.disabled convention; the off switch is a
// debug escape hatch / kill switch, not a privacy option):
//
//   - Within the file list: the blend's Model resolver
//     (index.Blend.Model, riding the SAME frecBlend the priors and
//     cwd layers swap) adds the model's clamped per-candidate delta
//     -- within-class nudges only, before Search returns, so the
//     frontend paints exactly once (no flicker by construction).
//   - Across sources: every plugin emission passes through
//     arbitrateEmission on its way to the "plugin:results" event --
//     rows re-ordered within their section by model score, and a
//     section may be promoted above the file rows when its best row
//     outscores the best file row of the same query (the file side
//     of that comparison comes from the layer's own impression ring,
//     stashed by Search's signals trace). Both happen BEFORE the
//     event is emitted, through the existing channel: zero new
//     frontend calls on the paint path.
//
// ACTIVATION GATE (internal/arbiter Train): the model participates
// only past arbiter.MinPicks joined picks AND a time-split holdout
// accuracy check against the delivered order. Until then -- and
// whenever training refuses -- the layer is INERT: emissions pass
// through untouched (same value, same Priority), the blend resolver
// answers nil per query, and ordering is byte-identical to the
// feature being off. Retraining runs asynchronously at Startup/apply
// and after every arbiter.RetrainEvery successfully appended picks
// (the priors single-flight refresh pattern). New picks only accrue
// while the search.telemetry ranking log is also on (both are, by
// default) -- the log is the only data source, and everything stays
// on this machine.
const arbiterLogName = "telemetry.jsonl"

// arbImpression is one stashed file-row impression: the trimmed
// query and the delivered rows' ranking signals, kept so a plugin
// emission for the same query can compare its rows against the best
// file offering.
type arbImpression struct {
	query string
	sig   []index.ResultSignals
}

// arbiterLayer is the enabled feature's state: the model store plus
// the impression ring. Built by startArbiter/applyArbiter; nil while
// the feature is off.
type arbiterLayer struct {
	store *arbiter.Store
	dir   string

	mu   sync.Mutex
	ring [telemetryRingSize]*arbImpression
	next int
}

// stash records one delivered file impression (the telemetryLayer
// ring shape).
func (l *arbiterLayer) stash(query string, sig []index.ResultSignals) {
	imp := &arbImpression{query: query, sig: sig}
	l.mu.Lock()
	l.ring[l.next] = imp
	l.next = (l.next + 1) % len(l.ring)
	l.mu.Unlock()
}

// lookup returns the newest stashed impression for query, or nil.
func (l *arbiterLayer) lookup(query string) *arbImpression {
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

// startArbiter brings the layer up once, at Startup, when config
// opted in: build the store, install the blend resolver, kick the
// initial asynchronous training run. Best-effort like startPriors:
// an unresolvable config dir logs one line and leaves the feature
// off.
func (a *App) startArbiter() {
	if a.opt.Arbiter.Disabled {
		log.Printf("arbiter: learned arbitration disabled in config (debug escape hatch)")
		return
	}
	dir, err := config.Dir()
	if err != nil {
		log.Printf("arbiter: %v (learned arbitration disabled)", err)
		return
	}
	a.installArbiter(dir)
}

// applyArbiter is the live-apply engine's search.arbiter hook (the
// applyPriors shape): disable swaps the layer out and detaches the
// blend's Model resolver -- both seams instantly inert, mid-session
// -- while enable (re)builds the layer exactly like startArbiter and
// kicks a training run (the activation gate still applies, so
// enabling changes nothing visible until the model qualifies). An
// unresolvable config dir is a reported apply error, unlike
// startArbiter's quiet degrade -- the user just asked for the
// feature, so the save/apply report should say why it stayed off.
func (a *App) applyArbiter(next *config.Config) error {
	if next.Search.Arbiter.Disabled {
		a.arbMu.Lock()
		was := a.arb != nil
		a.arb = nil
		a.arbActive = false
		a.arbMu.Unlock()
		if was {
			a.frecMu.Lock()
			a.frecBlend.Model = nil
			b := a.frecBlend
			a.frecMu.Unlock()
			if a.manager != nil {
				if b.Active() {
					a.manager.SetBlend(&b)
				} else {
					a.manager.SetBlend(nil)
				}
			}
			log.Printf("arbiter: learned arbitration disabled (debug escape hatch)")
		}
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		a.arbMu.Lock()
		a.arb = nil
		a.arbActive = false
		a.arbMu.Unlock()
		return err
	}
	a.installArbiter(dir)
	return nil
}

// installArbiter builds a fresh layer, wires the blend's Model
// resolver (riding frecBlend so the cwd stash's re-swaps keep it,
// the priors pattern), and kicks one asynchronous training run.
// Idempotent: a re-install swaps a fresh empty store whose next
// refresh repopulates it.
func (a *App) installArbiter(dir string) {
	l := &arbiterLayer{store: arbiter.NewStore(), dir: dir}
	a.arbMu.Lock()
	a.arb = l
	a.arbActive = false
	a.arbMu.Unlock()
	a.frecMu.Lock()
	a.frecBlend.Model = a.arbBlendModel
	b := a.frecBlend
	a.frecMu.Unlock()
	if a.manager != nil {
		a.manager.SetBlend(&b)
	}
	a.kickArbiterRefresh()
}

// arbLayer returns the arbiter layer; nil before Startup or while
// config keeps the feature off.
func (a *App) arbLayer() *arbiterLayer {
	a.arbMu.Lock()
	defer a.arbMu.Unlock()
	return a.arb
}

// activeArbLayer returns the layer only while a gate-passed model is
// installed -- the state in which Search must stash impressions for
// the cross-source comparison. Nil otherwise, keeping the inactive
// query path byte-identical.
func (a *App) activeArbLayer() *arbiterLayer {
	l := a.arbLayer()
	if l == nil || l.store.Current() == nil {
		return nil
	}
	return l
}

// arbBlendModel is the index.Blend.Model resolver: consulted once
// per query, it answers nil -- no term at all -- unless the layer is
// up AND the activation gate has passed; active, it converts each
// merged candidate's ranking components to the model's row shape and
// returns the clamped within-file delta. The method value is stable
// across enable/disable cycles, so stale blend copies degrade to nil
// harmlessly.
func (a *App) arbBlendModel(query string) func(string, index.ResultSignals) float64 {
	l := a.arbLayer()
	if l == nil {
		return nil
	}
	m := l.store.Current()
	if m == nil {
		return nil
	}
	base := arbiter.Row{
		Kind:  arbiter.KindFile,
		Query: query,
		Hour:  a.plat.now().Local().Hour(),
	}
	return func(path string, sig index.ResultSignals) float64 {
		r := base
		r.Class = int(sig.Class)
		r.EffClass = int(sig.EffClass)
		r.Align = int(sig.Align)
		r.Boost = sig.Boost
		r.Recency = sig.Recency
		r.Cwd = sig.Cwd
		r.Penalty = sig.Penalty
		r.IsDir = sig.IsDir
		r.Depth = pathDepth(path)
		r.Ext = filepath.Ext(path)
		return m.FileDelta(r)
	}
}

// arbitrateEmission is the cross-source seam: every QueryPlugins
// emission passes through here on its way to the "plugin:results"
// event. Inactive (layer off, or the gate unpassed) it returns the
// emission UNTOUCHED -- same rows, same order, same Priority, the
// pinned static behavior. Active, it scores each row, re-orders the
// section's rows by model score (stable, so ties keep the engine
// order), and promotes the section above the file results (effective
// Priority 1) when its best row outscores the best file row stashed
// for the same query; a section that already renders above files
// keeps its priority, and a missing stash (no file impression for
// this query yet) leaves placement alone. The frontend needs no new
// logic: it already renders priority > 0 sections above the files
// and reconciles selection by row identity.
func (a *App) arbitrateEmission(query string, em plugin.Emission) plugin.Emission {
	l := a.arbLayer()
	if l == nil || len(em.Results) == 0 {
		return em
	}
	m := l.store.Current()
	if m == nil {
		return em
	}
	q := strings.TrimSpace(query)
	base := arbiter.Row{
		Kind:     arbiter.KindPlugin,
		Plugin:   em.Plugin,
		Priority: em.Priority,
		Query:    q,
		Hour:     a.plat.now().Local().Hour(),
	}
	scores := make([]float64, len(em.Results))
	best := math.Inf(-1)
	for i, res := range em.Results {
		r := base
		r.SourceRank = i
		if res.Score != nil {
			r.Score = int(*res.Score)
		}
		scores[i] = m.Score(r)
		if scores[i] > best {
			best = scores[i]
		}
	}
	ord := make([]int, len(em.Results))
	for i := range ord {
		ord[i] = i
	}
	sort.SliceStable(ord, func(x, y int) bool { return scores[ord[x]] > scores[ord[y]] })
	rows := make([]plugin.Result, len(ord))
	for x, i := range ord {
		rows[x] = em.Results[i]
	}
	em.Results = rows
	if em.Priority <= 0 {
		if fbest, ok := l.bestFileScore(m, q); ok && best > fbest {
			em.Priority = 1
		}
	}
	return em
}

// bestFileScore scores the stashed file impression for query with
// the current model and returns the best row's score; ok reports
// whether an impression existed (without one there is nothing to
// compare against and placement stays untouched).
func (l *arbiterLayer) bestFileScore(m *arbiter.Model, query string) (float64, bool) {
	imp := l.lookup(query)
	if imp == nil || len(imp.sig) == 0 {
		return 0, false
	}
	best := math.Inf(-1)
	for _, sig := range imp.sig {
		r := arbiter.Row{
			Kind:     arbiter.KindFile,
			Class:    int(sig.Class),
			EffClass: int(sig.EffClass),
			Align:    int(sig.Align),
			Boost:    sig.Boost,
			Recency:  sig.Recency,
			Cwd:      sig.Cwd,
			Penalty:  sig.Penalty,
			IsDir:    sig.IsDir,
			Depth:    pathDepth(sig.Path),
			Ext:      filepath.Ext(sig.Path),
			Query:    query,
		}
		if s := m.Score(r); s > best {
			best = s
		}
	}
	return best, true
}

// noteArbiterPick counts one successfully appended telemetry pick
// and kicks a retrain every arbiter.RetrainEvery of them -- the only
// moments new training data can exist. Called from RecordPick's
// append goroutine AFTER the record landed on disk, so the retrain
// reads a log that already contains it.
func (a *App) noteArbiterPick() {
	a.arbMu.Lock()
	if a.arb == nil || a.arbClosed {
		a.arbMu.Unlock()
		return
	}
	a.arbPicks++
	kick := a.arbPicks >= arbiter.RetrainEvery
	if kick {
		a.arbPicks = 0
	}
	a.arbMu.Unlock()
	if kick {
		a.kickArbiterRefresh()
	}
}

// kickArbiterRefresh schedules one asynchronous training run:
// single-flight, with at most one pending re-run remembered while a
// run is in flight (the priors refresh pattern). No-op while the
// feature is off or after Shutdown.
func (a *App) kickArbiterRefresh() {
	a.arbMu.Lock()
	defer a.arbMu.Unlock()
	if a.arb == nil || a.arbClosed {
		return
	}
	if a.arbBusy {
		a.arbAgain = true
		return
	}
	a.arbBusy = true
	a.arbWG.Add(1)
	go a.runArbiterRefresh()
}

// runArbiterRefresh executes training runs until no re-run is
// pending, then clears the busy flag. Runs on its own goroutine,
// tracked by arbWG so Shutdown can drain it.
func (a *App) runArbiterRefresh() {
	defer a.arbWG.Done()
	for {
		a.refreshArbiterNow()
		a.arbMu.Lock()
		if !a.arbAgain || a.arbClosed {
			a.arbBusy = false
			a.arbMu.Unlock()
			return
		}
		a.arbAgain = false
		a.arbMu.Unlock()
	}
}

// refreshArbiterNow reads the telemetry log (oldest first: the
// rotated .1 generation before the live file), trains, and swaps the
// outcome into the store. Read errors log once and degrade to
// whatever parsed; the gate's verdict is logged on the first run and
// on every activation flip so the user can see the model qualify.
func (a *App) refreshArbiterNow() {
	a.arbMu.Lock()
	l := a.arb
	a.arbMu.Unlock()
	if l == nil {
		return
	}
	base := filepath.Join(l.dir, arbiterLogName)
	var imps []arbiter.Impression
	for _, p := range []string{base + ".1", base} {
		r, err := arbiter.ReadLogFile(p)
		if err != nil {
			a.arbErrOnce.Do(func() {
				log.Printf("arbiter: %v (continuing with what parsed; further read errors suppressed)", err)
			})
			continue
		}
		imps = append(imps, r...)
	}
	out := arbiter.Train(imps)
	l.store.SetOutcome(out)

	active := out.Model != nil
	a.arbMu.Lock()
	flipped := active != a.arbActive
	a.arbActive = active
	a.arbMu.Unlock()
	logLine := func() {
		log.Printf("arbiter: learned arbitration %s (trains on %s, local only)", out.Reason, arbiterLogName)
	}
	a.arbLogOnce.Do(func() {
		logLine()
		flipped = false // the first verdict is already reported
	})
	if flipped {
		logLine()
	}
}

// arbiterConfigured reports whether the layer came up (tests).
func (a *App) arbiterConfigured() bool {
	return a.arbLayer() != nil
}

// shutdownArbiter stops future refresh kicks and is followed by the
// arbWG drain in Shutdown (the flag keeps a completing run from
// re-arming behind the drain).
func (a *App) shutdownArbiter() {
	a.arbMu.Lock()
	a.arbClosed = true
	a.arbMu.Unlock()
	a.arbWG.Wait()
}
