// Package app holds the application object that is bound to the Wails
// frontend. Every exported method on App is callable from JavaScript as
// window.go.app.App.<Method>.
package app

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

// ErrNotImplemented marks bound methods whose real implementation
// arrives in a later phase.
var ErrNotImplemented = errors.New("not implemented yet")

// Result is a single search hit sent to the frontend. It is the index
// package's Result (json tags path/name/isDir live there); the alias
// keeps the bound method signature and the frontend contract stable.
type Result = index.Result

// App is the Wails-bound application object. It carries the Wails
// runtime context after Startup has run and owns the index manager plus
// the live-update layer (watcher + rescanner) built on top of it.
type App struct {
	ctx         context.Context
	manager     *index.Manager
	buildOnce   sync.Once
	rescanEvery time.Duration

	watchMu      sync.Mutex
	watcher      *watch.Watcher
	rescanner    *watch.Rescanner
	shuttingDown bool
}

// New creates an App around an index manager (nil is tolerated: Search
// then returns no results and Startup skips the index build).
// rescanEvery > 0 enables periodic full rescans at that interval (wire
// config.RescanIntervalMinutes here); 0 disables them.
func New(m *index.Manager, rescanEvery time.Duration) *App {
	return &App{manager: m, rescanEvery: rescanEvery}
}

// Startup is wired to the Wails OnStartup hook: it saves the runtime
// context and kicks off the initial index build in the background, so
// the window is responsive immediately while the walk fills the index.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	if a.manager == nil {
		return
	}
	a.buildOnce.Do(func() {
		go a.buildIndex()
	})
}

// buildIndex runs the full disk walk, then brings the live-update layer
// up. Progress goes to the log for now; a later phase forwards it to
// the frontend as Wails events.
func (a *App) buildIndex() {
	count, dur, err := a.manager.BuildFromDisk(context.Background(), logProgress)
	if err != nil {
		log.Printf("index: initial build failed: %v", err)
		return
	}
	log.Printf("index: %d entries in %s", count, dur.Round(time.Millisecond))
	a.startWatch()
}

// startWatch starts the fsnotify Watcher over the manager's roots --
// filtering events through the same Excluder semantics the walks use --
// and the Rescanner for periodic and degradation-triggered rebuilds. It
// is skipped when Shutdown already ran.
func (a *App) startWatch() {
	ex, err := index.NewExcluder(a.manager.Excludes())
	if err != nil {
		// The initial build would have failed on the same patterns and
		// returned before reaching here; a nil Excluder (matches
		// nothing) still keeps this path safe.
		log.Printf("watch: bad exclude patterns: %v", err)
		ex = nil
	}
	w := watch.New(a.manager, a.manager.Roots(), ex, watch.Options{})
	r := watch.NewRescanner(a.manager, w, watch.RescanOptions{Interval: a.rescanEvery})

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
	log.Printf("watch: live index updates started (periodic rescan every %v; 0s means off)", a.rescanEvery)
}

// Shutdown is wired to the Wails OnShutdown hook. It stops the
// rescanner first (it may be mid-rescan and calls back into the watcher
// to resync watches), then the watcher. Safe to call at any point, even
// before the watch layer came up; a still-running initial build then
// skips starting it.
func (a *App) Shutdown(_ context.Context) {
	a.watchMu.Lock()
	a.shuttingDown = true
	w, r := a.watcher, a.rescanner
	a.watcher, a.rescanner = nil, nil
	a.watchMu.Unlock()
	if r != nil {
		r.Stop()
	}
	if w != nil {
		w.Stop()
	}
}

func logProgress(indexed int, done bool) {
	if !done {
		log.Printf("index: indexing... %d entries", indexed)
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

// Open launches path with the operating system's default handler.
// Stub: the platform layer lands in a later phase.
func (a *App) Open(path string) error {
	if path == "" {
		return errors.New("open: empty path")
	}
	return ErrNotImplemented
}

// Reveal shows path in the operating system's file manager.
// Stub: the platform layer lands in a later phase.
func (a *App) Reveal(path string) error {
	if path == "" {
		return errors.New("reveal: empty path")
	}
	return ErrNotImplemented
}

// Hide hides the searchbar window. It is a no-op before Startup has
// run: calling Wails runtime functions without the runtime context
// would abort the process.
func (a *App) Hide() {
	if a.ctx == nil {
		return
	}
	runtime.WindowHide(a.ctx)
}
