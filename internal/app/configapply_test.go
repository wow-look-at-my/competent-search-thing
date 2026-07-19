package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// seedBaseline installs cfg as the applied-config baseline, the state
// Startup's startConfigState normally leaves behind.
func seedBaseline(a *App, cfg config.Config) {
	a.cfgMu.Lock()
	a.cfgCurrent = &cfg
	a.cfgMu.Unlock()
}

func TestApplyConfigDiffsPerSection(t *testing.T) {
	mgr := index.NewManager(nil, nil, 50)
	a, _ := newTestApp(t, mgr, Options{})
	seedBaseline(a, config.Default())

	next := config.Default()
	next.MaxResults = 9
	res := a.applyConfig(&next, "test")
	require.Equal(t, []string{"maxResults"}, res.Applied, "only the changed section applies")
	require.Empty(t, res.Pending)
	require.Empty(t, res.Errors)
	require.Equal(t, 9, mgr.MaxResults())

	// The pass advanced the baseline: re-applying the same document
	// changes nothing.
	res = a.applyConfig(&next, "test")
	require.Empty(t, res.Applied)
	require.Empty(t, res.Pending)
}

func TestApplyConfigRegistryGroupRunsOnce(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	reloads := 0
	a.newRegistry = func() dispatcher { reloads++; return nil }
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Plugins.Disabled = true
	next.Bangs.Aliases = map[string]string{"cfg": "config"}
	next.Firefox.OpenTabs.MaxResults = 9
	next.Rewrites = []config.RewriteRule{{Name: "n", Pattern: "p", Replacement: "https://x.test"}}
	res := a.applyConfig(&next, "test")

	require.Equal(t, 1, reloads, "one shared registry reload covers every registry-backed section")
	require.Equal(t, []string{"plugins", "bangs", "firefox", "rewrites"}, res.Applied)
	require.Empty(t, res.Pending)
}

func TestApplyConfigFuzzyTouchesManagerAndRegistry(t *testing.T) {
	mgr := index.NewManager(nil, nil, 50)
	a, _ := newTestApp(t, mgr, Options{})
	reloads := 0
	a.newRegistry = func() dispatcher { reloads++; return nil }
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Search.FuzzyDisabled = true
	res := a.applyConfig(&next, "test")
	require.True(t, mgr.FuzzyDisabled(), "the index engine switch flips")
	require.Equal(t, 1, reloads, "the plugin engine re-reads the switch via the registry reload")
	require.Equal(t, []string{"search.fuzzyDisabled"}, res.Applied)
}

func TestApplyConfigTableIsTotal(t *testing.T) {
	// The Phase-B contract: every section has a live applier, so a pass
	// changing sections across the groups reports them all applied --
	// Pending stays empty, in table order, with no errors.
	a, _ := newTestApp(t, nil, Options{})
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Roots = []string{"/data"}
	next.Hotkey = "ctrl+space"
	next.Watcher.MaxWatches = 5
	next.Stats.Disabled = true
	res := a.applyConfig(&next, "test")
	require.Empty(t, res.Pending, "the applier table is total; nothing awaits a restart")
	require.Empty(t, res.Errors)
	require.Equal(t, []string{"roots", "hotkey", "watcher", "stats"}, res.Applied,
		"every changed section applies live, in table order")
}

func TestApplyConfigEverySectionHasAnApplier(t *testing.T) {
	// The table-shape guard behind the live-apply promise: no row may
	// carry neither an apply func nor a group (that row would land in
	// Pending, which Phase B emptied for good).
	for _, s := range sectionAppliers {
		require.True(t, s.apply != nil || s.group != "",
			"section %q has neither an applier nor a group", s.name)
		if s.group != "" {
			require.Contains(t, applyGroups, s.group,
				"section %q names an unregistered group", s.name)
		}
	}
}

func TestApplyConfigThemeCountsApplied(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Theme = "light"
	res := a.applyConfig(&next, "test")
	require.Equal(t, []string{"theme"}, res.Applied,
		"theme is live via GetTheme's fresh load + the watcher's theme:changed")
}

func TestApplyConfigNilBaselineAppliesEverything(t *testing.T) {
	mgr := index.NewManager(nil, nil, 50)
	a, _ := newTestApp(t, mgr, Options{})
	// No Startup: cfgCurrent is nil, so every section counts changed
	// (idempotent appliers make over-applying safe).
	cfg := config.Default()
	cfg.MaxResults = 11
	res := a.applyConfig(&cfg, "test")
	require.Contains(t, res.Applied, "maxResults")
	require.Contains(t, res.Applied, "theme")
	require.Contains(t, res.Applied, "watcher")
	require.Empty(t, res.Pending)
	require.Empty(t, res.NextLaunch,
		"a baseline-free pass cannot know translucent changed, so it never claims it")
	require.Equal(t, 11, mgr.MaxResults())
	require.Equal(t, config.Default().Roots, mgr.Roots(),
		"the index-layer applier stored the roots even pre-Startup")
}

