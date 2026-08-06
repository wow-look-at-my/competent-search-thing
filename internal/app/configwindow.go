package app

// The SETTINGS WINDOW half of the App: the same bound object, started
// in a mode where it owns nothing but the config surface.
//
// The editor used to be a mode of the searchbar's own window -- a
// frameless always-on-top panel that hides on focus loss. That made it
// possible to lose the settings behind other windows (the auto-hide
// has to be suppressed while editing, so the panel just sits there
// unreachable from the taskbar). It is a separate PROCESS now, with an
// ordinary window: `competent-search-thing config` (internal/cli
// config.go) runs Options.ConfigWindow, and the searchbar's showConfig
// spawns exactly that.
//
// Nothing here talks to the searchbar instance. The window saves
// config.json; the running app's own config watcher hot-applies it.
// That is also why the settings window works with no app running at
// all.

import (
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// Startup modes reported to the frontend by GetStartupMode. The
// frontend wires ONE of two UIs from it: the searchbar, or the
// settings editor alone.
const (
	StartupModeSearch = "search"
	StartupModeConfig = "config"
)

// startConfigWindow is Startup's whole body in settings-window mode:
// the config-file baseline and watcher (so an external edit refreshes
// the open editor) plus the IPC handlers, whose Show/Config commands
// raise this window when a second `config` invocation arrives. No
// index, watcher, hotkey, tray, stats, plugins, service registration
// or preview dispatcher exists in this process.
func (a *App) startConfigWindow() {
	if a.opt.IPC != nil {
		a.opt.IPC.SetHandlers(ipc.Handlers{
			Toggle: a.raiseConfigWindow,
			Show:   a.raiseConfigWindow,
			Config: a.raiseConfigWindow,
			// Hide and Quit keep their meaning: a settings window can
			// be dismissed and replaced like any other instance.
			Hide: a.Hide,
			Quit: a.quitViaIPC,
		})
	}
	a.cfgOnce.Do(a.startConfigState)
	a.sidecarOnce.Do(a.startSchemaSidecar)
	a.themeOnce.Do(a.startThemeWatch)
}

// raiseConfigWindow brings this settings window to the front: what a
// second `competent-search-thing config` asks of the instance already
// holding the settings socket. Pre-DomReady it latches, like every
// other summon. Deliberately a PLAIN show -- no cursor-display
// positioning, no size clamp: this is an ordinary window the user (or
// their window manager) has placed, and a raise must not move it.
func (a *App) raiseConfigWindow() {
	a.mu.Lock()
	if !a.domReady {
		a.pendingShow = true
		a.mu.Unlock()
		return
	}
	a.visible = true
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		a.rt.show(ctx)
	}
}

// GetStartupMode tells the frontend which UI this process is: the
// searchbar ("search") or the settings window ("config"). It is the
// FIRST thing the frontend asks, before wiring anything.
func (a *App) GetStartupMode() string {
	if a.opt.ConfigWindow {
		return StartupModeConfig
	}
	return StartupModeSearch
}

// CloseConfigWindow quits the settings window -- the editor's Esc and
// Close button in settings-window mode (in the searchbar the same
// controls only left the editor mode). Refuses to run in the
// searchbar process: Esc there must never quit the app.
func (a *App) CloseConfigWindow() {
	if !a.opt.ConfigWindow {
		log.Printf("config: CloseConfigWindow ignored outside the settings window")
		return
	}
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.quit(ctx)
}
