// Package app holds the application object that is bound to the Wails
// frontend. Every exported method on App is callable from JavaScript as
// window.go.app.App.<Method>.
package app

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
	"github.com/wow-look-at-my/competent-search-thing/internal/history"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
	"github.com/wow-look-at-my/competent-search-thing/internal/progress"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

// Names of the events the Go side emits to the frontend.
const (
	// eventIndexProgress reports index build progress; payload
	// indexProgress.
	eventIndexProgress = "index:progress"
	// eventShown fires after the bar was shown; no payload.
	eventShown = "app:shown"
)

// indexProgress is the eventIndexProgress payload.
type indexProgress struct {
	Indexed int     `json:"indexed"`
	Done    bool    `json:"done"`
	Seconds float64 `json:"seconds"`
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
	// Frecency configures the frecency ranking blend (wire config's
	// search.frecency here; see frecency.go). Weights arrive
	// Normalize-repaired; Disabled leaves the whole layer unwired.
	Frecency config.FrecencyConfig
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
	// ResultsWidth is the pixel width the left results column keeps
	// while the preview pane is on -- the flag-off bar width (wire
	// config's window.width here), so the column matches what the bar
	// would be without the pane. Zero or negative values fall back to
	// config.DefaultWindowWidth in GetPreviewConfig, keeping
	// bare-Options tests working. Unused while the pane is off.
	ResultsWidth int
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

	mu         sync.Mutex // guards ctx, visible, lastToggle, lastHide, hotkeyStop, hotkeyCancel, portalHK, hotkeyDesc, trayH, trayCancel, stats, statsCancel, lastThemeErr, domReady, pendingShow, history, launchCtx, launchCancel, progress
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
	// panelOnce guards the one-time Spotlight-style panel configuration
	// DomReady applies through the plat.configurePanel seam -- DomReady
	// is the earliest point every platform has a native window to
	// configure.
	panelOnce sync.Once
	// spaceOnce guards the one-time dismiss-on-Space-change arming
	// (darwin; the plat.watchSpaceChanges seam is nil elsewhere) --
	// see startSpaceWatch in window.go.
	spaceOnce sync.Once

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

	// Icon resolution (see icons.go in this package): the resolver
	// behind the bound ResolveIcons method. newIcons is a seam over
	// buildIcons so unit tests resolve nothing.
	iconsOnce sync.Once
	newIcons  func() iconResolver
	iconsMu   sync.Mutex
	icons     iconResolver

	// System-stats sampler (see stats.go in this package): the running
	// source and the cancel func bounding its goroutines. newStats is
	// a seam over buildStats so unit tests never read config.json or
	// probe /proc//sys.
	statsOnce   sync.Once
	newStats    func() statsSource
	stats       statsSource
	statsCancel context.CancelFunc

	// Startup progress printer (see progress.go in this package): the
	// initial index build's "indexing..." line -- in-place on a TTY
	// (where it also intercepts the standard logger until Shutdown
	// restores stderr), throttled log lines elsewhere. newProgress is a
	// seam over buildProgress so unit tests never touch the real stderr
	// or the global log output; the printer is built once (Startup, or
	// buildIndex's own Once for direct-call tests) and never nil after
	// that -- a nil seam degrades to an inert io.Discard printer.
	progressOnce sync.Once
	newProgress  func() *progress.Printer
	progress     *progress.Printer

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

	// Frecency ranking (see frecency.go): the open-count store and
	// the blend the Manager serves, built once at Startup; nil/zero
	// before that or when config disables the feature (recordOpen and
	// the cwd capture then no-op). frecBlend is the base copy the cwd
	// stash derives fresh immutable Blends from. frecWG tracks the
	// layer's short-lived goroutines (state load, open recording, cwd
	// derivation) so Shutdown can drain them -- a recording racing
	// process teardown would otherwise be lost, and in tests a write
	// racing the TempDir cleanup fails the test.
	frecOnce    sync.Once
	frecErrOnce sync.Once
	frecMu      sync.Mutex // guards frecStore, frecBlend
	frecStore   *frecency.Store
	frecBlend   index.Blend
	frecWG      sync.WaitGroup

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
	a.newProgress = a.buildProgress
	a.newIcons = a.buildIcons
	return a
}

