package app

import (
	"context"
	"log"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/firefox"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// firefoxContext returns the app-lifetime context bounding every
// Firefox data refresh goroutine (history and session snapshot),
// created on first use and cancelled in Shutdown. Registry reloads
// build fresh caches (so config changes apply) but every cache shares
// THIS context, never a per-registry one -- a reload can therefore
// never leak an unbounded refresh, and quit aborts an in-flight
// history copy promptly.
func (a *App) firefoxContext() context.Context {
	a.pluginMu.Lock()
	defer a.pluginMu.Unlock()
	if a.firefoxCtx == nil {
		a.firefoxCtx, a.firefoxCancel = context.WithCancel(context.Background())
	}
	return a.firefoxCtx
}

// firefoxSources assembles the Firefox-backed plugin sources --
// frequent sites and open tabs -- around ONE shared profile
// discovery: each section's config profileDir override wins for that
// section, and the override-less sections share a single FindProfile
// pass over the platform base dirs. When discovery comes up empty
// every override-less source is nil -- the builtin providers are then
// never registered -- and the one quiet log line is the only trace.
func (a *App) firefoxSources(cfg config.FirefoxConfig) (sites func() []plugin.SiteInfo, tabs func() []plugin.TabInfo) {
	discovered := ""
	failed := false
	resolve := func(override string) string {
		if override != "" {
			return override
		}
		if discovered == "" && !failed {
			prof, ok := firefox.FindProfile(a.plat.firefoxBases())
			if !ok {
				failed = true
				log.Printf("firefox: no profile found; the Firefox result sections are disabled")
				return ""
			}
			discovered = prof.Dir
		}
		return discovered
	}
	if dir := resolve(cfg.FrequentSites.ProfileDir); dir != "" {
		sites = a.frequentSites(cfg.FrequentSites, dir)
	}
	if dir := resolve(cfg.OpenTabs.ProfileDir); dir != "" {
		tabs = a.openTabs(dir)
	}
	return sites, tabs
}

// frequentSites builds the frequent-sites source over the resolved
// profile directory: a snapshot cache bound to the firefox context,
// adapted to the plugin wire type. The returned getter is cheap and
// non-blocking: it reads the cache's current snapshot and converts;
// the cache refreshes itself in the background.
func (a *App) frequentSites(fs config.FrequentSitesConfig, dir string) func() []plugin.SiteInfo {
	cache := firefox.NewCache(a.firefoxContext(), firefox.CacheOptions{
		ProfileDir: dir,
		MinMonth:   fs.MinVisitsMonth,
		MinWeek:    fs.MinVisitsWeek,
		TTL:        time.Duration(fs.RefreshMinutes) * time.Minute,
		Logf:       log.Printf,
	})
	return func() []plugin.SiteInfo {
		sites := cache.Sites()
		if len(sites) == 0 {
			return nil
		}
		out := make([]plugin.SiteInfo, len(sites))
		for i, s := range sites {
			out[i] = plugin.SiteInfo{URL: s.URL, Title: s.Title, Host: s.Host, Visits: s.Visits}
		}
		return out
	}
}

// openTabs builds the open-tabs source over the resolved profile
// directory. The getter PREFERS the companion-extension bridge's live
// snapshot -- rows then carry the activation token that switches to
// the exact tab -- whenever a host is connected and the snapshot is
// fresh (ffextTabTTL; see liveTabs in ffext.go), and otherwise falls
// back to the sessionstore cache exactly as before the bridge
// existed: a session-snapshot cache bound to the same firefox context
// (freshness is mtime- and TTL-gated inside the cache; there is no
// config cadence knob), adapted to the plugin wire type like
// frequentSites. The bridge handle is read lazily per call, so
// registry reloads keep working against the app-lifetime bridge.
func (a *App) openTabs(dir string) func() []plugin.TabInfo {
	cache := firefox.NewTabCache(a.firefoxContext(), firefox.TabCacheOptions{
		ProfileDir: dir,
		Logf:       log.Printf,
	})
	return func() []plugin.TabInfo {
		if live, ok := a.liveTabs(); ok {
			return live
		}
		tabs := cache.Tabs()
		if len(tabs) == 0 {
			return nil
		}
		out := make([]plugin.TabInfo, len(tabs))
		for i, tb := range tabs {
			// The snapshot's image attribute is the fallback rows'
			// favicon hint (liveTabs notes the bridge's favIconUrl the
			// same way; the noter validates either).
			a.noteFavicon(tb.URL, tb.FavIconURL)
			out[i] = plugin.TabInfo{
				URL:          tb.URL,
				Title:        tb.Title,
				Host:         tb.Host,
				Pinned:       tb.Pinned,
				LastAccessed: tb.LastAccessed,
			}
		}
		return out
	}
}
