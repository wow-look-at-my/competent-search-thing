// Command competent-search-thing is a Spotlight-style desktop searchbar
// with voidtools-Everything-style instant filename search, built with Go
// and Wails v2.
//
// The Go side embeds the built frontend (frontend/dist) and hosts the
// Wails runtime; the bound application object lives in internal/app and
// owns the index engine (internal/index), the live-update layer
// (internal/watch: fsnotify watcher + periodic rescanner), and the
// platform layer (internal/platform: global hotkey, cursor-display
// positioning, open/reveal).
//
// NOTE: frontend/dist must exist before the Go build can succeed
// (cd frontend && npm install && npm run build), because it is embedded
// below with go:embed.
package main

import (
	"embed"
	"log"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/wow-look-at-my/competent-search-thing/internal/app"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v (continuing with defaults)", err)
	}
	a := app.New(index.NewManager(cfg.Roots, cfg.Excludes, cfg.MaxResults), app.Options{
		RescanEvery: time.Duration(cfg.RescanIntervalMinutes) * time.Minute,
		Hotkey:      cfg.Hotkey,
	})

	err = wails.Run(&options.App{
		Title:             "competent-search-thing",
		Width:             app.WindowWidth,
		Height:            app.WindowHeight,
		Frameless:         true,
		AlwaysOnTop:       true,
		StartHidden:       true,
		HideWindowOnClose: true,
		DisableResize:     true,
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         a.Startup,
		OnShutdown:        a.Shutdown,
		Bind:              []interface{}{a},
	})
	if err != nil {
		log.Fatal(err)
	}
}
