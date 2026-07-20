package app

import (
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/icons"
)

// iconResolver is the slice of *icons.Service the App consumes -- a
// seam (the newTray/newStats pattern) so unit tests can inject fakes
// or nothing at all.
type iconResolver interface {
	Resolve(keys []string, size int) map[string]string
}

// buildIcons is the production newIcons value: the icon resolution
// service over its own defaults (XDG dirs and gsettings on linux,
// .app bundle extraction on darwin, the embedded Material file-type
// pack everywhere -- see internal/icons) plus the live theme getter
// below. icons.NewService does no IO; the first Resolve pays the
// one-time initialization, so building it on the startup path is
// free.
func (a *App) buildIcons() iconResolver {
	return icons.NewService(icons.Options{Theme: iconTheme})
}

// iconTheme reads the configured theme name fresh per resolve batch
// (the translucent.go standalone-read pattern, consumed live like
// GetTheme's own re-read) so a config theme switch selects the
// Material pack's light icon variants without a relaunch. Any config
// error reads as "" -- the dark-default icon set.
func iconTheme() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return cfg.Theme
}

// startIcons builds the resolver once, at Startup. A nil seam result
// (newTestApp) leaves ResolveIcons answering empty maps.
func (a *App) startIcons() {
	svc := a.newIcons()
	a.iconsMu.Lock()
	a.icons = svc
	a.iconsMu.Unlock()
}

// ResolveIcons maps icon keys (the internal/icons key protocol:
// "app:<ref>", "dir", "file:<basename>") to data URIs at the wanted
// physical pixel size. Keys that miss are absent from the result; the
// map is never nil. The frontend batches the visible rows' keys per
// render and fills icons in as this answers -- rows keep their glyph
// meanwhile, so nothing on the query path ever waits on icon IO
// (resolution runs on this bound method's own goroutine, and repeat
// keys are served from the service's LRU).
func (a *App) ResolveIcons(keys []string, size int) map[string]string {
	a.iconsMu.Lock()
	svc := a.icons
	a.iconsMu.Unlock()
	if svc == nil || len(keys) == 0 {
		return map[string]string{}
	}
	return svc.Resolve(keys, size)
}
