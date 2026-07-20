package app

import (
	"log"
	"path/filepath"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/priors"
)

// The pick-memory priors wiring: internal/priors' lookup tables built
// from the LOCAL data files and injected into the ranking blend as
// the index.Blend.Prior resolver (the blend itself is internal/index
// blend.go; the knob config search.priors -- ON by default, zero
// value = on per the tray.disabled convention; the off switch is a
// debug escape hatch for a deterministic ranking baseline, not a
// privacy option).
//
//   - Data sources, both read-only: <configDir>/telemetry.jsonl (+
//     .jsonl.1), the local search.telemetry pick log, and
//     <configDir>/frecency.json as the bootstrap while the log is
//     still thin. Nothing is ever written and nothing leaves the
//     machine.
//   - Refresh policy: rebuilt asynchronously at Startup and again
//     after every successful file activation (the Open/Reveal success
//     paths -- the moments new pick data can exist), single-flight
//     with one pending re-run coalescing a burst. No timers: an idle
//     app re-reads nothing.
//   - Disabled (the escape hatch) = nothing: no store, no file reads,
//     no goroutines, and the Manager's blend never carries a Prior,
//     so ordering is byte-identical to the layer not existing (pinned
//     in internal/index).
const priorsTelemetryLog = "telemetry.jsonl"

// startPriors builds the priors store once at Startup (unless config
// search.priors.disabled opted out), installs its resolver on the
// blend (piggybacking on the frecency blend when that layer is
// enabled; a prior-only blend otherwise), and kicks the initial
// asynchronous table build. Best-effort throughout: an unresolvable
// config dir logs one line and leaves the feature off.
func (a *App) startPriors() {
	if a.opt.Priors.Disabled {
		log.Printf("priors: pick-memory priors disabled in config (debug escape hatch)")
		return
	}
	dir, err := config.Dir()
	if err != nil {
		log.Printf("priors: %v (pick-memory priors disabled)", err)
		return
	}
	store := priors.New(priors.Options{})
	a.priorsMu.Lock()
	a.priorsStore = store
	a.priorsDir = dir
	a.priorsMu.Unlock()

	// The resolver rides the SAME blend the frecency layer swaps
	// (setFrecencyCwd derives future copies from frecBlend, so the
	// Prior survives cwd swaps); with frecency disabled the blend
	// carries only the Prior and the engine still activates it.
	a.frecMu.Lock()
	a.frecBlend.Prior = store.PriorFunc
	b := a.frecBlend
	a.frecMu.Unlock()
	if a.manager != nil {
		a.manager.SetBlend(&b)
	}
	a.kickPriorsRefresh()
}

// applyPriors is the live-apply engine's search.priors hook (the
// applyTelemetry shape): disable clears the store and detaches the
// blend's Prior resolver; enable (re)builds the layer exactly like
// startPriors and kicks a table build. An in-flight refresh finishes
// against the detached store harmlessly (the blend no longer holds
// its resolver), and an unresolvable config dir is a reported apply
// error, unlike startPriors' quiet degrade -- the user just asked
// for the feature, so the save/apply report should say why it
// stayed off.
func (a *App) applyPriors(next *config.Config) error {
	if next.Search.Priors.Disabled {
		a.priorsMu.Lock()
		was := a.priorsStore != nil
		a.priorsStore = nil
		a.priorsMu.Unlock()
		if was {
			a.frecMu.Lock()
			a.frecBlend.Prior = nil
			b := a.frecBlend
			a.frecMu.Unlock()
			if a.manager != nil {
				if b.Active() {
					a.manager.SetBlend(&b)
				} else {
					a.manager.SetBlend(nil)
				}
			}
			log.Printf("priors: pick-memory priors disabled (debug escape hatch)")
		}
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		a.priorsMu.Lock()
		a.priorsStore = nil
		a.priorsMu.Unlock()
		return err
	}
	store := priors.New(priors.Options{})
	a.priorsMu.Lock()
	a.priorsStore = store
	a.priorsDir = dir
	a.priorsMu.Unlock()
	a.frecMu.Lock()
	a.frecBlend.Prior = store.PriorFunc
	b := a.frecBlend
	a.frecMu.Unlock()
	if a.manager != nil {
		a.manager.SetBlend(&b)
	}
	a.kickPriorsRefresh()
	return nil
}

