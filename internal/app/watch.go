package app

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

// The live-update wiring: once the initial index build finishes,
// startWatch brings up the internal/watch trio (Watcher + Rescanner +
// Sweeper) over the manager's roots, announces the effective backend
// to the frontend, and forwards degradation. Split from app.go, which
// keeps the App object, options, and lifecycle hooks.

// Names of the watch-layer events the Go side emits to the frontend.
const (
	// eventWatchDegraded reports that live updates became incomplete;
	// payload watchDegraded.
	eventWatchDegraded = "watch:degraded"
	// eventWatchBackend announces the effective live-watch backend
	// once, when the watch layer is up; payload watchBackend. full
	// false means the user runs a suboptimal (or off) live-watch
	// configuration, and the frontend shows a persistent notice chip
	// with the hint -- nothing about reduced coverage is ever silent.
	eventWatchBackend = "watch:backend"
)

// watchDegraded is the eventWatchDegraded payload.
type watchDegraded struct {
	Watched   int `json:"watched"`
	Dropped   int `json:"dropped"`
	Overflows int `json:"overflows"`
}

// watchBackend is the eventWatchBackend payload: which notification
// backend the watch layer runs on ("fanotify" | "inotify" | "none"),
// whether that is full whole-filesystem coverage (fanotify only), and
// -- when it is not -- a short user-facing hint the frontend surfaces
// on the notice chip.
type watchBackend struct {
	Backend string `json:"backend"`
	Full    bool   `json:"full"`
	Hint    string `json:"hint"`
}

// The user-facing hints carried by eventWatchBackend when live
// coverage is not full (empty when it is).
const (
	// hintPartialWatch: the bounded inotify hot set is live -- changes
	// under it show up in ~1s, everything else within one sweep.
	hintPartialWatch = "Partial file watching: changes outside the hot set appear within the sweep interval. Enable full coverage: see README (fanotify)."
	// hintWatchOff: the strict watcher.backend="fanotify" mode could
	// not start fanotify, so live watching is disabled outright.
	hintWatchOff = "Live file watching is off (fanotify required by config but unavailable). The index refreshes on sweeps only."
	// hintWatchFailed: the watcher itself failed to start (an OS
	// refusal, e.g. inotify instance exhaustion) -- no live events
	// from any backend.
	hintWatchFailed = "Live file watching is off (the watcher could not start). The index refreshes on sweeps only."
)

// watchBackendFor maps the watcher's reported backend to the
// eventWatchBackend payload. fanotify is the only full-coverage
// backend; everything else carries a hint the frontend must show.
func watchBackendFor(backend string) watchBackend {
	switch backend {
	case "fanotify":
		return watchBackend{Backend: backend, Full: true}
	case "none":
		return watchBackend{Backend: backend, Hint: hintWatchOff}
	default: // "inotify": the bounded per-directory hot set
		return watchBackend{Backend: backend, Hint: hintPartialWatch}
	}
}

// defaultSweepInterval is the default sweep cadence (the convergence
// bound for directories the bounded hot set does not watch live),
// used when watcher.sweepMinutes is 0.
const defaultSweepInterval = 20 * time.Minute

// watchConfig is the live watch-layer configuration startWatch
// consumes: seeded from the boot Options in New, swapped (under
// watchMu) by the config index-layer applier so a live rebuild picks
// the new knobs up. The boot Options themselves stay frozen.
type watchConfig struct {
	rescanEvery   time.Duration
	maxWatches    int
	sweepInterval time.Duration
	sweepDisabled bool
	watchExcludes []string
	backend       string
}

// watchConfigFrom maps the watcher-relevant config sections to a
// watchConfig, exactly the wiring main.go performs at boot.
func watchConfigFrom(cfg *config.Config) watchConfig {
	return watchConfig{
		rescanEvery:   time.Duration(cfg.RescanIntervalMinutes) * time.Minute,
		maxWatches:    cfg.Watcher.MaxWatches,
		sweepInterval: time.Duration(cfg.Watcher.SweepMinutes) * time.Minute,
		sweepDisabled: cfg.Watcher.SweepDisabled,
		watchExcludes: cfg.Watcher.WatchExcludes,
		backend:       cfg.Watcher.Backend,
	}
}

