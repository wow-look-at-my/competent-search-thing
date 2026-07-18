package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// dispatchRecord captures one Dispatch call's arguments.
type dispatchRecord struct {
	ctx    context.Context
	query  string
	gen    int64
	appCtx *plugin.RequestContext
	emit   func(plugin.Emission)
}

// fakeDispatcher stands in for *plugin.Registry: it records Dispatch
// calls and hands the emit closures back to the test so emissions can
// be replayed against any generation.
type fakeDispatcher struct {
	mu     sync.Mutex
	calls  []dispatchRecord
	target plugin.TargetInfo
	cheat  plugin.Emission
	errs   []error
	closed int
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, query string, gen int64, appCtx *plugin.RequestContext, emit func(plugin.Emission)) plugin.TargetInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dispatchRecord{ctx: ctx, query: query, gen: gen, appCtx: appCtx, emit: emit})
	return f.target
}

func (f *fakeDispatcher) CheatSheet() plugin.Emission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cheat
}

func (f *fakeDispatcher) Errors() []error { return f.errs }

func (f *fakeDispatcher) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *fakeDispatcher) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeDispatcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDispatcher) call(i int) dispatchRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

// fakeSource is a scripted appctx.Source that records its calls into
// the seamRecorder, so tests can assert ordering against runtime
// calls (capture-before-show).
type fakeSource struct {
	r           *seamRecorder
	focused     appctx.AppInfo
	focusedOK   bool
	running     []appctx.AppInfo
	runningOK   bool
	installed   []appctx.InstalledApp
	installedOK bool
	windows     []appctx.WindowInfo
	windowsOK   bool
}

func (s *fakeSource) FocusedApp() (appctx.AppInfo, bool) {
	s.r.call("captureFocused")
	return s.focused, s.focusedOK
}

func (s *fakeSource) RunningApps() ([]appctx.AppInfo, bool) {
	s.r.call("runningApps")
	return s.running, s.runningOK
}

func (s *fakeSource) InstalledApps() ([]appctx.InstalledApp, bool) {
	s.r.call("installedApps")
	return s.installed, s.installedOK
}

func (s *fakeSource) OpenWindows() ([]appctx.WindowInfo, bool) {
	s.r.call("openWindows")
	return s.windows, s.windowsOK
}

// newPluginTestApp is newTestApp with a fake dispatcher installed
// through the builder seam and Startup already run.
func newPluginTestApp(t *testing.T) (*App, *seamRecorder, *fakeDispatcher) {
	t.Helper()
	a, r := newTestApp(t, nil, Options{})
	f := &fakeDispatcher{}
	a.newRegistry = func() dispatcher { return f }
	a.Startup(context.Background())
	return a, r, f
}

// indexOf returns the position of name in names, or -1.
func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

func TestQueryPluginsDispatchesAndEmits(t *testing.T) {
	a, r, f := newPluginTestApp(t)
	f.target = plugin.TargetInfo{Targeted: true, Plugin: "calc", Name: "Calculator", Bang: "calc"}

	got := a.QueryPlugins("!calc 2+2", 7)
	require.Equal(t, f.target, got, "Dispatch's TargetInfo passes through")
	require.Equal(t, 1, f.callCount())
	c := f.call(0)
	require.Equal(t, "!calc 2+2", c.query)
	require.EqualValues(t, 7, c.gen)
	require.NotNil(t, c.appCtx)
	require.NoError(t, c.ctx.Err(), "generation context starts live")

	em := plugin.Emission{Plugin: "calc", Name: "Calculator", Gen: 7,
		Results: []plugin.Result{{Title: "4"}}}
	c.emit(em)
	events := r.emitted(eventPluginResults)
	require.Len(t, events, 1)
	require.Equal(t, em, events[0].payload[0], "emission is the event payload")
}

func TestQueryPluginsDropsStaleEmissions(t *testing.T) {
	a, r, f := newPluginTestApp(t)
	a.QueryPlugins("one", 1)
	first := f.call(0)

	a.QueryPlugins("two", 2)
	require.Error(t, first.ctx.Err(), "a new query cancels the previous generation")

	first.emit(plugin.Emission{Plugin: "p", Gen: 1})
	require.Empty(t, r.emitted(eventPluginResults), "stale generation never reaches the frontend")

	second := f.call(1)
	second.emit(plugin.Emission{Plugin: "p", Gen: 2})
	require.Len(t, r.emitted(eventPluginResults), 1, "current generation still emits")
}

