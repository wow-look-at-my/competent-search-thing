// Package app holds the application object that is bound to the Wails
// frontend. Every exported method on App is callable from JavaScript as
// window.go.app.App.<Method>.
package app

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/history"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

// Names of the events the Go side emits to the frontend.
const (
	// eventIndexProgress reports index build progress; payload
	// indexProgress.
	eventIndexProgress = "index:progress"
	// eventWatchDegraded reports that live updates became incomplete;
	// payload watchDegraded.
	eventWatchDegraded = "watch:degraded"
	// eventWatchBackend announces the effective live-watch backend
	// once, when the watch layer is up; payload watchBackend. full
	// false means the user runs a suboptimal (or off) live-watch
	// configuration, and the frontend shows a persistent notice chip
	// with the hint -- nothing about reduced coverage is ever silent.
	eventWatchBackend = "watch:backend"
	// eventShown fires after the bar was shown; no payload.
	eventShown = "app:shown"
)

// indexProgress is the eventIndexProgress payload.
type indexProgress struct {
	Indexed int     `json:"indexed"`
	Done    bool    `json:"done"`
	Seconds float64 `json:"seconds"`
}

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

// Result is a single search hit sent to the frontend. It is the index
// package's Result (json tags path/name/isDir live there); the alias
// keeps the bound method signature and the frontend contract stable.
type Result = index.Result

// Options configures an App.
type Options struct {
	// RescanEvery > 0 enables periodic full rescans at that interval
	// (wire config.RescanIntervalMinutes here); 0 disables them.
	RescanEvery time.Duration
	// WatchMaxWatches bounds the live-watch hot set (wire config's
	// watcher.maxWatches here): 0 = automatic budget, negative =
	// explicitly unlimited, positive taken as-is. See
	// watch.Options.MaxWatches.
	WatchMaxWatches int
	// SweepInterval is the reconcile-sweep cadence (wire config's
	// watcher.sweepMinutes here, as a Duration); 0 selects the watch
	// layer's default (20 minutes).
	SweepInterval time.Duration
	// SweepDisabled turns the sweep tier off entirely (wire config's
	// watcher.sweepDisabled here). startWatch then logs a loud
	// warning: without sweeps, directories without a live watch
	// converge only at full rescans.
	SweepDisabled bool
	// WatchExcludes are exclude patterns applied to live watching
	// only (wire config's watcher.watchExcludes here): matching
	// directories and their subtrees stay indexed and swept but never
	// hold a watch. See watch.Options.WatchEx.
	WatchExcludes []string
	// WatchBackend selects the notification backend (wire config's
	// watcher.backend here): "auto"/"" = automatic detection,
	// "fanotify" = strict fanotify-or-nothing, "inotify" = skip the
	// fanotify probe. See watch.Options.Backend; the effective backend
	// is announced to the frontend via eventWatchBackend either way.
	WatchBackend string
	// Hotkey is the config hotkey string ("alt+space"); empty disables
	// the global hotkey.
	Hotkey string
	// IPC is the single-instance IPC server the CLI layer acquired
	// (nil when IPC is unavailable; everything degrades to no-ops).
	// Startup wires the toggle/show/hide handlers into it and the App
	// owns it from then on: Shutdown closes it.
	IPC *ipc.Server
	// ShowOnStartup asks for the bar to be shown as soon as the
	// frontend is ready (set when a CLI toggle/show started the app).
	ShowOnStartup bool
	// TrayDisabled turns the tray icon off (wire config's
	// tray.disabled here); the default zero value keeps it on.
	TrayDisabled bool
	// HistoryPersistDisabled keeps the query history in memory only
	// (wire config's history.persistDisabled here); the default zero
	// value persists it to <configDir>/history.json. See history.go.
	HistoryPersistDisabled bool
	// ConfigNotes are the human-readable migration notes config.Load
	// produced (wire cfg.MigrationNotes here); Startup logs each one
	// loudly, exactly once, so a changed index scope is never silent.
	ConfigNotes []string
	// Preview is the preview pane configuration (wire config's
	// preview section here); the zero value keeps the pane off and
	// every preview method degrades to a no-op. See preview.go.
	Preview config.PreviewConfig
	// WindowWidth and WindowHeight are the effective bar window size
	// in pixels; the positioning math uses them, so they must match
	// what the native window was built with (main.go feeds both from
	// the one app.PreviewWindowSize() read -- the configured
	// window.width/height, or the preview-widened size when the pane
	// is on). Zero values fall back to the config defaults, keeping
	// bare-Options tests working.
	WindowWidth  int
	WindowHeight int
}