// kickPriorsRefresh schedules one asynchronous table rebuild:
// single-flight, with at most one pending re-run remembered while a
// build is in flight (a burst of activations coalesces). No-op while
// the feature is off or after Shutdown.
func (a *App) kickPriorsRefresh() {
	a.priorsMu.Lock()
	defer a.priorsMu.Unlock()
	if a.priorsStore == nil || a.priorsClosed {
		return
	}
	if a.priorsBusy {
		a.priorsAgain = true
		return
	}
	a.priorsBusy = true
	a.priorsWG.Add(1)
	go a.runPriorsRefresh()
}

// runPriorsRefresh executes rebuilds until no re-run is pending,
// then clears the busy flag. Runs on its own goroutine, tracked by
// priorsWG so Shutdown can drain it.
func (a *App) runPriorsRefresh() {
	defer a.priorsWG.Done()
	for {
		a.refreshPriorsNow()
		a.priorsMu.Lock()
		if !a.priorsAgain || a.priorsClosed {
			a.priorsBusy = false
			a.priorsMu.Unlock()
			return
		}
		a.priorsAgain = false
		a.priorsMu.Unlock()
	}
}

// refreshPriorsNow reads the local sources and swaps a fresh table
// generation into the store: telemetry oldest-first (the rotated .1
// generation before the live log), frecency.json as the bootstrap
// input (BuildTables applies it only while the log is thin). Read
// errors log once and degrade to whatever data did parse.
func (a *App) refreshPriorsNow() {
	a.priorsMu.Lock()
	store := a.priorsStore
	dir := a.priorsDir
	a.priorsMu.Unlock()
	if store == nil {
		return
	}
	telem := filepath.Join(dir, priorsTelemetryLog)
	var recs []priors.PickRecord
	for _, p := range []string{telem + ".1", telem} {
		r, err := priors.ReadTelemetryFile(p)
		if err != nil {
			a.priorsErrOnce.Do(func() {
				log.Printf("priors: %v (continuing with what parsed; further read errors suppressed)", err)
			})
			continue
		}
		recs = append(recs, r...)
	}
	now := time.Now()
	fw, err := priors.ReadFrecencyWeights(filepath.Join(dir, frecencyFileName), now)
	if err != nil {
		a.priorsErrOnce.Do(func() {
			log.Printf("priors: %v (continuing without the frecency bootstrap; further read errors suppressed)", err)
		})
	}
	// frecency.json also holds the namespaced app-launch usage keys
	// now ("app:<id>", recordAppPick in frecency.go). The bootstrap
	// derives FILE extension/folder pick distributions, so only real
	// paths may feed it -- an "app:open -a /Applications/X.app" key
	// would smear weight over garbage ext/dir buckets.
	for k := range fw {
		if !filepath.IsAbs(k) {
			delete(fw, k)
		}
	}
	store.SetTables(priors.BuildTables(recs, fw, now))
	a.priorsLogOnce.Do(func() {
		q, e, d := store.Counts()
		log.Printf("priors: pick-memory priors enabled: %d remembered queries, %d extension rates, %d folder rates (%s + frecency bootstrap, local only)",
			q, e, d, priorsTelemetryLog)
	})
}

// priorsConfigured reports whether the layer came up (tests).
func (a *App) priorsConfigured() bool {
	a.priorsMu.Lock()
	defer a.priorsMu.Unlock()
	return a.priorsStore != nil
}

// shutdownPriors stops future refresh kicks and is followed by the
// priorsWG drain in Shutdown (the flag keeps a completing refresh
// from re-arming behind the drain).
func (a *App) shutdownPriors() {
	a.priorsMu.Lock()
	a.priorsClosed = true
	a.priorsMu.Unlock()
	a.priorsWG.Wait()
}