func TestQueryPluginsEmptyQueryCancelsOnly(t *testing.T) {
	a, _, f := newPluginTestApp(t)
	a.QueryPlugins("query", 3)
	first := f.call(0)

	got := a.QueryPlugins("   ", 4)
	require.Equal(t, plugin.TargetInfo{}, got)
	require.Equal(t, 1, f.callCount(), "empty query never dispatches")
	select {
	case <-first.ctx.Done():
	default:
		t.Fatal("previous generation context still live after an empty query")
	}
}

func TestQueryPluginsWithoutRegistry(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.Equal(t, plugin.TargetInfo{}, a.QueryPlugins("x", 1), "safe before Startup")
	a.Startup(context.Background()) // the test builder seam yields a nil registry
	require.Equal(t, plugin.TargetInfo{}, a.QueryPlugins("x", 2), "safe with a nil registry")
}

func TestCheatSheetWithoutRegistry(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	e := a.CheatSheet() // safe before Startup
	require.NotNil(t, e.Results, "JS must see results: [], never null")
	require.Empty(t, e.Results)
	require.Empty(t, e.Plugin)

	a.Startup(context.Background()) // the test builder seam yields a nil registry
	e = a.CheatSheet()
	require.NotNil(t, e.Results)
	require.Empty(t, e.Results)
}

func TestCheatSheetFillsNilResults(t *testing.T) {
	a, _, f := newPluginTestApp(t)
	f.cheat = plugin.Emission{Plugin: "bangs", Name: "Commands"} // nil Results
	e := a.CheatSheet()
	require.Equal(t, "bangs", e.Plugin)
	require.Equal(t, "Commands", e.Name)
	require.NotNil(t, e.Results, "nil registry results are normalized for JSON")
	require.Empty(t, e.Results)
}

func TestCheatSheetFromRealRegistry(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.newRegistry = func() dispatcher {
		return plugin.New(plugin.Options{Version: Version, Logf: func(string, ...any) {}})
	}
	a.Startup(context.Background())

	e := a.CheatSheet()
	require.Equal(t, "bangs", e.Plugin)
	require.Equal(t, "Commands", e.Name)
	require.EqualValues(t, 0, e.Gen)
	var titles []string
	for _, res := range e.Results {
		titles = append(titles, res.Title)
	}
	require.Equal(t,
		[]string{"!app", "!config", "!launch", "!quit", "!reload", "!rescan", "!version"},
		titles, "the builtin bang list, sorted, titled with the primary sigil")
}

func TestToggleCapturesFocusBeforeShow(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.plat.appSource = &fakeSource{r: r, focusedOK: true}
	a.Startup(context.Background())
	a.DomReady(context.Background())

	a.toggle() // hidden -> capture app context, then show

	names := r.callNames()
	capture := indexOf(names, "captureFocused")
	show := indexOf(names, "show")
	require.GreaterOrEqual(t, capture, 0, "focused app was captured")
	require.GreaterOrEqual(t, show, 0, "bar was shown")
	require.Less(t, capture, show, "capture happens BEFORE the window shows (it steals focus)")
}

func TestCaptureBuildsPluginRequestContext(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.appSource = &fakeSource{
		r:           r,
		focused:     appctx.AppInfo{Name: "firefox", Exe: "/usr/lib/firefox/firefox", Title: "Mozilla Firefox", PID: 1234},
		focusedOK:   true,
		running:     []appctx.AppInfo{{Name: "code", Exe: "/usr/bin/code", Title: "editor", PID: 7}},
		runningOK:   true,
		installed:   []appctx.InstalledApp{{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"}},
		installedOK: true,
	}
	a.Startup(context.Background())
	a.captureAppContext()

	require.Eventually(t, func() bool {
		rc := a.pluginRequestContext()
		return rc.FocusedApp != nil && len(rc.RunningApps) == 1 && len(rc.InstalledApps) == 1
	}, 5*time.Second, 10*time.Millisecond, "async refreshes land in the snapshot")

	rc := a.pluginRequestContext()
	require.Equal(t, &plugin.AppInfo{Name: "firefox", Exe: "/usr/lib/firefox/firefox", Title: "Mozilla Firefox", PID: 1234}, rc.FocusedApp)
	require.Equal(t, []plugin.AppInfo{{Name: "code", Exe: "/usr/bin/code", Title: "editor", PID: 7}}, rc.RunningApps)
	require.Equal(t, []plugin.InstalledApp{{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"}}, rc.InstalledApps)
	require.Equal(t, rc.InstalledApps, a.installedApps(), "the registry getter sees the same conversion")
}

func TestPluginContextSafeBeforeStartup(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.captureAppContext() // nil cache: no-op, no panic
	rc := a.pluginRequestContext()
	require.Nil(t, rc.FocusedApp)
	require.Empty(t, rc.RunningApps)
	require.Empty(t, rc.InstalledApps)
	require.Nil(t, a.installedApps())
	require.Nil(t, a.openWindows())
}

func TestCaptureKicksWindowsRefreshAndConverts(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.appSource = &fakeSource{
		r:         r,
		windows:   []appctx.WindowInfo{{ID: 4294967295, Title: "Mozilla Firefox", App: "firefox", PID: 9}},
		windowsOK: true,
	}
	a.Startup(context.Background())
	a.captureAppContext()

	require.Eventually(t, func() bool {
		return len(a.openWindows()) == 1
	}, 5*time.Second, 10*time.Millisecond, "the summon-time capture kicks the windows refresh")
	require.Equal(t,
		[]plugin.WindowInfo{{ID: "4294967295", Title: "Mozilla Firefox", App: "firefox", PID: 9}},
		a.openWindows(),
		"the registry getter sees string ids across the full uint32 range")
	// (plugin.RequestContext has no windows field at all -- open
	// windows structurally cannot leak to external plugins.)
}

func TestRunPluginActionCopyText(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "42"}))
	require.True(t, r.has("clipboard:42"))
	require.False(t, r.has("hide"), "copy_text keeps the bar open")

	require.Error(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText}),
		"empty value is rejected")
}

