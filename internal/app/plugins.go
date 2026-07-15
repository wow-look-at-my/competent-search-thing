package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// Version is the application version. The plugin registry shows it in
// the builtin !version command, whose action copies it to the
// clipboard.
const Version = "0.1.0"

// eventPluginResults carries one plugin's results for one query
// generation; payload plugin.Emission (json tags
// plugin/name/gen/results).
const eventPluginResults = "plugin:results"

// installedAppsTTL is how stale the installed-apps snapshot may get
// before a summon kicks a background refresh.
const installedAppsTTL = 5 * time.Minute

// run_command re-validation limits, mirroring the response sanitizer's
// caps in internal/plugin.
const (
	maxActionArgvEntries    = 16
	maxActionArgvEntryBytes = 1024
)

// Builtin command names: the run_builtin action values produced by
// internal/plugin's app-commands provider.
const (
	builtinRescan  = "rescan"
	builtinReload  = "reload"
	builtinConfig  = "config"
	builtinVersion = "version"
	builtinQuit    = "quit"
)

// dispatcher is the slice of *plugin.Registry the App consumes, split
// out so tests can inject fakes.
type dispatcher interface {
	Dispatch(ctx context.Context, query string, gen int64, appCtx *plugin.RequestContext, emit func(plugin.Emission)) plugin.TargetInfo
	Errors() []error
	Close()
}

// startPlugins brings the plugin layer up once, at Startup: the
// app-context cache (over the platform Source seam) and the plugin
// registry. Building the registry is cheap file IO (config.json plus
// the manifest files), so it stays synchronous; a missing plugins
// directory simply yields a registry with only the builtin providers.
func (a *App) startPlugins() {
	cache := appctx.NewCache(a.plat.appSource)
	a.pluginMu.Lock()
	a.appCache = cache
	a.pluginMu.Unlock()
	cache.RefreshInstalledAsync()
	reg := a.newRegistry()
	a.pluginMu.Lock()
	a.registry = reg
	a.pluginMu.Unlock()
}

// buildRegistry loads config.json and the plugin manifests and
// assembles a fresh Registry. It never fails: config problems fall
// back to defaults, and everything the registry collected (manifest
// load errors, bad sigils, duplicate bangs/ids) is logged here, once,
// with a "plugin:" prefix. It is the production value behind the
// newRegistry seam.
func (a *App) buildRegistry() dispatcher {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("plugin: config: %v (continuing with defaults)", err)
	}
	var manifests []*plugin.Manifest
	var loadErrs []error
	dir, err := config.Dir()
	if err != nil {
		loadErrs = append(loadErrs, err)
	} else {
		manifests, loadErrs = plugin.LoadDir(filepath.Join(dir, "plugins"))
	}
	entries := make(map[string]plugin.Entry, len(cfg.Plugins.Entries))
	for id, e := range cfg.Plugins.Entries {
		entries[id] = plugin.Entry{Disabled: e.Disabled, Settings: e.Settings}
	}
	reg := plugin.New(plugin.Options{
		Manifests:     manifests,
		LoadErrors:    loadErrs,
		Sigils:        cfg.Bangs.Sigils,
		Aliases:       cfg.Bangs.Aliases,
		AllDisabled:   cfg.Plugins.Disabled,
		Entries:       entries,
		Version:       Version,
		InstalledApps: a.installedApps,
		Logf:          log.Printf,
	})
	for _, err := range reg.Errors() {
		log.Printf("plugin: %v", err)
	}
	return reg
}

// reloadRegistry rebuilds the plugin registry from disk (config.json
// and the manifests) and swaps it in. In-flight dispatches on the old
// registry die with their generation context; the old registry's
// pooled connections are released.
func (a *App) reloadRegistry() {
	reg := a.newRegistry()
	a.pluginMu.Lock()
	old := a.registry
	a.registry = reg
	a.pluginMu.Unlock()
	if old != nil {
		old.Close()
	}
	log.Printf("plugin: registry reloaded")
}

