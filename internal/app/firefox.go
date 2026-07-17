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
// Firefox history refresh goroutine, created on first use and
// cancelled in Shutdown. Registry reloads build fresh caches (so
// config changes apply) but every cache shares THIS context, never a
// per-registry one -- a reload can therefore never leak an unbounded
// refresh, and quit aborts an in-flight history copy promptly.
func (a *App) firefoxContext() context.Context {
	a.pluginMu.Lock()
	defer a.pluginMu.Unlock()
	if a.firefoxCtx == nil {
		a.firefoxCtx, a.firefoxCancel = context.WithCancel(context.Background())
	}
	return a.firefoxCtx
}

// frequentSites assembles the frequent-sites source consumed by the
// plugin registry: the config profileDir override or profile
// discovery over the platform base dirs, then a snapshot cache bound
// to the firefox context. When no profile exists anywhere it returns
// nil -- the builtin provider is then never registered -- and the one
// quiet log line is the only trace. The returned getter is cheap and
// non-blocking: it reads the cache's current snapshot and converts;
// the cache refreshes itself in the background.
func (a *App) frequentSites(fs config.FrequentSitesConfig) func() []plugin.SiteInfo {
	dir := fs.ProfileDir
	if dir == "" {
		prof, ok := firefox.FindProfile(a.plat.firefoxBases())
		if !ok {
			log.Printf("firefox: no profile found; frequent-sites disabled")
			return nil
		}
		dir = prof.Dir
	}
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