// App is the Wails-bound application object. It carries the Wails
// runtime context after Startup has run and owns the index manager,
// the live-update layer (watcher + rescanner), and the platform hooks
// (global hotkey, cursor-display positioning, open/reveal).
type App struct {
	manager   *index.Manager
	opt       Options
	buildOnce sync.Once
	hkOnce    sync.Once
	notesOnce sync.Once
	// grantOnce guards the one-time fanotify capability-grant log line
	// (see logFanotifyGrant).
	grantOnce sync.Once

	mu         sync.Mutex // guards ctx, visible, lastToggle, lastHide, hotkeyStop, hotkeyCancel, portalHK, hotkeyDesc, trayH, trayCancel, stats, statsCancel, lastThemeErr, domReady, pendingShow, history, launchCtx, launchCancel
	ctx        context.Context
	visible    bool
	lastToggle time.Time
	// lastHide is when Hide last ran. A toggle whose show branch runs
	// within toggleGap of it is treated as the dismiss press whose own
	// side effects (the grab FocusOut -> frontend blur -> Hide chain)
	// already hid the bar, and is dropped instead of re-summoning --
	// see toggle in window.go.
	lastHide   time.Time
	hotkeyStop func()
	// launchCtx bounds the post-launch raise-watcher goroutines (see
	// launch.go): created on first use, cancelled in Shutdown and left
	// cancelled, so late launches spawn watchers that exit instantly.
	launchCtx    context.Context
	launchCancel context.CancelFunc
	// launchOnce guards the one-time launch announce + native prep.
	launchOnce sync.Once
	// hotkeyCancel aborts the async portal/gsettings backend chain;
	// portalHK is the active portal shortcut (nil otherwise);
	// hotkeyDesc describes the effective summon trigger (see
	// hotkey.go).
	hotkeyCancel context.CancelFunc
	portalHK     portalHandle
	hotkeyDesc   string
	// Tray icon (see tray.go in this package): the running handle and
	// the cancel func aborting a Start still waiting on the bus.
	// newTray is a seam over buildTray so unit tests never dial a
	// session bus.
	trayH        trayHandle
	trayCancel   context.CancelFunc
	lastThemeErr string
	// domReady flips when the Wails OnDomReady hook fires; before
	// that the frontend cannot render, so summons (ShowOnStartup, an
	// early hotkey press or IPC command) are remembered in
	// pendingShow and executed once by DomReady.
	domReady    bool
	pendingShow bool

	// sessionOnce caches desktop session detection (hotkey backend
	// selection, the Wayland show path, and the open-windows provider
	// gate all consume it); waylandPlaceOnce guards the one-time
	// compositor-placement log and openWindowsLogOnce the one-time
	// "no open-window search on wayland" log.
	sessionOnce        sync.Once
	sessionVal         platform.Session
	waylandPlaceOnce   sync.Once
	openWindowsLogOnce sync.Once

	themeOnce    sync.Once
	watchMu      sync.Mutex
	watcher      *watch.Watcher
	rescanner    *watch.Rescanner
	sweeper      *watch.Sweeper
	themeW       *themeWatcher
	buildCancel  context.CancelFunc // cancels the initial build's walk
	shuttingDown bool

	// Plugin layer (see plugins.go). pluginGen is the current query
	// generation; emissions from older generations are dropped.
	// newRegistry is a seam over buildRegistry so tests can inject
	// fake dispatchers without touching config.json or the disk.
	// firefoxCtx/firefoxCancel bound the frequent-sites history
	// refreshes (see firefox.go): app-lifetime, shared across registry
	// reloads, cancelled in Shutdown.
	pluginOnce    sync.Once
	pluginGen     atomic.Int64
	pluginMu      sync.Mutex // guards registry, pluginCancel, appCache, firefoxCtx, firefoxCancel
	registry      dispatcher
	pluginCancel  context.CancelFunc
	appCache      *appctx.Cache
	newRegistry   func() dispatcher
	firefoxCtx    context.Context
	firefoxCancel context.CancelFunc

	trayOnce sync.Once
	newTray  func() trayHandle

	// System-stats sampler (see stats.go in this package): the running
	// source and the cancel func bounding its goroutines. newStats is
	// a seam over buildStats so unit tests never read config.json or
	// probe /proc//sys.
	statsOnce   sync.Once
	newStats    func() statsSource
	stats       statsSource
	statsCancel context.CancelFunc

	// Preview pane layer (see preview.go in this package): the
	// dispatcher (nil while the pane is disabled), the cancel func for
	// its parent context (Shutdown), and the generation gate mirroring
	// the plugin layer's pluginGen.
	previewOnce   sync.Once
	previewGen    atomic.Int64
	previewMu     sync.Mutex // guards previewDisp, previewCancel
	previewDisp   *preview.Dispatcher
	previewCancel context.CancelFunc

	// Query history (see history.go): built once at Startup, nil
	// before that -- the bound methods degrade to no-ops, which keeps
	// newTestApp working without extra wiring.
	histOnce sync.Once
	history  *history.Store

	// rt and plat are seams over the Wails runtime and the platform
	// layer. Production fills them in New; unit tests MUST replace
	// every rt member before driving code that reaches it (the real
	// runtime aborts the process without a Wails context) -- see
	// newTestApp in the tests.
	rt   runtimeSeams
	plat platformSeams
}