func TestRunPluginActionCopyTextErrors(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.Error(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "x"}),
		"no clipboard before Startup")

	a2, _, _ := newPluginTestApp(t)
	boom := errors.New("no clipboard")
	a2.rt.clipboardSetText = func(context.Context, string) error { return boom }
	require.ErrorIs(t, a2.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "x"}), boom)
}

func TestRunPluginActionOpenPath(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: "/tmp/report.pdf"}))
	require.Equal(t, []string{"resolve:/tmp/report.pdf", "mint", "open:/tmp/report.pdf", "hide"}, r.callNames())
}

func TestRunPluginActionOpenPathValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{"", "relative/path", "./x", "tmp"} {
		require.Error(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: bad}), "path %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid paths never reach the launcher")

	boom := errors.New("no handler")
	a.plat.open = func(string, []string) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: "/tmp/x"}), boom)
	require.False(t, r.has("hide"), "a failed open does not hide the bar")
}

func TestRunPluginActionOpenURL(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("web", plugin.Action{Type: plugin.ActionOpenURL, Value: "https://example.com/x"}))
	require.Equal(t, []string{"resolve:https://example.com/x", "mint", "open:https://example.com/x", "hide"}, r.callNames())
}

func TestRunPluginActionOpenURLValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{
		"",
		"ftp://example.com",
		"javascript:alert(1)",
		"http://",     // no host
		"example.com", // no scheme
		"file:///etc/passwd",
	} {
		require.Error(t, a.RunPluginAction("web", plugin.Action{Type: plugin.ActionOpenURL, Value: bad}), "url %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid URLs never reach the launcher")
}

func TestRunPluginActionRunCommand(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: []string{"firefox", "--new-window"}}))
	require.Equal(t, []string{"run:firefox --new-window", "hide"}, r.callNames())
}

func TestRunPluginActionRunCommandValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	tooMany := make([]string, maxActionArgvEntries+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	for name, argv := range map[string][]string{
		"nil argv":        nil,
		"empty argv":      {},
		"17 entries":      tooMany,
		"empty entry":     {"a", ""},
		"oversized entry": {strings.Repeat("y", maxActionArgvEntryBytes+1)},
	} {
		require.Error(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: argv}), name)
	}
	require.Empty(t, r.callNames(), "invalid argv never reaches the launcher")

	boom := errors.New("spawn failed")
	a.plat.run = func([]string, []string) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: []string{"firefox"}}), boom)
	require.False(t, r.has("hide"), "a failed spawn does not hide the bar")
}

func TestRunPluginActionRejectsUnknownTypes(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, typ := range []string{"", "set_query", "explode"} {
		require.Error(t, a.RunPluginAction("p", plugin.Action{Type: typ, Value: "x"}), "type %q", typ)
	}
	require.Empty(t, r.callNames())
}

func TestRunPluginActionActivateWindow(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: "4294967295"}))
	require.Equal(t, []string{"activateWindow:4294967295", "hide"}, r.callNames(),
		"the seam gets the parsed id, then the bar hides")
}

func TestRunPluginActionActivateWindowValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{"", "abc", "-1", "1.5", "0x10", "4294967296", " 7"} {
		require.Error(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: bad}),
			"window id %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid window ids never reach the platform, and the bar stays")

	boom := errors.New("no X server")
	a.plat.activateWindow = func(uint32) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: "7"}), boom)
	require.False(t, r.has("hide"), "a failed activation does not hide the bar")
}

func TestRunBuiltinRescanWithoutWatcher(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.Error(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "rescan"}),
		"no rescanner yet: friendly error")
	require.False(t, r.has("hide"))
}

func TestRunBuiltinRescanRequestsAndHides(t *testing.T) {
	dir := t.TempDir()
	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{})
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond, "watch layer comes up after the initial build")

	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "rescan"}))
	require.True(t, r.has("hide"))
}

func TestRunBuiltinReloadSwapsRegistry(t *testing.T) {
	a, r, f := newPluginTestApp(t)
	f2 := &fakeDispatcher{}
	a.newRegistry = func() dispatcher { return f2 }

	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "reload"}))
	require.Equal(t, 1, f.closedCount(), "the old registry is closed")
	require.True(t, r.has("hide"))

	a.QueryPlugins("x", 9)
	require.Equal(t, 1, f2.callCount(), "dispatch reaches the new registry")
	require.Equal(t, 0, f.callCount(), "the old registry is out of the loop")
}

func TestRunBuiltinConfigOpensConfigJSON(t *testing.T) {
	// newTestApp (inside newPluginTestApp) points EnvConfigDir at its
	// own temp dir, so pin ours AFTER building the app.
	a, r, _ := newPluginTestApp(t)
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "config"}))
	cfgPath := filepath.Join(dir, "config.json")
	require.Equal(t, []string{"resolve:" + cfgPath, "mint", "open:" + cfgPath, "hide"}, r.callNames(),
		"the config file opens through the credentialed launch path too")
}

func TestRunBuiltinVersionCopiesWithoutHiding(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "version"}))
	require.True(t, r.has("clipboard:"+Version))
	require.False(t, r.has("hide"), "version keeps the bar open")
}

func TestRunBuiltinQuit(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "quit"}))
	require.Equal(t, []string{"quit"}, r.callNames())

	before, _ := newTestApp(t, nil, Options{})
	require.Error(t, before.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "quit"}),
		"quit needs the runtime context")
}

func TestRunBuiltinUnknown(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.Error(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "explode"}))
	require.Empty(t, r.callNames())
}

func TestStartupBuildsRegistryFromPluginsDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	good := filepath.Join(dir, "plugins", "calc")
	require.NoError(t, os.MkdirAll(good, 0o755))
	manifest := `{"v":1,"id":"calc","name":"Calculator","type":"command",` +
		`"bangs":["calc"],"command":{"argv":["/bin/true"]}}`
	require.NoError(t, os.WriteFile(filepath.Join(good, "manifest.json"), []byte(manifest), 0o644))
	bad := filepath.Join(dir, "plugins", "broken")
	require.NoError(t, os.MkdirAll(bad, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "manifest.json"), []byte("{not json"), 0o644))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	files := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(files, "shopping-list.txt"), []byte("x"), 0o644))
	a, _ := newTestApp(t, index.NewManager([]string{files}, nil, 0), Options{})
	t.Setenv(config.EnvConfigDir, dir) // newTestApp re-pointed it; restore ours before Startup
	a.newRegistry = a.buildRegistry    // the real builder
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	logged := buf.String()
	require.Contains(t, logged, "plugin:", "manifest problems are logged with the plugin prefix")
	require.Contains(t, logged, "broken", "the broken manifest is named")

	ti := a.QueryPlugins("!calc 2+2", 1)
	require.Equal(t, plugin.TargetInfo{Targeted: true, Plugin: "calc", Name: "Calculator", Bang: "calc"}, ti,
		"the valid manifest made it into the registry")

	require.Eventually(t, func() bool { return len(a.Search("shopping")) == 1 },
		20*time.Second, 10*time.Millisecond, "file search works alongside plugins")
}

func TestStartupMissingPluginsDirIsQuiet(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir()) // fresh dir, definitely no plugins/ inside
	a.newRegistry = a.buildRegistry
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	require.NotContains(t, buf.String(), "plugin:", "no error noise for a missing plugins dir")

	ti := a.QueryPlugins("!version ", 1)
	require.Equal(t, plugin.TargetInfo{Targeted: true, Plugin: "app", Name: "App Commands", Bang: "version"}, ti,
		"builtins are still registered")
}

