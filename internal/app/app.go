// Package app holds the application object that is bound to the Wails
// frontend. Every exported method on App is callable from JavaScript as
// window.go.app.App.<Method>.
package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

// The searchbar window's fixed size; main.go feeds these to Wails and
// the positioning math uses them.
const (
	WindowWidth  = 680
	WindowHeight = 460
)

// Names of the events the Go side emits to the frontend.
const (
	// eventIndexProgress reports index build progress; payload
	// indexProgress.
	eventIndexProgress = "index:progress"
	// eventWatchDegraded reports that live updates became incomplete;
	// payload watchDegraded.
	eventWatchDegraded = "watch:degraded"
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

// Result is a single search hit sent to the frontend. It is the index
// package's Result (json tags path/name/isDir live there); the alias
// keeps the bound method signature and the frontend contract stable.
type Result = index.Result

// Options configures an App.
type Options struct {
	// RescanEvery > 0 enables periodic full rescans at that interval
	// (wire config.RescanIntervalMinutes here); 0 disables them.
	RescanEvery time.Duration
	// Hotkey is the config hotkey string ("alt+space"); empty disables
	// the global hotkey.
	Hotkey string
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

	mu           sync.Mutex // guards ctx, visible, lastToggle, hotkeyStop, lastThemeErr
	ctx          context.Context
	visible      bool
	lastToggle   time.Time
	hotkeyStop   func()
	lastThemeErr string

	themeOnce    sync.Once
	watchMu      sync.Mutex
	watcher      *watch.Watcher
	rescanner    *watch.Rescanner
	themeW       *themeWatcher
	shuttingDown bool

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
	return &App{
		manager: m,
		opt:     opt,
		rt:      defaultRuntimeSeams(),
		plat:    defaultPlatformSeams(),
	}
}

// Startup is wired to the Wails OnStartup hook: it saves the runtime
// context, registers the global hotkey (best effort), starts theme
// hot reload (best effort, see theme.go), and kicks off the initial
// index build in the background, so the window is responsive
// immediately while the walk fills the index.
func (a *App) Startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	a.hkOnce.Do(a.registerHotkey)
	a.themeOnce.Do(a.startThemeWatch)
	if a.manager == nil {
		return
	}
	a.buildOnce.Do(func() {
		go a.buildIndex()
	})
}

// registerHotkey parses the configured hotkey and starts the OS
// listener with toggle as the callback. Any failure -- bad spec, no X
// server, combination taken -- is logged once and the app runs on
// without a global hotkey.
func (a *App) registerHotkey() {
	spec := strings.TrimSpace(a.opt.Hotkey)
	if spec == "" || a.plat.startHotkey == nil {
		return
	}
	hk, err := platform.ParseHotkey(spec)
	if err != nil {
		log.Printf("hotkey: %v (running without a global hotkey)", err)
		return
	}
	stop, err := a.plat.startHotkey(hk, a.toggle)
	if err != nil {
		log.Printf("hotkey: registering %s failed: %v (running without a global hotkey)", hk, err)
		return
	}
	log.Printf("hotkey: %s summons the searchbar", hk)
	a.mu.Lock()
	a.hotkeyStop = stop
	a.mu.Unlock()
}

// buildIndex runs the full disk walk -- forwarding progress to the log
// and to the frontend as eventIndexProgress -- then brings the
// live-update layer up.
func (a *App) buildIndex() {
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
	count, dur, err := a.manager.BuildFromDisk(context.Background(), progress)
	if err != nil {
		log.Printf("index: initial build failed: %v", err)
		return
	}
	log.Printf("index: %d entries in %s", count, dur.Round(time.Millisecond))
	a.startWatch()
}

// startWatch starts the fsnotify Watcher over the manager's roots --
// filtering events through the same Excluder semantics the walks use,
// reporting degradation to the frontend -- and the Rescanner for
// periodic and degradation-triggered rebuilds. It is skipped when
// Shutdown already ran.
func (a *App) startWatch() {
	ex, err := index.NewExcluder(a.manager.Excludes())
	if err != nil {
		// The initial build would have failed on the same patterns and
		// returned before reaching here; a nil Excluder (matches
		// nothing) still keeps this path safe.
		log.Printf("watch: bad exclude patterns: %v", err)
		ex = nil
	}
	w := watch.New(a.manager, a.manager.Roots(), ex, watch.Options{OnDegraded: a.emitDegraded})
	r := watch.NewRescanner(a.manager, w, watch.RescanOptions{Interval: a.opt.RescanEvery})

	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if a.shuttingDown {
		return
	}
	if err := w.Start(); err != nil {
		log.Printf("watch: live updates unavailable (rescans still work): %v", err)
	}
	if err := r.Start(); err != nil {
		log.Printf("watch: rescanner failed to start: %v", err)
	}
	a.watcher, a.rescanner = w, r
	log.Printf("watch: live index updates started (periodic rescan every %v; 0s means off)", a.opt.RescanEvery)
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

// Shutdown is wired to the Wails OnShutdown hook. It releases the
// global hotkey and stops the rescanner first (it may be mid-rescan and
// calls back into the watcher to resync watches), then the watcher,
// then the theme hot-reload watcher. Safe to call at any point, even
// before the watch layer came up; a still-running initial build then
// skips starting it.
func (a *App) Shutdown(_ context.Context) {
	a.mu.Lock()
	hkStop := a.hotkeyStop
	a.hotkeyStop = nil
	a.mu.Unlock()
	if hkStop != nil {
		hkStop()
	}

	a.watchMu.Lock()
	a.shuttingDown = true
	w, r, tw := a.watcher, a.rescanner, a.themeW
	a.watcher, a.rescanner, a.themeW = nil, nil, nil
	a.watchMu.Unlock()
	if r != nil {
		r.Stop()
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
// iterate without null checks.
func (a *App) Search(query string) []Result {
	q := strings.TrimSpace(query)
	if q == "" || a.manager == nil {
		return []Result{}
	}
	res := a.manager.Query(q, 0)
	if res == nil {
		return []Result{}
	}
	return res
}

// Open launches path with the operating system's default handler and
// hides the bar on success.
func (a *App) Open(path string) error {
	if err := a.plat.open(path); err != nil {
		return err
	}
	a.Hide()
	return nil
}

// Reveal shows path selected in the operating system's file manager
// and hides the bar on success.
func (a *App) Reveal(path string) error {
	if err := a.plat.reveal(path); err != nil {
		return err
	}
	a.Hide()
	return nil
}