// New creates an App around an index manager (nil is tolerated: Search
// then returns no results and Startup skips the index build).
func New(m *index.Manager, opt Options) *App {
	a := &App{
		manager: m,
		opt:     opt,
		rt:      defaultRuntimeSeams(),
		plat:    defaultPlatformSeams(),
	}
	a.newRegistry = a.buildRegistry
	a.newTray = a.buildTray
	a.newStats = a.buildStats
	return a
}

// Startup is wired to the Wails OnStartup hook: it saves the runtime
// context, brings up the global hotkey through the session's backend
// plan (best effort; see hotkey.go), starts the tray icon (linux
// only, async, best effort; see tray.go), starts the system-stats
// sampler (idle until the bar first shows; see stats.go), wires the
// single-instance IPC handlers (when Options.IPC is set), brings the
// plugin layer up (app-context cache + registry; cheap file IO),
// builds the query-history store (best effort; see history.go),
// starts theme hot reload (best effort, see theme.go), and kicks off
// the initial index build in the background, so the window is
// responsive immediately while the walk fills the index. An
// Options.ShowOnStartup request is latched here and executed by
// DomReady, once the frontend can render.
func (a *App) Startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	if a.opt.ShowOnStartup {
		a.pendingShow = true
	}
	a.mu.Unlock()
	a.notesOnce.Do(func() {
		for _, n := range a.opt.ConfigNotes {
			log.Printf("config: %s", n)
		}
	})
	a.launchOnce.Do(a.announceLaunch)
	a.hkOnce.Do(a.registerHotkey)
	a.trayOnce.Do(a.startTray)
	a.statsOnce.Do(a.startStats)
	if a.opt.IPC != nil {
		a.opt.IPC.SetHandlers(ipc.Handlers{
			Toggle: a.toggle,
			Show:   a.showIfHidden,
			Hide:   a.Hide,
		})
	}
	a.pluginOnce.Do(a.startPlugins)
	a.previewOnce.Do(a.startPreview)
	a.histOnce.Do(a.startHistory)
	a.themeOnce.Do(a.startThemeWatch)
	if a.manager == nil {
		return
	}
	a.buildOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		a.watchMu.Lock()
		a.buildCancel = cancel
		a.watchMu.Unlock()
		go a.buildIndex(ctx)
	})
}