// appctxCache returns the app-context cache; nil before Startup, which
// is safe because every *appctx.Cache method tolerates a nil receiver.
func (a *App) appctxCache() *appctx.Cache {
	a.pluginMu.Lock()
	defer a.pluginMu.Unlock()
	return a.appCache
}

// captureAppContext snapshots the focused app and kicks the async
// running/installed refreshes. The toggle path runs it BEFORE showing
// the bar, because the bar window steals focus and the focused app
// must be the one the user was just using. Safe before Startup (nil
// cache no-ops).
func (a *App) captureAppContext() {
	c := a.appctxCache()
	c.CaptureFocused()
	c.RefreshRunningAsync()
	c.EnsureFreshInstalled(installedAppsTTL)
}

// installedApps adapts the cached installed-apps snapshot to the
// plugin wire type; it is the registry's InstalledApps getter (used by
// the builtin !app launcher).
func (a *App) installedApps() []plugin.InstalledApp {
	s := a.appctxCache().Snapshot()
	if len(s.Installed) == 0 {
		return nil
	}
	out := make([]plugin.InstalledApp, len(s.Installed))
	for i, ia := range s.Installed {
		out[i] = plugin.InstalledApp{Name: ia.Name, Exec: ia.Exec, ID: ia.ID}
	}
	return out
}

// pluginRequestContext converts the current app-context snapshot to
// the plugin request shape. The registry narrows it to the parts each
// manifest declared, so handing over everything here is fine.
func (a *App) pluginRequestContext() *plugin.RequestContext {
	s := a.appctxCache().Snapshot()
	rc := &plugin.RequestContext{}
	if s.Focused != nil {
		rc.FocusedApp = &plugin.AppInfo{
			Name:  s.Focused.Name,
			Exe:   s.Focused.Exe,
			Title: s.Focused.Title,
			PID:   s.Focused.PID,
		}
	}
	if len(s.Running) > 0 {
		rc.RunningApps = make([]plugin.AppInfo, len(s.Running))
		for i, r := range s.Running {
			rc.RunningApps[i] = plugin.AppInfo{Name: r.Name, Exe: r.Exe, Title: r.Title, PID: r.PID}
		}
	}
	if len(s.Installed) > 0 {
		rc.InstalledApps = a.installedApps()
	}
	return rc
}

// QueryPlugins fans query out to the plugin registry under generation
// gen (the frontend's per-keystroke sequence number) and returns the
// bang-target info for the query-row chip. Matching providers answer
// asynchronously via eventPluginResults events carrying gen, so stale
// answers are droppable on both sides. Every call cancels the previous
// generation's context -- aborting its subprocesses, HTTP requests,
// and debounce sleeps -- and an empty query just cancels.
func (a *App) QueryPlugins(query string, gen int) plugin.TargetInfo {
	next := int64(gen)
	a.pluginGen.Store(next)

	dispatch := strings.TrimSpace(query) != ""
	var genCtx context.Context
	a.pluginMu.Lock()
	if a.pluginCancel != nil {
		a.pluginCancel()
		a.pluginCancel = nil
	}
	reg := a.registry
	if dispatch && reg != nil {
		genCtx, a.pluginCancel = context.WithCancel(context.Background())
	}
	a.pluginMu.Unlock()
	if genCtx == nil {
		return plugin.TargetInfo{}
	}

	emit := func(em plugin.Emission) {
		if a.pluginGen.Load() != next {
			return // a newer query superseded this generation
		}
		a.emitEvent(eventPluginResults, em)
	}
	return reg.Dispatch(genCtx, query, next, a.pluginRequestContext(), emit)
}