// Startup is wired to the Wails OnStartup hook: it saves the runtime
// context, wires the single-instance IPC handlers first (when
// Options.IPC is set; see the ordering note in the body), brings up
// the global hotkey through the session's backend plan (best effort;
// see hotkey.go), starts the tray icon (linux only, async, best
// effort; see tray.go), starts the system-stats sampler (idle until
// the bar first shows; see stats.go), brings the
// plugin layer up (app-context cache + registry; cheap file IO),
// builds the query-history store (best effort; see history.go),
// starts theme hot reload (best effort, see theme.go), builds the
// startup progress printer (see progress.go in this package), and
// kicks off
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
	// The IPC handlers are wired BEFORE everything else, in particular
	// before registerHotkey: on darwin the hotkey registration can
	// block briefly on the Cocoa main-loop race, and summons sent over
	// IPC during that window used to be answered "err not ready" and
	// dropped. All three handlers are safe this early: toggle and
	// showIfHidden latch pendingShow while domReady is false, and Hide
	// no-ops without a runtime ctx.
	if a.opt.IPC != nil {
		a.opt.IPC.SetHandlers(ipc.Handlers{
			Toggle: a.toggle,
			Show:   a.showIfHidden,
			Hide:   a.Hide,
		})
	}
	a.notesOnce.Do(func() {
		for _, n := range a.opt.ConfigNotes {
			log.Printf("config: %s", n)
		}
	})
	a.launchOnce.Do(a.announceLaunch)
	a.hkOnce.Do(a.registerHotkey)
	a.spaceOnce.Do(a.startSpaceWatch)
	a.trayOnce.Do(a.startTray)
	a.statsOnce.Do(a.startStats)
	a.iconsOnce.Do(a.startIcons)
	a.pluginOnce.Do(a.startPlugins)
	a.previewOnce.Do(a.startPreview)
	a.histOnce.Do(a.startHistory)
	a.frecOnce.Do(a.startFrecency)
	a.themeOnce.Do(a.startThemeWatch)
	// The progress printer exists before the build kick: the walk's
	// first tick can arrive immediately.
	a.progressOnce.Do(a.startProgress)
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
// loaded and can render. It applies the native panel configuration
// once, then executes at most one show that was
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
	// Spotlight-style collection behavior must be applied after the
	// window exists; DomReady is the earliest point every platform has
	// one (and it precedes the pending show, so the first mapping
	// already carries the behavior).
	a.panelOnce.Do(func() {
		if a.plat.configurePanel != nil {
			a.plat.configurePanel()
		}
	})
	if pending {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
}

// buildIndex runs the full disk walk -- forwarding progress to the
// progress printer (in place on a TTY, throttled log lines otherwise;
// see internal/progress) and to the frontend as eventIndexProgress --
// then brings the live-update layer up and logs the one
// startup-complete summary. Cancelling ctx (Shutdown) aborts the walk
// mid-flight: the partial store is discarded (BuildFromDisk only swaps
// on success), the watch layer never starts, and no summary is logged.
func (a *App) buildIndex(ctx context.Context) {
	// The same Once Startup runs before kicking the build; tests that
	// drive buildIndex directly get the printer here.
	a.progressOnce.Do(a.startProgress)
	a.mu.Lock()
	pr := a.progress
	a.mu.Unlock()
	start := a.plat.now()
	onProgress := func(indexed int, done bool) {
		if !done {
			pr.Indexing(int64(indexed))
		}
		a.emitEvent(eventIndexProgress, indexProgress{
			Indexed: indexed,
			Done:    done,
			Seconds: a.plat.now().Sub(start).Seconds(),
		})
	}
	// Bound the walk's GC headroom for exactly the build window: the
	// restore runs before the watch layer comes up, so steady-state
	// behavior is untouched (gcbound.go has the full rationale).
	restoreGC := boundBuildGC(a.plat.setGCPercent)
	count, dur, err := a.manager.BuildFromDisk(ctx, onProgress)
	restoreGC()
	// Clear the in-place line (a no-op off a TTY) BEFORE any completion
	// or error log line, so none of them can collide with a
	// still-rendered progress row.
	pr.Done()
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
	// The user-facing startup summary fires only once the watch layer
	// is established: the elapsed figure covers index build + watch
	// setup, the whole time-to-ready.
	log.Printf("index: startup complete: %d entries in %s, %s ram",
		count, a.plat.now().Sub(start).Round(time.Millisecond), progress.RAMString())
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
// flag keeps a racing startWatch from starting it afterwards. The
// very last step clears the TTY progress line and restores the
// standard logger to stderr (non-TTY printers never touched it).
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

	// Drain the frecency layer's short-lived goroutines (one state
	// load, in-flight open recordings, a cwd derivation) so an open
	// recorded moments before quit still hits the disk. Each is a
	// single bounded file operation or /proc walk -- no lock is held
	// here and none of them can block indefinitely.
	a.frecWG.Wait()

	// Restore the standard logger LAST: in TTY mode installProgressLog
	// pointed it at the printer, and keeping that interception through
	// the teardown above let every log line up to here interleave
	// cleanly with a still-rendered progress row. Done clears any
	// in-place line first, so nothing written after us collides with a
	// leftover row. Non-TTY printers never touched the logger and are
	// left alone (unit tests capture log output; Shutdown must not
	// clobber their buffers).
	a.mu.Lock()
	pr := a.progress
	a.mu.Unlock()
	if pr != nil && pr.TTY() {
		pr.Done()
		log.SetOutput(os.Stderr)
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
// -- and hides the bar on success. A successful open of an absolute
// path is recorded as a frecency signal (recordOpen filters the
// open_url values that share this method).
func (a *App) Open(path string) error {
	if err := a.openTarget(path); err != nil {
		return err
	}
	a.recordOpen(path)
	a.Hide()
	return nil
}

// Reveal shows path selected in the operating system's file manager
// (credentialed on linux, like Open) and hides the bar on success. A
// successful reveal counts as a frecency open too -- the user went
// for that exact file.
func (a *App) Reveal(path string) error {
	if err := a.revealTarget(path); err != nil {
		return err
	}
	a.recordOpen(path)
	a.Hide()
	return nil
}
