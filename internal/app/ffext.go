package app

import (
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/ffext"
	"github.com/wow-look-at-my/competent-search-thing/internal/firefox"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// ffextTabTTL is how fresh the bridge's live-tab snapshot must be for
// the open-tabs getter to prefer it over the sessionstore fallback --
// deliberately firefox.DefaultTabTTL's value (the two sources answer
// the same question at the same staleness bound).
const ffextTabTTL = 15 * time.Second

// ffextBridge is the slice of *ffext.Server the App consumes, split
// out so tests inject fakes (the stats seam pattern): the merged
// live-tab snapshot + freshness, the fire-and-forget refresh kick, the
// activation route, connection state, and teardown.
type ffextBridge interface {
	Tabs() ([]ffext.Tab, time.Time)
	KickRefresh()
	Activate(connID, tabID, windowID int64) error
	Connected() bool
	Close() error
}

// startFfext brings the companion-extension bridge up once, at
// Startup: the newFfext seam yields the bridge (nil = no Firefox
// profile, socket failure, or a test), stored for the app lifetime --
// registry reloads must never own or restart it, or a reload would
// sever the extension's connection.
func (a *App) startFfext() {
	if a.newFfext == nil {
		return
	}
	b := a.newFfext()
	if b == nil {
		return
	}
	a.ffextMu.Lock()
	a.ffextB = b
	a.ffextMu.Unlock()
}

// buildFfext is the production value behind the newFfext seam. It is
// gated on Firefox presence (the firefoxSources discovery gate's
// twin): no profile = one quiet log + no socket, no manifest. With a
// profile it (re)installs the native-messaging host pieces
// (self-healing; see installFfextHost) and brings the bridge socket
// up -- a listen failure degrades to nil with one log line, exactly
// like the Options.IPC nil path (the app must still work).
func (a *App) buildFfext() ffextBridge {
	if _, ok := firefox.FindProfile(a.plat.firefoxBases()); !ok {
		log.Printf("firefox: no profile found; the tab-switching bridge is disabled")
		return nil
	}
	a.installFfextHost()
	path := ffext.SocketPath(a.plat.getenv)
	srv, err := ffext.Listen(path, ffext.ServerOptions{Logf: log.Printf})
	if err != nil {
		log.Printf("ffext: %v (tab switching disabled; tab picks open the URL)", err)
		return nil
	}
	return srv
}

// installFfextHost writes (or self-heals) the Firefox native-messaging
// host pieces: the wrapper script in the config dir and the manifest
// in Firefox's per-OS location, both pointing at the STABLE spelling
// of the running binary (platform.StableExecutable -- the gsettings
// keybinding-command precedent: a resolved os.Executable dies with
// versioned symlinked installs on every upgrade). Best-effort: any
// failure logs and the app runs on (the extension simply cannot
// connect until the next successful install).
func (a *App) installFfextHost() {
	exe, err := a.plat.executable()
	if err != nil || exe == "" {
		log.Printf("ffext: cannot resolve the executable path (%v); native-messaging host not installed", err)
		return
	}
	exe = platform.StableExecutable(exe, a.plat.args0())
	home, err := a.plat.userHome()
	if err != nil {
		home = ""
	}
	dir, err := config.Dir()
	if err != nil {
		log.Printf("ffext: config dir: %v; native-messaging host not installed", err)
		return
	}
	res, err := ffext.InstallHost(a.plat.goos, home, dir, exe)
	if err != nil {
		log.Printf("ffext: install native-messaging host: %v", err)
		return
	}
	switch {
	case res.PreviousExe != "":
		// The gsettings repair-log precedent: ONE loud old->new line
		// when the stored command no longer named this binary.
		log.Printf("ffext: repaired the native-messaging host wrapper command: %s -> %s (%s)",
			res.PreviousExe, exe, res.WrapperPath)
	case res.WroteWrapper || res.WroteManifest:
		log.Printf("ffext: installed the Firefox native-messaging host (manifest %s, wrapper %s)",
			res.ManifestPath, res.WrapperPath)
	}
}

// ffextBridgeHandle returns the live bridge (nil before Startup, when
// disabled, or after Shutdown).
func (a *App) ffextBridgeHandle() ffextBridge {
	a.ffextMu.Lock()
	defer a.ffextMu.Unlock()
	return a.ffextB
}

// shutdownFfext closes the bridge socket (unlinking it) and detaches
// the handle; nil-safe and idempotent like every other teardown step.
func (a *App) shutdownFfext() {
	a.ffextMu.Lock()
	b := a.ffextB
	a.ffextB = nil
	a.ffextMu.Unlock()
	if b != nil {
		if err := b.Close(); err != nil {
			log.Printf("ffext: close: %v", err)
		}
	}
}

// kickFfextRefresh asks the bridge for a fresh live-tab list,
// fire-and-forget (the summon path calls it from captureAppContext, so
// the list is warm by the time the user finishes typing). Nil-safe.
func (a *App) kickFfextRefresh() {
	if b := a.ffextBridgeHandle(); b != nil {
		b.KickRefresh()
	}
}

// liveTabs returns the bridge's live-tab snapshot converted to the
// plugin wire type, with each row carrying its activation token.
// ok=false -- no bridge, no host connection, or a stale snapshot
// (older than ffextTabTTL) -- sends the caller to the sessionstore
// fallback. Rows are filtered to http(s)-with-host exactly like the
// sessionstore reader, so the two sources are interchangeable and
// every row's URL survives the activate_tab fallback's open_url
// validation. Each row's browser-reported favicon location feeds the
// icon resolver's hint side-channel (noteFavicon: bounded map writes,
// never IO), so the favicon resolution behind the rows' IconKeys has
// the live hint by the time the frontend asks.
func (a *App) liveTabs() ([]plugin.TabInfo, bool) {
	b := a.ffextBridgeHandle()
	if b == nil || !b.Connected() {
		return nil, false
	}
	tabs, at := b.Tabs()
	if at.IsZero() || a.plat.now().Sub(at) > ffextTabTTL {
		return nil, false
	}
	out := make([]plugin.TabInfo, 0, len(tabs))
	for _, tb := range tabs {
		host, hok := httpHost(tb.URL)
		if !hok {
			continue
		}
		a.noteFavicon(tb.URL, tb.FavIconURL)
		out = append(out, plugin.TabInfo{
			URL:          tb.URL,
			Title:        tb.Title,
			Host:         host,
			Pinned:       tb.Pinned,
			LastAccessed: tb.LastAccessed,
			Token:        ffext.Token(tb.Conn, tb.ID, tb.WindowID),
		})
	}
	if len(out) == 0 {
		return nil, true
	}
	return out, true
}

// httpHost returns the hostname of an http(s) URL; ok=false for
// anything else (about:, moz-extension:, view-source:, ...) -- the
// sessionstore reader's gate.
func httpHost(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.Hostname(), true
}

// activateTab routes one live-tab activation through the bridge. A
// missing or disconnected bridge answers ffext.ErrNotConnected after
// the ONE quiet heads-up log (the user picked a tab row but the
// companion extension is not connected -- every such pick falls back
// to opening the URL); any other failure (timeout, tab gone, refusal)
// logs per occurrence. The caller falls back to Open on any error.
func (a *App) activateTab(connID, tabID, windowID int64) error {
	b := a.ffextBridgeHandle()
	if b == nil || !b.Connected() {
		a.ffextInactiveOnce.Do(func() {
			log.Printf("firefox: tab switching inactive (companion extension not connected); tab picks open the URL")
		})
		return ffext.ErrNotConnected
	}
	if err := b.Activate(connID, tabID, windowID); err != nil {
		log.Printf("firefox: tab switch failed (%v); opening the URL instead", err)
		return err
	}
	return nil
}