// startWatch starts the live-update layer: the Watcher over the
// manager's roots (fanotify or the bounded inotify hot set; filtering
// events through the same Excluder semantics the walks use, honoring
// the watcher.* budget and watch-only excludes, reporting degradation
// to the frontend), the Rescanner for periodic and requested full
// rebuilds, and the Sweeper whose passes converge everything the hot
// set does not cover (its watermark starts at this call's entry time
// -- the just-finished initial build vouches for everything older).
// watcher.sweepDisabled skips the Sweeper and logs a LOUD warning
// instead: the coverage invariant (tiers differ only in latency) then
// holds only through full rescans. After everything is up it waits
// for the watcher's initial registration (ctx-abortable, so Shutdown
// cuts the wait), logs one loud behavior-contract summary including
// the sweep state, and emits the one-time eventWatchBackend notice --
// with the setcap grant command logged whenever coverage is not full.
// It is skipped when Shutdown already ran.
func (a *App) startWatch() {
	a.watchMu.Lock()
	cfg := a.watchCfg
	a.watchMu.Unlock()
	watermark := time.Now()
	ex, err := index.NewExcluder(a.manager.Excludes())
	if err != nil {
		// The initial build would have failed on the same patterns and
		// returned before reaching here; a nil Excluder (matches
		// nothing) still keeps this path safe.
		log.Printf("watch: bad exclude patterns: %v", err)
		ex = nil
	}
	watchEx, err := index.NewExcluder(cfg.watchExcludes)
	if err != nil {
		// Same stance: a bad watch-only pattern costs the feature, not
		// the watch layer (nil excludes nothing from watching).
		log.Printf("watch: bad watcher.watchExcludes patterns: %v", err)
		watchEx = nil
	}
	w := watch.New(a.manager, a.manager.Roots(), ex, watch.Options{
		MaxWatches: cfg.maxWatches,
		WatchEx:    watchEx,
		OnDegraded: a.emitDegraded,
		Backend:    cfg.backend,
	})
	r := watch.NewRescanner(a.manager, w, watch.RescanOptions{Interval: cfg.rescanEvery})
	sweepEvery := cfg.sweepInterval
	if sweepEvery <= 0 {
		sweepEvery = defaultSweepInterval
	}
	var s *watch.Sweeper
	if cfg.sweepDisabled {
		log.Printf("watch: sweeps disabled in config; directories without live watches converge only at full rescans (!rescan or rescanIntervalMinutes)")
	} else {
		s = watch.NewSweeper(a.manager, w, watch.SweepOptions{
			Interval:         sweepEvery,
			InitialWatermark: watermark,
		})
	}

	a.watchMu.Lock()
	if a.shuttingDown {
		a.watchMu.Unlock()
		return
	}
	wErr := w.Start()
	if wErr != nil {
		log.Printf("watch: live updates unavailable (sweeps and rescans still work): %v", wErr)
	}
	if err := r.Start(); err != nil {
		log.Printf("watch: rescanner failed to start: %v", err)
	}
	if s != nil {
		if err := s.Start(); err != nil {
			log.Printf("watch: sweeper failed to start: %v", err)
		}
	}
	a.watcher, a.rescanner, a.sweeper = w, r, s
	kick := a.rescanOnWatchUp
	a.rescanOnWatchUp = false
	a.watchMu.Unlock()
	if kick {
		// The just-finished initial walk latched roots/excludes from
		// before a config change: converge in the background.
		r.Request()
	}

	if wErr == nil {
		// The summary numbers are only real once the initial
		// registration pass finished; the pass aborts promptly on
		// Shutdown, which also unblocks this wait.
		<-w.InitialRegistration()
	}
	st := w.Stats()
	sweepDesc := "disabled"
	if s != nil {
		sweepDesc = sweepEvery.String()
	}
	rescanDesc := "off"
	if cfg.rescanEvery > 0 {
		rescanDesc = cfg.rescanEvery.String()
	}
	log.Printf("watch: backend %s: %d/%d dirs live-watched (budget %d); sweep interval %s; full rescan interval %s",
		st.Backend, st.WatchedDirs, st.IndexedDirs, st.Budget, sweepDesc, rescanDesc)

	// Announce the effective backend to the frontend exactly once. A
	// non-full backend is a user-visible state, never a silent one:
	// the frontend keeps a notice chip up, and the log gets the exact
	// capability-grant command that would enable full coverage. The
	// grant line goes first so observing the event implies the line is
	// written (tests synchronize on the emit).
	wb := watchBackendFor(st.Backend)
	if wErr != nil {
		// The watcher itself never started, so no backend delivers
		// anything -- report that honestly as "none" with its own
		// reason instead of parroting a backend name that is not live.
		wb = watchBackend{Backend: "none", Hint: hintWatchFailed}
	}
	if !wb.Full {
		a.logFanotifyGrant()
	}
	a.emitEvent(eventWatchBackend, wb)
}