// DomReady is wired to the Wails OnDomReady hook: the frontend is
// loaded and can render. It executes at most one show that was
// requested earlier (Options.ShowOnStartup, or a hotkey press / IPC
// toggle/show that arrived while the frontend was still loading);
// after it has run, summons act immediately.
func (a *App) DomReady(ctx context.Context) {
	a.mu.Lock()
	if ctx != nil {
		a.ctx = ctx
	}
	a.domReady = true
	pending := a.pendingShow
	a.pendingShow = false
	a.mu.Unlock()
	if pending {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
}

// buildIndex runs the full disk walk -- forwarding progress to the log
// and to the frontend as eventIndexProgress -- then brings the
// live-update layer up. Cancelling ctx (Shutdown) aborts the walk
// mid-flight: the partial store is discarded (BuildFromDisk only swaps
// on success) and the watch layer never starts.
func (a *App) buildIndex(ctx context.Context) {
	start := a.plat.now()
	progress := func(indexed int, done bool) {
		if !done {
			log.Printf("index: indexing... %d entries", indexed)
		}
		a.emitEvent(eventIndexProgress, indexProgress{
			Indexed: indexed,
			Done:    done,
			Seconds: a.plat.now().Sub(start).Seconds(),
		})
	}
	count, dur, err := a.manager.BuildFromDisk(ctx, progress)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("index: initial build cancelled")
		} else {
			log.Printf("index: initial build failed: %v", err)
		}
		return
	}
	log.Printf("index: %d entries in %s", count, dur.Round(time.Millisecond))
	a.startWatch()
}

