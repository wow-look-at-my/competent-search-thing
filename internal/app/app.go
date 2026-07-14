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
)

// ErrNotImplemented marks bound methods whose real implementation
// arrives in a later phase.
var ErrNotImplemented = errors.New("not implemented yet")

// Result is a single search hit sent to the frontend. It is the index
// package's Result (json tags path/name/isDir live there); the alias
// keeps the bound method signature and the frontend contract stable.
type Result = index.Result

// App is the Wails-bound application object. It carries the Wails
// runtime context after Startup has run and owns the index manager.
type App struct {
	ctx       context.Context
	manager   *index.Manager
	buildOnce sync.Once
}

// New creates an App around an index manager (nil is tolerated: Search
// then returns no results and Startup skips the index build).
func New(m *index.Manager) *App {
	return &App{manager: m}
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

// buildIndex runs the full disk walk. Progress goes to the log for now;
// a later phase forwards it to the frontend as Wails events.
func (a *App) buildIndex() {
	count, dur, err := a.manager.BuildFromDisk(context.Background(), logProgress)
	if err != nil {
		log.Printf("index: initial build failed: %v", err)
		return
	}
	log.Printf("index: %d entries in %s", count, dur.Round(time.Millisecond))
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
