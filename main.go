// Command competent-search-thing is a Spotlight-style desktop searchbar
// with voidtools-Everything-style instant filename search, built with Go
// and Wails v2.
//
// The Go side embeds the built frontend (frontend/dist) and hosts the
// Wails runtime; the bound application object lives in internal/app, and
// later phases add internal/index (walker + store + search),
// internal/watch (fsnotify), and internal/platform (hotkey, displays,
// open/reveal).
//
// NOTE: frontend/dist must exist before the Go build can succeed
// (cd frontend && npm install && npm run build), because it is embedded
// below with go:embed.
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/wow-look-at-my/competent-search-thing/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	a := app.New()

	err := wails.Run(&options.App{
		Title:             "competent-search-thing",
		Width:             680,
		Height:            460,
		Frameless:         true,
		AlwaysOnTop:       true,
		StartHidden:       true,
		HideWindowOnClose: true,
		DisableResize:     true,
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         a.Startup,
		Bind:              []interface{}{a},
	})
	if err != nil {
		log.Fatal(err)
	}
}