func TestBuildRegistryToleratesCorruptConfig(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir) // after newTestApp, which re-points it
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{corrupt"), 0o644))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	reg := a.buildRegistry()
	require.NotNil(t, reg, "a corrupt config still yields a registry (defaults)")
	require.Contains(t, buf.String(), "plugin: config:", "the parse error is logged")
	reg.Close()
}

func TestOpenWindowsGetterSessionGating(t *testing.T) {
	t.Run("x11 enables without probing", func(t *testing.T) {
		a, r := newTestApp(t, nil, Options{})
		a.plat.detectSession = func() platform.Session { return platform.Session{Kind: platform.SessionX11} }
		a.plat.appSource = &fakeSource{r: r}
		require.NotNil(t, a.openWindowsGetter())
		require.False(t, r.has("openWindows"), "no probe on x11")
	})

	t.Run("wayland disables with one log line", func(t *testing.T) {
		var buf bytes.Buffer
		log.SetOutput(&buf)
		defer log.SetOutput(os.Stderr)

		a, r := newTestApp(t, nil, Options{})
		a.plat.detectSession = func() platform.Session { return platform.Session{Kind: platform.SessionWayland} }
		// The XWayland trap: an X probe WOULD succeed here, which is
		// exactly why the gate must be the session type, not the probe.
		a.plat.appSource = &fakeSource{r: r, windows: []appctx.WindowInfo{{ID: 1, Title: "w"}}, windowsOK: true}

		require.Nil(t, a.openWindowsGetter(), "wayland never enables")
		require.False(t, r.has("openWindows"), "the source is not even consulted")
		require.Contains(t, buf.String(), "open-window search unavailable on wayland")

		buf.Reset()
		require.Nil(t, a.openWindowsGetter(), "still disabled on a registry reload")
		require.NotContains(t, buf.String(), "wayland", "the log line fires exactly once")
	})

	t.Run("unknown session probes the source", func(t *testing.T) {
		a, _ := newTestApp(t, nil, Options{}) // detectSession pinned to unknown, appSource nil
		require.Nil(t, a.openWindowsGetter(), "nil source: nothing can list")

		a2, r2 := newTestApp(t, nil, Options{})
		a2.plat.appSource = &fakeSource{r: r2, windowsOK: false}
		require.Nil(t, a2.openWindowsGetter(), "source cannot list (headless, windows, darwin): disabled")
		require.True(t, r2.has("openWindows"), "the probe ran")

		a3, r3 := newTestApp(t, nil, Options{})
		a3.plat.appSource = &fakeSource{r: r3, windowsOK: true}
		require.NotNil(t, a3.openWindowsGetter(), "source can list (CI's Xvfb-like case): enabled")
	})
}

func TestOpenWindowsEndToEndOnX11(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.detectSession = func() platform.Session { return platform.Session{Kind: platform.SessionX11} }
	a.plat.appSource = &fakeSource{
		r:         r,
		windows:   []appctx.WindowInfo{{ID: 42, Title: "Mozilla Firefox", App: "firefox", PID: 9}},
		windowsOK: true,
	}
	a.newRegistry = a.buildRegistry // the real builder registers the provider on x11
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	a.captureAppContext() // the summon path: kicks the async windows refresh
	require.Eventually(t, func() bool { return len(a.openWindows()) == 1 },
		5*time.Second, 10*time.Millisecond, "the refreshed snapshot reaches the getter")

	a.QueryPlugins("firefox", 3)
	require.Eventually(t, func() bool {
		for _, e := range r.emitted(eventPluginResults) {
			em, ok := e.payload[0].(plugin.Emission)
			if ok && em.Plugin == "windows" && em.Gen == 3 && len(em.Results) == 1 {
				res := em.Results[0]
				return res.Title == "Mozilla Firefox" && res.Action != nil &&
					res.Action.Type == plugin.ActionActivateWindow && res.Action.Window == "42"
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the Open Windows section emits end to end on x11")
}

func TestShutdownClosesRegistryAndCancels(t *testing.T) {
	a, _, f := newPluginTestApp(t)
	a.QueryPlugins("query", 1)
	c := f.call(0)

	a.Shutdown(context.Background())
	require.Equal(t, 1, f.closedCount())
	select {
	case <-c.ctx.Done():
	default:
		t.Fatal("generation context survived Shutdown")
	}

	require.Equal(t, plugin.TargetInfo{}, a.QueryPlugins("query", 2))
	require.Equal(t, 1, f.callCount(), "no dispatch after Shutdown")
}
