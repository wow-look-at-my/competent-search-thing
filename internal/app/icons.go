package app

import (
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/firefox"
	"github.com/wow-look-at-my/competent-search-thing/internal/icons"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform/native"
)

// iconResolver is the slice of *icons.Service the App consumes -- a
// seam (the newTray/newStats pattern) so unit tests can inject fakes
// or nothing at all.
type iconResolver interface {
	Resolve(keys []string, size int) map[string]string
}

// faviconNoter is the OPTIONAL slice of *icons.Service behind the
// favicon hint side-channel (the plugin `prioritized` extension
// pattern): resolvers that accept browser-reported favicon URLs
// implement it, fakes that do not care simply do not.
type faviconNoter interface {
	NoteFavicon(pageURL, favURL string)
}

// buildIcons is the production newIcons value: the icon resolution
// service over its own defaults (XDG dirs and gsettings on linux,
// .app bundle extraction on darwin -- see internal/icons), with the
// OS's own icon rendering wired as the darwin fallback: NativeAppIcon
// = native.AppIconPNG (NSWorkspace iconForFile) serves every .app
// whose icon the pure plist/icns path cannot read (Assets.car-only
// apps), unconditionally -- the !darwin stub answers nil, so the one
// wiring compiles and no-ops everywhere else -- and, when a Firefox
// profile resolves, the website-favicon offline tier: FaviconLookup =
// a firefox.FaviconReader over that profile's favicons.sqlite,
// bounded by the app-lifetime firefox context (Shutdown aborts an
// in-flight snapshot copy and reaps the temp dir). No profile = no
// offline tier; the resolver's hint/fetch tiers still serve rows that
// carry browser hints, and without Firefox no favicon keys exist
// anyway. icons.NewService and firefox.NewFaviconReader do no IO; the
// first Resolve pays the one-time initialization, so building it on
// the startup path is free.
func (a *App) buildIcons() iconResolver {
	o := icons.Options{NativeAppIcon: native.AppIconPNG}
	if dir := a.faviconProfileDir(); dir != "" {
		rd := firefox.NewFaviconReader(a.firefoxContext(), firefox.FaviconOptions{
			ProfileDir: dir,
			Logf:       log.Printf,
		})
		o.FaviconLookup = rd.Lookup
	}
	return icons.NewService(o)
}

// faviconProfileDir resolves the profile whose favicons.sqlite backs
// the favicon lookups: a config profileDir override wins (open-tabs
// first -- the headline favicon consumer -- then frequent-sites, the
// firefoxSources order), else the shared platform discovery. "" = no
// profile anywhere (the quiet no-Firefox degrade; firefoxSources
// already logged the one discovery line). The fresh config.Load is
// the translucent.go standalone-read pattern.
func (a *App) faviconProfileDir() string {
	cfg, err := config.Load()
	if err == nil {
		if dir := cfg.Firefox.OpenTabs.ProfileDir; dir != "" {
			return dir
		}
		if dir := cfg.Firefox.FrequentSites.ProfileDir; dir != "" {
			return dir
		}
	}
	if prof, ok := firefox.FindProfile(a.plat.firefoxBases()); ok {
		return prof.Dir
	}
	return ""
}

// noteFavicon feeds one browser-reported favicon location into the
// icon resolver's hint side-channel, when the resolver has one
// (production always does; test fakes may not). Nil-safe and cheap --
// the noter validates and stores into a bounded map, never IO.
func (a *App) noteFavicon(pageURL, favURL string) {
	if favURL == "" {
		return
	}
	a.iconsMu.Lock()
	svc := a.icons
	a.iconsMu.Unlock()
	if n, ok := svc.(faviconNoter); ok {
		n.NoteFavicon(pageURL, favURL)
	}
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