// defaultSweepInterval is the default sweep cadence (the convergence
// bound for directories the bounded hot set does not watch live),
// used when watcher.sweepMinutes is 0.
const defaultSweepInterval = 20 * time.Minute

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
	watermark := time.Now()
	ex, err := index.NewExcluder(a.manager.Excludes())
	if err != nil {
		// The initial build would have failed on the same patterns and
		// returned before reaching here; a nil Excluder (matches
		// nothing) still keeps this path safe.
		log.Printf("watch: bad exclude patterns: %v", err)
		ex = nil
	}
	watchEx, err := index.NewExcluder(a.opt.WatchExcludes)
	if err != nil {
		// Same stance: a bad watch-only pattern costs the feature, not
		// the watch layer (nil excludes nothing from watching).
		log.Printf("watch: bad watcher.watchExcludes patterns: %v", err)
		watchEx = nil
	}
	w := watch.New(a.manager, a.manager.Roots(), ex, watch.Options{
		MaxWatches: a.opt.WatchMaxWatches,
		WatchEx:    watchEx,
		OnDegraded: a.emitDegraded,
		Backend:    a.opt.WatchBackend,
	})
	r := watch.NewRescanner(a.manager, w, watch.RescanOptions{Interval: a.opt.RescanEvery})
	sweepEvery := a.opt.SweepInterval
	if sweepEvery <= 0 {
		sweepEvery = defaultSweepInterval
	}
	var s *watch.Sweeper
	if a.opt.SweepDisabled {
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
	a.watchMu.Unlock()

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
	if a.opt.RescanEvery > 0 {
		rescanDesc = a.opt.RescanEvery.String()
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

// Shutdown is wired to the Wails OnShutdown hook. It closes the
// single-instance IPC server first (no new summons during teardown;
// closing also unlinks the socket), releases the global hotkey
// (stopping the native listener, aborting a still-running
// portal/gsettings backend chain, and closing an active portal
// shortcut), closes the tray icon (aborting a Start still waiting on
// the bus; closing the tray's connection unregisters the icon),
// cancels the system-stats sampler's goroutines,
// cancels the in-flight plugin generation, closes the registry, and
// cancels the firefox context (aborting a frequent-sites history
// refresh mid-copy), cancels the preview dispatcher's parent context
// (aborting an in-flight preview request; see preview.go), cancels a
// still-running initial build (its walk aborts
// and logs "index: initial build cancelled"), and stops the rescanner
// first (it may be mid-rescan and calls back into the watcher to
// resync watches), then the sweeper (its passes reconcile through the
// watcher too, so it must stop before it), then the watcher, then the
// theme hot-reload watcher. Every step is bounded: an in-flight
// rescan, sweep pass, or watch resync is cancelled, never waited out,
// so quit stays fast even mid-walk on a huge index. Safe to call at
// any point, even before the watch layer came up; the shuttingDown
// flag keeps a racing startWatch from starting it afterwards.
func (a *App) Shutdown(_ context.Context) {
	if a.opt.IPC != nil {
		if err := a.opt.IPC.Close(); err != nil {
			log.Printf("ipc: close: %v", err)
		}
	}

	a.mu.Lock()
	hkStop := a.hotkeyStop
	a.hotkeyStop = nil
	hkCancel := a.hotkeyCancel
	a.hotkeyCancel = nil
	ph := a.portalHK
	a.portalHK = nil
	th := a.trayH
	a.trayH = nil
	trayCancel := a.trayCancel
	a.trayCancel = nil
	statsCancel := a.statsCancel
	a.statsCancel = nil
	a.stats = nil
	launchCancel := a.launchCancel
	a.launchCancel = nil
	if launchCancel == nil && a.launchCtx == nil {
		// Nothing was ever launched: park a pre-cancelled context so a
		// post-shutdown launch cannot arm a watcher that outlives us.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		a.launchCtx = ctx
	}
	a.mu.Unlock()
	if launchCancel != nil {
		launchCancel()
	}
	if hkCancel != nil {
		hkCancel()
	}
	if hkStop != nil {
		hkStop()
	}
	if ph != nil {
		if err := ph.Close(); err != nil {
			log.Printf("hotkey: closing the portal shortcut: %v", err)
		}
	}
	if trayCancel != nil {
		trayCancel()
	}
	if th != nil {
		if err := th.Close(); err != nil {
			log.Printf("tray: close: %v", err)
		}
	}
	if statsCancel != nil {
		statsCancel()
	}

	a.pluginMu.Lock()
	cancel := a.pluginCancel
	a.pluginCancel = nil
	reg := a.registry
	a.registry = nil
	ffCancel := a.firefoxCancel
	a.firefoxCancel = nil
	a.pluginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if reg != nil {
		reg.Close()
	}
	if ffCancel != nil {
		ffCancel()
	}

	a.shutdownPreview()

	a.watchMu.Lock()
	a.shuttingDown = true
	buildCancel := a.buildCancel
	a.buildCancel = nil
	w, r, sw, tw := a.watcher, a.rescanner, a.sweeper, a.themeW
	a.watcher, a.rescanner, a.sweeper, a.themeW = nil, nil, nil, nil
	a.watchMu.Unlock()
	if buildCancel != nil {
		buildCancel()
	}
	if r != nil {
		r.Stop()
	}
	if sw != nil {
		sw.Stop()
	}
	if w != nil {
		w.Stop()
	}
	if tw != nil {
		tw.stop()
	}
}

// Search returns index entries whose name contains query,
// case-insensitively, best matches first (limit: the configured
// MaxResults). It always returns a non-nil slice so the frontend can
// iterate without null checks. An absolute-path query with zero index
// results may yield one synthetic outside-indexed-roots hint result
// instead of nothing (see hint.go).
func (a *App) Search(query string) []Result {
	q := strings.TrimSpace(query)
	if q == "" || a.manager == nil {
		return []Result{}
	}
	res := a.manager.Query(q, 0)
	if len(res) == 0 {
		if r, ok := a.outsideRootsHint(q); ok {
			return []Result{r}
		}
		return []Result{}
	}
	return res
}

// Open launches path (or URL) with the operating system's default
// handler -- on linux through the credentialed launch path, so the
// target application's window ends focused and raised (see launch.go)
// -- and hides the bar on success.
func (a *App) Open(path string) error {
	if err := a.openTarget(path); err != nil {
		return err
	}
	a.Hide()
	return nil
}

// Reveal shows path selected in the operating system's file manager
// (credentialed on linux, like Open) and hides the bar on success.
func (a *App) Reveal(path string) error {
	if err := a.revealTarget(path); err != nil {
		return err
	}
	a.Hide()
	return nil
}
