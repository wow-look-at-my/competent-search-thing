// Command competent-search-thing is a Spotlight-style desktop searchbar
// with voidtools-Everything-style instant filename search, built with Go
// and Wails v2.
//
// The Go side embeds the built frontend (frontend/dist) and hosts the
// Wails runtime; the bound application object lives in internal/app and
// owns the index engine (internal/index), the live-update layer
// (internal/watch: fsnotify watcher + periodic rescanner), and the
// platform layer (internal/platform: global hotkey, cursor-display
// positioning, open/reveal). Argument handling lives in internal/cli: a
// bare invocation boots the GUI as a single instance (internal/ipc unix
// socket), and the toggle/show/hide subcommands drive an already
// running instance -- the summon path for desktops without grabbable
// global hotkeys (Wayland).
//
// NOTE: frontend/dist must exist before the Go build can succeed
// (cd frontend && npm install && npm run build), because it is embedded
// below with go:embed.
package main

import (
	"embed"
	"log"
	"os"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"github.com/wow-look-at-my/competent-search-thing/internal/app"
	"github.com/wow-look-at-my/competent-search-thing/internal/cli"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	os.Exit(cli.Execute(app.Version, runGUI))
}

// runGUI builds the application object and blocks in the Wails event
// loop until the app quits. The CLI layer hands over the
// single-instance IPC server (nil in degraded runs) and whether the
// bar should show once the frontend is ready; the App owns both from
// here (Startup wires the IPC handlers, Shutdown closes the server).
func runGUI(opts cli.RunOptions) error {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v (continuing with the returned config)", err)
	}
	mgr := index.NewManager(cfg.Roots, cfg.Excludes, cfg.MaxResults)
	mgr.SetFuzzyDisabled(cfg.Search.FuzzyDisabled)
	// The preview pane (preview.enabled) widens the window; with the
	// flag off this stays exactly WindowWidth x WindowHeight.
	winW, winH, _ := app.PreviewWindowSize()
	a := app.New(mgr, app.Options{
		RescanEvery:            time.Duration(cfg.RescanIntervalMinutes) * time.Minute,
		Hotkey:                 cfg.Hotkey,
		IPC:                    opts.Server,
		ShowOnStartup:          opts.ShowOnStartup,
		TrayDisabled:           cfg.Tray.Disabled,
		HistoryPersistDisabled: cfg.History.PersistDisabled,
		ConfigNotes:            cfg.MigrationNotes,
		Preview:                cfg.Preview,
		WindowW:                winW,
		WindowH:                winH,
	})

	wailsOpts := &options.App{
		Title:             "competent-search-thing",
		Width:             winW,
		Height:            winH,
		Frameless:         true,
		AlwaysOnTop:       true,
		StartHidden:       true,
		HideWindowOnClose: true,
		DisableResize:     true,
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         a.Startup,
		OnDomReady:        a.DomReady,
		OnShutdown:        a.Shutdown,
		Bind:              []interface{}{a},
	}
	// window.translucent (config.json) requests a per-pixel-alpha
	// window so the rounded bar corners are truly see-through where a
	// compositor runs (README "Translucent window"). With the flag off
	// BackgroundColour and Linux stay nil -- the exact pre-flag
	// wails.Run call.
	if app.WindowTranslucent() {
		// The zero-value RGBA (alpha 0) makes the GTK #webview-box
		// background fully transparent; WindowIsTranslucent requests
		// the RGBA visual and forces the webview background alpha to
		// 0 (both in wails v2.13 linux/window.c).
		wailsOpts.BackgroundColour = &options.RGBA{}
		wailsOpts.Linux = &linux.Options{
			WindowIsTranslucent: true,
			// Pin the nil-Linux default GPU policy: wails' #2977
			// workaround (Never) lives only in the nil branch, so a
			// non-nil Linux block would silently flip it to OnDemand.
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		}
	}
	return wails.Run(wailsOpts)
}
