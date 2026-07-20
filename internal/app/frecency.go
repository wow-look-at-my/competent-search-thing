package app

import (
	"log"
	"path/filepath"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// The frecency wiring: internal/frecency's signals built once at
// Startup and handed to the index Manager as an index.Blend (the
// ranking blend itself lives in internal/index blend.go).
//
//   - The open-count store persists at <configDir>/frecency.json and
//     learns through recordOpen, the ONE capture hook, called from
//     the success paths of Open and Reveal -- which also covers the
//     open_path plugin action, since it executes through Open. URLs
//     flowing through Open (open_url) are filtered out by the
//     absolute-path guard. Deleting the file resets the learning.
//   - The recency probe reuses the plat.lstat seam, so unit tests
//     never stat the real disk.
//   - The focused-app working directory is derived best-effort and
//     ASYNC at summon time (captureFrecencyCwd): focused PID ->
//     plat.procTree (a per-capture /proc snapshot on linux, nil
//     elsewhere) -> frecency.DeriveCwd -> a fresh Blend copy swapped
//     into the Manager. Nothing on the summon path blocks on it; the
//     boost simply applies from whenever the walk finishes. On X11
//     the focused PID comes from _NET_WM_PID and is usually right;
//     where no PID or no meaningful cwd surfaces, the boost is
//     cleared rather than left stale.
//
// config search.frecency.disabled leaves everything here nil: no
// store, no recording, no stats, and the Manager never gets a blend
// (the pre-blend ranking, byte-identical).
const frecencyFileName = "frecency.json"

// startFrecency builds the store, probe, and initial blend once at
// Startup (from the boot Options) and kicks the async state load.
func (a *App) startFrecency() {
	a.applyFrecencyConfig(a.opt.Frecency)
}

// applyFrecencyConfig (re)builds the frecency layer for one
// configuration: Startup feeds it the boot Options, the config
// live-apply path the just-saved section. Disabled clears everything
// -- store and blend dropped, the Manager back on the byte-identical
// pre-blend ranking -- while enabled builds a fresh store over the
// SAME frecency.json (the on-disk learning always survives a config
// change; the async Load re-reads it) and swaps a fresh immutable
// Blend into the Manager. Best-effort throughout: an unresolvable
// config dir degrades to a memory-only store, a corrupt state file
// logs ONE line and starts empty. Idempotent; a re-apply of the same
// values just rebuilds the same state.
func (a *App) applyFrecencyConfig(fc config.FrecencyConfig) {
	if fc.Disabled {
		// Preserve the pick-memory Prior across frecency rebuilds: the
		// priors layer (priors.go) installs its resolver on this SAME
		// blend, and wiping it here would silently kill an enabled
		// priors feature on any live frecency config change.
		a.frecMu.Lock()
		a.frecStore = nil
		a.frecBlend = index.Blend{Prior: a.frecBlend.Prior}
		b := a.frecBlend
		a.frecMu.Unlock()
		if a.manager != nil {
			if b.Prior != nil {
				a.manager.SetBlend(&b)
			} else {
				a.manager.SetBlend(nil)
			}
		}
		return
	}
	opts := frecency.Options{
		HalfLife: time.Duration(fc.HalfLifeDays * float64(24) * float64(time.Hour)),
		Persist:  true,
	}
	path := ""
	if dir, err := config.Dir(); err != nil {
		log.Printf("frecency: %v (keeping open counts in memory only)", err)
		opts.Persist = false
	} else {
		path = filepath.Join(dir, frecencyFileName)
	}
	store := frecency.New(path, opts)
	blend := index.Blend{
		Signals: frecency.Signals{
			Store:     store,
			Probe:     frecency.NewProbe(frecency.ProbeOptions{Lstat: a.plat.lstat}),
			CwdWeight: fc.WeightCwd,
		},
		WeightFrecency: fc.WeightFrecency,
		WeightRecency:  fc.WeightRecency,
		WeightNoise:    fc.WeightNoise,
		TierJump:       fc.TierJumpCount,
	}
	a.frecMu.Lock()
	a.frecStore = store
	blend.Prior = a.frecBlend.Prior // survive rebuilds (see the Disabled path)
	a.frecBlend = blend
	b := blend
	a.frecMu.Unlock()
	if a.manager != nil {
		a.manager.SetBlend(&b)
	}
	// Async so a slow disk never delays Startup. A recordOpen racing
	// the load can at worst lose that single open (Load replaces the
	// in-memory state) -- harmless, and the window is sub-millisecond.
	a.frecWG.Add(1)
	go func() {
		defer a.frecWG.Done()
		if err := store.Load(); err != nil {
			log.Printf("frecency: %v (starting with an empty open-count store)", err)
		}
	}()
}

// frecencyStore returns the open-count store; nil before Startup or
// when frecency is disabled (recordOpen then no-ops).
func (a *App) frecencyStore() *frecency.Store {
	a.frecMu.Lock()
	defer a.frecMu.Unlock()
	return a.frecStore
}

// recordOpen is the file-side frecency capture hook: it counts one
// open of an absolute path, asynchronously, never blocking the action
// that triggered it. Open serves open_url actions too, so
// non-absolute values (URLs) are filtered here rather than at the
// call sites.
func (a *App) recordOpen(path string) {
	if !filepath.IsAbs(path) {
		return
	}
	a.recordFrecencyKey(path)
}

// recordAppPick is the app-launch capture hook, called from the
// RunPluginAction run_command success path: it counts one launch of
// an installed app under its namespaced "app:" usage key
// (plugin.AppPickKey -- non-empty only for the two builtin app
// sources, so external plugins' run_commands record nothing). The
// keys live in the SAME frecency store as file opens but can never
// collide with the absolute paths the file-ranking blend looks up;
// they feed only the apps sections' within-tier usage tie-break
// (registry Options.AppUsage).
func (a *App) recordAppPick(pluginID string, action *plugin.Action) {
	key := plugin.AppPickKey(pluginID, action)
	if key == "" {
		return
	}
	a.recordFrecencyKey(key)
}

// recordFrecencyKey is the shared async store write behind recordOpen
// and recordAppPick: write errors log once and never fail the action
// (the in-memory count still updates, so in-session ranking keeps
// working); a nil store (frecency disabled, pre-Startup) no-ops.
func (a *App) recordFrecencyKey(key string) {
	st := a.frecencyStore()
	if st == nil {
		return
	}
	a.frecWG.Add(1)
	go func() {
		defer a.frecWG.Done()
		if err := st.RecordOpen(key); err != nil {
			a.frecErrOnce.Do(func() {
				log.Printf("frecency: recording an open: %v (in-session ranking still works; further write errors suppressed)", err)
			})
		}
	}()
}

// appUsage is the plugin registry's Options.AppUsage seam: the
// decayed launch count recorded under one app usage key. It reads
// through the LIVE store accessor on every call -- the config
// live-apply path (applyFrecencyConfig) rebuilds and swaps the store,
// and a registry built earlier must see the swap without a reload. A
// nil store (frecency disabled) reads 0 for every key: the cold
// pure-name app ordering.
func (a *App) appUsage(key string) float64 {
	return a.frecencyStore().Boost(key)
}

// captureFrecencyCwd derives the focused app's working directory for
// the cwd proximity boost, asynchronously -- the summon path never
// waits on a /proc walk. No process-tree source (non-linux), frecency
// disabled, or the boost weighted off all skip it entirely; no
// focused PID clears a stale boost instead of keeping it.
func (a *App) captureFrecencyCwd(c *appctx.Cache) {
	newTree := a.plat.procTree
	if newTree == nil {
		return
	}
	a.frecMu.Lock()
	enabled := a.frecStore != nil && a.frecBlend.Signals.CwdWeight > 0
	a.frecMu.Unlock()
	if !enabled {
		return
	}
	pid := 0
	if s := c.Snapshot(); s.Focused != nil {
		pid = s.Focused.PID
	}
	if pid <= 0 {
		a.setFrecencyCwd("")
		return
	}
	a.frecWG.Add(1)
	go func() {
		defer a.frecWG.Done()
		cwd, _ := frecency.DeriveCwd(newTree(), pid)
		a.setFrecencyCwd(cwd) // "" (no meaningful cwd) clears
	}()
}

// setFrecencyCwd stashes the derived working directory for the next
// query's Signals.Cwd by swapping a fresh Blend copy into the Manager
// (a handed-over Blend is immutable by contract). An unchanged value
// swaps nothing.
func (a *App) setFrecencyCwd(cwd string) {
	a.frecMu.Lock()
	if a.frecStore == nil || a.frecBlend.Signals.Cwd == cwd {
		a.frecMu.Unlock()
		return
	}
	a.frecBlend.Signals.Cwd = cwd
	b := a.frecBlend
	a.frecMu.Unlock()
	if a.manager != nil {
		a.manager.SetBlend(&b)
	}
}
