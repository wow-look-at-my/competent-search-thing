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
	errs   []error
	closed int
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, query string, gen int64, appCtx *plugin.RequestContext, emit func(plugin.Emission)) plugin.TargetInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dispatchRecord{ctx: ctx, query: query, gen: gen, appCtx: appCtx, emit: emit})
	return f.target
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

// newPluginTestApp is newTestApp with a fake dispatcher installed
// through the builder seam and Startup already run.
func newPluginTestApp(t *testing.T) (*App, *seamRecorder, *fakeDispatcher) {
	t.Helper()
	a, r := newTestApp(nil, Options{})
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
	a, _ := newTestApp(nil, Options{})
	require.Equal(t, plugin.TargetInfo{}, a.QueryPlugins("x", 1), "safe before Startup")
	a.Startup(context.Background()) // the test builder seam yields a nil registry
	require.Equal(t, plugin.TargetInfo{}, a.QueryPlugins("x", 2), "safe with a nil registry")
}

func TestToggleCapturesFocusBeforeShow(t *testing.T) {
	a, r := newTestApp(nil, Options{})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.plat.appSource = &fakeSource{r: r, focusedOK: true}
	a.Startup(context.Background())

	a.toggle() // hidden -> capture app context, then show

	names := r.callNames()
	capture := indexOf(names, "captureFocused")
	show := indexOf(names, "show")
	require.GreaterOrEqual(t, capture, 0, "focused app was captured")
	require.GreaterOrEqual(t, show, 0, "bar was shown")
	require.Less(t, capture, show, "capture happens BEFORE the window shows (it steals focus)")
}

func TestCaptureBuildsPluginRequestContext(t *testing.T) {
	a, r := newTestApp(nil, Options{})
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
	a, _ := newTestApp(nil, Options{})
	a.captureAppContext() // nil cache: no-op, no panic
	rc := a.pluginRequestContext()
	require.Nil(t, rc.FocusedApp)
	require.Empty(t, rc.RunningApps)
	require.Empty(t, rc.InstalledApps)
	require.Nil(t, a.installedApps())
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
	a, _ := newTestApp(nil, Options{})
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
	require.Equal(t, []string{"open:/tmp/report.pdf", "hide"}, r.callNames())
}

func TestRunPluginActionOpenPathValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{"", "relative/path", "./x", "tmp"} {
		require.Error(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: bad}), "path %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid paths never reach the launcher")

	boom := errors.New("no handler")
	a.plat.open = func(string) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: "/tmp/x"}), boom)
	require.False(t, r.has("hide"), "a failed open does not hide the bar")
}

func TestRunPluginActionOpenURL(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("web", plugin.Action{Type: plugin.ActionOpenURL, Value: "https://example.com/x"}))
	require.Equal(t, []string{"open:https://example.com/x", "hide"}, r.callNames())
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
	a.plat.run = func([]string) error { return boom }
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

func TestRunBuiltinRescanWithoutWatcher(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.Error(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "rescan"}),
		"no rescanner yet: friendly error")
	require.False(t, r.has("hide"))
}

func TestRunBuiltinRescanRequestsAndHides(t *testing.T) {
	dir := t.TempDir()
	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(m, Options{})
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
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "config"}))
	require.Equal(t, []string{"open:" + filepath.Join(dir, "config.json"), "hide"}, r.callNames())
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

	before, _ := newTestApp(nil, Options{})
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
	a, _ := newTestApp(index.NewManager([]string{files}, nil, 0), Options{})
	a.newRegistry = a.buildRegistry // the real builder
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
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(nil, Options{})
	a.newRegistry = a.buildRegistry
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	require.NotContains(t, buf.String(), "plugin:", "no error noise for a missing plugins dir")

	ti := a.QueryPlugins("!version ", 1)
	require.Equal(t, plugin.TargetInfo{Targeted: true, Plugin: "app", Name: "App Commands", Bang: "version"}, ti,
		"builtins are still registered")
}

func TestBuildRegistryToleratesCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{corrupt"), 0o644))

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(nil, Options{})
	reg := a.buildRegistry()
	require.NotNil(t, reg, "a corrupt config still yields a registry (defaults)")
	require.Contains(t, buf.String(), "plugin: config:", "the parse error is logged")
	reg.Close()
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