// RunPluginAction executes a result's action on behalf of the
// frontend. Actions were already sanitized on their way in (or came
// from a trusted builtin provider), but everything is re-validated
// here as defense in depth -- the frontend merely echoes them back.
// Actions that hand off to another program (open_path, open_url,
// run_command, and most builtins) hide the bar on success; copy_text
// and the version builtin keep it open for the "Copied" feedback.
func (a *App) RunPluginAction(pluginID string, action plugin.Action) error {
	switch action.Type {
	case plugin.ActionCopyText:
		if action.Value == "" {
			return errors.New("copy_text: empty value")
		}
		a.logAction(pluginID, action.Type, "")
		return a.clipboardCopy(action.Value)
	case plugin.ActionOpenPath:
		if action.Value == "" || !filepath.IsAbs(action.Value) {
			return fmt.Errorf("open_path: %q is not an absolute path", action.Value)
		}
		a.logAction(pluginID, action.Type, action.Value)
		return a.Open(action.Value)
	case plugin.ActionOpenURL:
		if !validHTTPURL(action.Value) {
			return fmt.Errorf("open_url: %q is not an http(s) URL", action.Value)
		}
		a.logAction(pluginID, action.Type, action.Value)
		return a.Open(action.Value)
	case plugin.ActionRunCommand:
		if err := validateArgv(action.Argv); err != nil {
			return fmt.Errorf("run_command: %w", err)
		}
		a.logAction(pluginID, action.Type, strings.Join(action.Argv, " "))
		if err := a.plat.run(action.Argv); err != nil {
			return err
		}
		a.Hide()
		return nil
	case plugin.ActionRunBuiltin:
		a.logAction(pluginID, action.Type, action.Value)
		return a.runBuiltin(action.Value)
	default:
		return fmt.Errorf("unsupported plugin action type %q", action.Type)
	}
}

// runBuiltin executes one app-level builtin command (the actions
// behind the !rescan/!reload/!config/!version/!quit bangs).
func (a *App) runBuiltin(value string) error {
	switch value {
	case builtinRescan:
		a.watchMu.Lock()
		r := a.rescanner
		a.watchMu.Unlock()
		if r == nil {
			return errors.New("rescan: the index is still building; try again when it finishes")
		}
		r.Request()
		a.Hide()
		return nil
	case builtinReload:
		a.reloadRegistry()
		a.Hide()
		return nil
	case builtinConfig:
		p, err := config.Path()
		if err != nil {
			return err
		}
		return a.Open(p)
	case builtinVersion:
		return a.clipboardCopy(Version)
	case builtinQuit:
		ctx := a.runtimeCtx()
		if ctx == nil {
			return errors.New("quit: not started")
		}
		a.rt.quit(ctx)
		return nil
	default:
		return fmt.Errorf("unknown builtin command %q", value)
	}
}

// clipboardCopy puts text on the system clipboard via the Wails
// runtime, nil-context-guarded like every other runtime call.
func (a *App) clipboardCopy(text string) error {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return errors.New("clipboard unavailable before startup")
	}
	return a.rt.clipboardSetText(ctx, text)
}

// logAction records an accepted plugin action, post-validation.
// copy_text values are elided: clipboard content is not log material.
func (a *App) logAction(pluginID, typ, detail string) {
	if detail == "" {
		log.Printf("plugin action: %s: %s", pluginID, typ)
		return
	}
	log.Printf("plugin action: %s: %s %s", pluginID, typ, detail)
}

// validateArgv re-checks a run_command argv against the sanitizer's
// shape rules: 1..16 entries, each non-empty and at most 1024 bytes.
func validateArgv(argv []string) error {
	if len(argv) == 0 || len(argv) > maxActionArgvEntries {
		return fmt.Errorf("needs 1..%d argv entries, got %d", maxActionArgvEntries, len(argv))
	}
	for i, arg := range argv {
		if arg == "" {
			return fmt.Errorf("argv entry %d is empty", i)
		}
		if len(arg) > maxActionArgvEntryBytes {
			return fmt.Errorf("argv entry %d exceeds %d bytes", i, maxActionArgvEntryBytes)
		}
	}
	return nil
}

// validHTTPURL reports whether raw parses as an absolute http(s) URL
// with a host -- the same rule the plugin response sanitizer applies.
func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}
