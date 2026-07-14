// Package app holds the application object that is bound to the Wails
// frontend. Every exported method on App is callable from JavaScript as
// window.go.app.App.<Method>.
package app

import (
	"context"
	"errors"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ErrNotImplemented marks bound methods whose real implementation
// arrives in a later phase.
var ErrNotImplemented = errors.New("not implemented yet")

// Result is a single search hit sent to the frontend.
type Result struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}

// App is the Wails-bound application object. It carries the Wails
// runtime context after Startup has run.
type App struct {
	ctx context.Context
}

// New creates an App. The runtime context is supplied later by Startup.
func New() *App {
	return &App{}
}

// Startup is wired to the Wails OnStartup hook and saves the runtime
// context for use by runtime calls such as Hide.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// Search returns index entries whose name contains query. The real
// index engine lands in a later phase; for now it always returns an
// empty, non-nil slice so the frontend contract is stable.
func (a *App) Search(query string) []Result {
	if strings.TrimSpace(query) == "" {
		return []Result{}
	}
	// TODO(later phase): query internal/index here.
	return []Result{}
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
