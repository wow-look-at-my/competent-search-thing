package app

import (
	"context"
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/tray"
)

// trayHandle is the slice of *tray.Tray the App consumes, split out so
// tests can inject recording fakes (the real Start dials the session
// bus).
type trayHandle interface {
	Start(ctx context.Context) error
	Close() error
}

// startTray brings the tray icon up once, at Startup: linux-only for
// now (the one user-facing request is GNOME; windows/darwin need
// different mechanisms entirely), honoring the config kill switch, and
// asynchronous -- tray.Start degrades quietly when the session has no
// bus or no StatusNotifierItem host, and nothing on the startup path
// waits for it.
func (a *App) startTray() {
	if a.plat.goos != "linux" {
		return
	}
	if a.opt.TrayDisabled {
		log.Printf("tray: disabled in config")
		return
	}
	if a.newTray == nil {
		return
	}
	h := a.newTray()
	if h == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.trayH = h
	a.trayCancel = cancel
	a.mu.Unlock()
	go func() {
		if err := h.Start(ctx); err != nil {
			log.Printf("tray: %v (running without a tray icon)", err)
		}
	}()
}

// buildTray is the production value behind the newTray seam.
func (a *App) buildTray() trayHandle { return tray.New(a.trayOptions()) }

// trayOptions describes the tray icon: identity, the summon-shortcut
// tooltip, and a menu that reuses the app's existing behaviors --
// Show/Hide is the hotkey toggle path (pre-DomReady deferral
// included), Rescan now and Open config are the !rescan/!config
// builtins minus their bar-hide (there is no bar interaction to end
// when the click came from the tray), Quit is the quit builtin.
// Callbacks arrive on the tray's D-Bus goroutines; every reused path
// is goroutine-safe the same way the hotkey and IPC callbacks already
// are.
func (a *App) trayOptions() tray.Options {
	return tray.Options{
		ID:    "competent-search-thing",
		Title: "Competent Search",
		Tooltip: func() string {
			d := a.hotkeyDescription()
			if d == "" {
				return ""
			}
			return d + " summons the searchbar"
		},
		Menu: []tray.MenuItem{
			{Label: "Show/Hide", OnClick: a.trayToggle},
			{Label: "Rescan now", OnClick: a.trayRescan},
			{Label: "Open config", OnClick: a.trayOpenConfig},
			{Separator: true},
			{Label: "Quit", OnClick: a.trayQuit},
		},
		OnActivate: a.trayToggle,
	}
}

// trayToggle handles the Show/Hide menu item and icon activation.
func (a *App) trayToggle() {
	log.Printf("tray: toggle")
	a.toggle()
}

// trayRescan handles Rescan now; the index-still-building case is a
// logged friendly error, exactly like the !rescan bang reports it.
func (a *App) trayRescan() {
	if err := a.requestRescan(); err != nil {
		log.Printf("tray: %v", err)
		return
	}
	log.Printf("tray: rescan requested")
}

// trayOpenConfig handles Open config.
func (a *App) trayOpenConfig() {
	if err := a.openConfigFile(); err != nil {
		log.Printf("tray: opening config.json: %v", err)
	}
}

// trayQuit handles Quit through the same builtin the !quit bang runs.
func (a *App) trayQuit() {
	if err := a.runBuiltin(builtinQuit); err != nil {
		log.Printf("tray: %v", err)
	}
}