func TestStartupSeedsBaselineFromDisk(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir) // after newTestApp, before Startup
	cfg := config.Default()
	cfg.MaxResults = 42
	data, err := config.Encode(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644))

	a.Startup(context.Background())
	a.cfgMu.Lock()
	cur := a.cfgCurrent
	a.cfgMu.Unlock()
	require.NotNil(t, cur, "Startup seeds the live-apply baseline")
	require.Equal(t, 42, cur.MaxResults)
}

// externalTestApp builds a started app with a real manager whose
// config dir is newTestApp's temp dir (read back through config.Dir),
// ready for config-dir watcher tests.
func externalTestApp(t *testing.T) (*App, *seamRecorder, *index.Manager, string) {
	t.Helper()
	mgr := index.NewManager(nil, nil, 50)
	a, r := newTestApp(t, mgr, Options{})
	dir, err := config.Dir()
	require.NoError(t, err)
	a.Startup(context.Background()) // seeds the baseline, starts the watcher
	return a, r, mgr, filepath.Join(dir, "config.json")
}

func TestExternalEditHotApplies(t *testing.T) {
	_, r, mgr, cfgPath := externalTestApp(t)

	cfg := config.Default()
	cfg.MaxResults = 9
	data, err := config.Encode(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, data, 0o644))

	require.Eventually(t, func() bool { return mgr.MaxResults() == 9 },
		5*time.Second, 10*time.Millisecond, "the hand edit hot-applies")
	require.Eventually(t, func() bool { return len(r.emitted(eventConfigChanged)) >= 1 },
		5*time.Second, 10*time.Millisecond)
	ev := r.emitted(eventConfigChanged)[0]
	require.Len(t, ev.payload, 1)
	payload, ok := ev.payload[0].(configChangedEvent)
	require.True(t, ok)
	require.Contains(t, payload.Applied, "maxResults")
	require.Empty(t, payload.Error)
	require.NotEmpty(t, r.emitted(eventThemeChanged), "the existing theme:changed flow still fires")
}

func TestExternalAtomicRenameHotApplies(t *testing.T) {
	// Editors and the app's own Save land config.json via rename; the
	// watcher must treat that exactly like an in-place write.
	_, r, mgr, cfgPath := externalTestApp(t)

	cfg := config.Default()
	cfg.MaxResults = 8
	data, err := config.Encode(cfg)
	require.NoError(t, err)
	tmp := filepath.Join(filepath.Dir(cfgPath), ".ext-edit.tmp")
	require.NoError(t, os.WriteFile(tmp, data, 0o644))
	require.NoError(t, os.Rename(tmp, cfgPath))

	require.Eventually(t, func() bool { return mgr.MaxResults() == 8 },
		5*time.Second, 10*time.Millisecond, "a rename onto config.json hot-applies")
	require.Eventually(t, func() bool { return len(r.emitted(eventThemeChanged)) >= 1 },
		5*time.Second, 10*time.Millisecond, "theme:changed fires for the rename too")
}

func TestExternalEditCorruptFileReportsError(t *testing.T) {
	_, r, mgr, cfgPath := externalTestApp(t)

	require.NoError(t, os.WriteFile(cfgPath, []byte("{corrupt"), 0o644))
	require.Eventually(t, func() bool {
		for _, e := range r.emitted(eventConfigChanged) {
			if len(e.payload) == 1 {
				if p, ok := e.payload[0].(configChangedEvent); ok && p.Error != "" {
					return true
				}
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the reload failure surfaces to the frontend")
	require.Equal(t, 50, mgr.MaxResults(), "the previous config stays applied")
}

func TestSaveConfigSelfWriteIsNotReApplied(t *testing.T) {
	a, r, mgr, _ := externalTestApp(t)

	res := a.SaveConfig(`{"roots": ["/"], "maxResults": 7}`)
	require.True(t, res.OK, "error: %s", res.Error)
	require.Equal(t, 7, mgr.MaxResults(), "the save itself applied")

	// The watcher sees the save land on disk (theme:changed fires),
	// recognizes the app's own bytes, and skips the external-edit
	// pass: no config:changed, no second apply.
	require.Eventually(t, func() bool { return len(r.emitted(eventThemeChanged)) >= 1 },
		5*time.Second, 10*time.Millisecond, "the watcher did see the write")
	time.Sleep(150 * time.Millisecond) // the handler runs right after that emit
	require.Empty(t, r.emitted(eventConfigChanged),
		"the app's own save is never re-applied as an external edit")
}