// logFanotifyGrant logs -- once, linux only (fanotify does not exist
// elsewhere) -- the exact command that grants the running binary
// full-filesystem watching. The path prefers the STABLE spelling of
// the binary (the PATH shim or the argv[0] symlink proven to be this
// very binary) over the fully resolved os.Executable, exactly like
// the GNOME keybinding command in hotkey.go: a versioned install dir
// (Homebrew Cellar, Nix, stow) dies on the next upgrade, and file
// capabilities are re-granted per installed path.
func (a *App) logFanotifyGrant() {
	a.grantOnce.Do(func() {
		if a.plat.goos != "linux" || a.plat.executable == nil {
			return
		}
		exe, err := a.plat.executable()
		if err != nil || exe == "" {
			return // no path to print; the README documents the command
		}
		if !filepath.IsAbs(exe) {
			if abs, aerr := filepath.Abs(exe); aerr == nil {
				exe = abs
			}
		}
		args0 := ""
		if a.plat.args0 != nil {
			args0 = a.plat.args0()
		}
		exe = platform.StableExecutable(exe, args0)
		log.Printf("watch: enable full-filesystem watching with: sudo setcap cap_sys_admin,cap_dac_read_search+ep %s", exe)
	})
}

// emitDegraded forwards watcher degradation to the frontend (the
// watcher calls it at most once, when it first degrades).
func (a *App) emitDegraded(s watch.Stats) {
	a.emitEvent(eventWatchDegraded, watchDegraded{
		Watched:   s.WatchedDirs,
		Dropped:   s.DroppedWatches,
		Overflows: s.Overflows,
	})
}

// restartIndexLayer is the config live-apply path for everything
// index-shaped (roots, excludes, watcher.*, rescanIntervalMinutes):
// swap the Manager's roots/excludes and the live watch configuration,
// then rebuild the watch trio through the exact startWatch path --
// stopping in Shutdown's order first -- and kick a background rescan
// so the index converges to the new scope while queries keep serving
// the previous store. Serialized by the applyConfig pass (one apply at
// a time), idempotent, nil-manager-safe.
//
// The not-yet-up states are handled without double-building: while the
// initial walk is still running, the new values are just stored (the
// walk's completion path starts the watch layer with them; one rescan
// is requested then, because the walk latched the previous scope), and
// before Startup nothing more than the stores is needed. A FAILED
// initial build (the trio never came up) is revived with a fresh
// background build under the new configuration -- fixing a broken
// exclude pattern in the editor must bring the index back without a
// restart.
func (a *App) restartIndexLayer(next *config.Config) error {
	if a.manager == nil {
		return nil
	}
	a.manager.SetRoots(next.Roots)
	a.manager.SetExcludes(next.Excludes)

	a.watchMu.Lock()
	a.watchCfg = watchConfigFrom(next)
	if a.shuttingDown {
		a.watchMu.Unlock()
		return nil
	}
	w, r, s := a.watcher, a.rescanner, a.sweeper
	a.watcher, a.rescanner, a.sweeper = nil, nil, nil
	if w == nil && r == nil {
		switch {
		case a.buildCancel != nil && !a.buildFinished:
			// The initial walk is in flight with the previous scope:
			// its completion path starts the watch layer (reading the
			// new configuration) and then converges via one rescan.
			a.rescanOnWatchUp = true
			a.watchMu.Unlock()
			log.Printf("config: index scope updated; the running initial build finishes first, then a rescan converges the index")
		case a.buildFinished:
			// The initial build ended WITHOUT bringing the trio up: it
			// failed. Run a fresh one under the new configuration.
			ctx, cancel := context.WithCancel(context.Background())
			a.buildCancel = cancel
			a.buildFinished = false
			a.watchMu.Unlock()
			log.Printf("config: index scope updated; retrying the initial build")
			go a.buildIndex(ctx)
		default:
			// Pre-Startup: the stored values are all Startup needs.
			a.watchMu.Unlock()
		}
		return nil
	}
	a.watchMu.Unlock()

	// Shutdown's stop order: rescanner (may call back into the
	// watcher), sweeper (reconciles through the watcher), watcher.
	if r != nil {
		r.Stop()
	}
	if s != nil {
		s.Stop()
	}
	if w != nil {
		w.Stop()
	}
	a.startWatch()
	return a.requestRescan()
}
