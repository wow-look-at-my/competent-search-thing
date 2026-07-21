package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/ffext"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// fakeBridge is a scripted ffextBridge.
type fakeBridge struct {
	mu        sync.Mutex
	tabs      []ffext.Tab
	at        time.Time
	connected bool
	actErr    error
	activated [][3]int64
	kicks     int
	closed    int
}

func (f *fakeBridge) Tabs() ([]ffext.Tab, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tabs, f.at
}

func (f *fakeBridge) KickRefresh() {
	f.mu.Lock()
	f.kicks++
	f.mu.Unlock()
}

func (f *fakeBridge) Activate(connID, tabID, windowID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, [3]int64{connID, tabID, windowID})
	return f.actErr
}

func (f *fakeBridge) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeBridge) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

func (f *fakeBridge) kickCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kicks
}

// installBridge plants a bridge (usually a fake) on a
// newTestApp-built App.
func installBridge(a *App, b ffextBridge) {
	a.ffextMu.Lock()
	a.ffextB = b
	a.ffextMu.Unlock()
}

func TestStartupBuildsBridgeOnceAndShutdownCloses(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	f := &fakeBridge{}
	builds := 0
	a.newFfext = func() ffextBridge {
		builds++
		return f
	}
	a.Startup(context.Background())
	a.Startup(context.Background())
	require.Equal(t, 1, builds, "the bridge is built exactly once")
	require.NotNil(t, a.ffextBridgeHandle())

	a.Shutdown(context.Background())
	require.Nil(t, a.ffextBridgeHandle())
	require.Equal(t, 1, f.closed)
	// Shutdown is idempotent; the bridge is closed once.
	a.Shutdown(context.Background())
	require.Equal(t, 1, f.closed)
}

func TestStartFfextNilSeamAndNilBuild(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.newFfext = nil
	a.startFfext()
	require.Nil(t, a.ffextBridgeHandle())

	a.newFfext = func() ffextBridge { return nil }
	a.startFfext()
	require.Nil(t, a.ffextBridgeHandle())
	a.shutdownFfext() // nil-safe
}

func TestCaptureAppContextKicksBridgeRefresh(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	f := &fakeBridge{}
	installBridge(a, f)
	a.captureAppContext()
	require.Equal(t, 1, f.kickCount())

	// Nil bridge: the kick is a safe no-op.
	a.shutdownFfext()
	a.captureAppContext()
	require.Equal(t, 1, f.kickCount())
}

func TestLiveTabsGates(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a.plat.now = func() time.Time { return now }

	// No bridge at all.
	_, ok := a.liveTabs()
	require.False(t, ok)

	// Bridge present but no host connected.
	f := &fakeBridge{}
	installBridge(a, f)
	_, ok = a.liveTabs()
	require.False(t, ok)

	// Connected but nothing ever reported (zero time).
	f.connected = true
	_, ok = a.liveTabs()
	require.False(t, ok)

	// Connected but stale.
	f.at = now.Add(-ffextTabTTL - time.Second)
	_, ok = a.liveTabs()
	require.False(t, ok, "a stale snapshot falls back to the sessionstore")

	// Fresh: rows convert, carry tokens, and non-http(s) rows drop.
	f.at = now.Add(-time.Second)
	f.tabs = []ffext.Tab{
		{Conn: 1, ID: 42, WindowID: 3, Title: "Docs", URL: "https://www.example.com/docs", Pinned: true, LastAccessed: 5},
		{Conn: 1, ID: 43, WindowID: 3, Title: "Prefs", URL: "about:preferences"},
		{Conn: 2, ID: 7, WindowID: 1, Title: "Other", URL: "http://other.example/"},
	}
	tabs, ok := a.liveTabs()
	require.True(t, ok)
	require.Len(t, tabs, 2, "the about: row is filtered like the sessionstore reader")
	require.Equal(t, plugin.TabInfo{
		URL:          "https://www.example.com/docs",
		Title:        "Docs",
		Host:         "www.example.com",
		Pinned:       true,
		LastAccessed: 5,
		Token:        "c1:42:3",
	}, tabs[0])
	require.Equal(t, "c2:7:1", tabs[1].Token)

	// Fresh but empty still wins (the browser truly has no tabs).
	f.tabs = nil
	tabs, ok = a.liveTabs()
	require.True(t, ok)
	require.Empty(t, tabs)
}

// fakeNotingResolver is a fakeIconResolver that also records favicon
// hints (the production *icons.Service shape).
type fakeNotingResolver struct {
	fakeIconResolver
	notes [][2]string
}

func (f *fakeNotingResolver) NoteFavicon(pageURL, favURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, [2]string{pageURL, favURL})
}

func (f *fakeNotingResolver) noted() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.notes...)
}

func TestLiveTabsNotesFavicons(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a.plat.now = func() time.Time { return now }
	noter := &fakeNotingResolver{}
	a.newIcons = func() iconResolver { return noter }
	a.startIcons()

	f := &fakeBridge{connected: true, at: now}
	f.tabs = []ffext.Tab{
		{Conn: 1, ID: 1, WindowID: 1, Title: "A", URL: "https://a.example/", FavIconURL: "https://a.example/favicon.ico"},
		{Conn: 1, ID: 2, WindowID: 1, Title: "B", URL: "https://b.example/"}, // no favicon reported
		{Conn: 1, ID: 3, WindowID: 1, Title: "C", URL: "about:blank", FavIconURL: "https://c.example/f.ico"},
	}
	installBridge(a, f)

	tabs, ok := a.liveTabs()
	require.True(t, ok)
	require.Len(t, tabs, 2)
	require.Equal(t, [][2]string{{"https://a.example/", "https://a.example/favicon.ico"}}, noter.noted(),
		"every http(s) row with a reported favicon is noted; empty and filtered rows are not")

	// A plain resolver without the noter surface degrades quietly.
	a.newIcons = func() iconResolver { return &fakeIconResolver{} }
	a.startIcons()
	_, ok = a.liveTabs()
	require.True(t, ok)
}

func TestOpenTabsGetterPrefersLiveAndFallsBack(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a.plat.now = func() time.Time { return now }
	getter := a.openTabs(t.TempDir()) // no sessionstore file: fallback yields nil

	f := &fakeBridge{connected: true, at: now}
	f.tabs = []ffext.Tab{{Conn: 1, ID: 1, WindowID: 1, Title: "Live", URL: "https://live.example/"}}
	installBridge(a, f)
	tabs := getter()
	require.Len(t, tabs, 1)
	require.Equal(t, "Live", tabs[0].Title)
	require.NotEmpty(t, tabs[0].Token)

	// Bridge gone: the sessionstore fallback (empty here) serves.
	a.shutdownFfext()
	require.Empty(t, getter())
}

func TestRunPluginActionActivateTab(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	f := &fakeBridge{connected: true}
	installBridge(a, f)
	require.NoError(t, a.RunPluginAction("firefox-tabs", plugin.Action{
		Type: plugin.ActionActivateTab, Tab: "c1:42:3", Value: "https://example.com/x",
	}))
	require.Equal(t, [][3]int64{{1, 42, 3}}, f.activated)
	require.Equal(t, []string{"hide"}, r.callNames(), "a successful switch only hides -- no launcher involved")
}

func TestRunPluginActionActivateTabValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	f := &fakeBridge{connected: true}
	installBridge(a, f)
	for _, bad := range []plugin.Action{
		{Type: plugin.ActionActivateTab, Tab: "", Value: "https://example.com/"},
		{Type: plugin.ActionActivateTab, Tab: "1:2:3", Value: "https://example.com/"},
		{Type: plugin.ActionActivateTab, Tab: "c1:2", Value: "https://example.com/"},
		{Type: plugin.ActionActivateTab, Tab: "cx:2:3", Value: "https://example.com/"},
		{Type: plugin.ActionActivateTab, Tab: "c1:2:3", Value: ""},
		{Type: plugin.ActionActivateTab, Tab: "c1:2:3", Value: "ftp://example.com/"},
		{Type: plugin.ActionActivateTab, Tab: "c1:2:3", Value: "about:blank"},
	} {
		require.Error(t, a.RunPluginAction("firefox-tabs", bad), "action %+v", bad)
	}
	require.Empty(t, f.activated, "invalid actions never reach the bridge")
	require.Empty(t, r.callNames(), "and never launch or hide")
}

func TestActivateTabFallsBackToOpenURL(t *testing.T) {
	act := plugin.Action{Type: plugin.ActionActivateTab, Tab: "c1:42:3", Value: "https://example.com/x"}
	fallbackCalls := []string{"resolve:https://example.com/x", "mint", "open:https://example.com/x", "hide"}

	t.Run("bridge activate fails", func(t *testing.T) {
		a, r, _ := newPluginTestApp(t)
		f := &fakeBridge{connected: true, actErr: errors.New("Invalid tab ID: 42")}
		installBridge(a, f)
		require.NoError(t, a.RunPluginAction("firefox-tabs", act),
			"the pick must not surface an error when the fallback works")
		require.Len(t, f.activated, 1)
		require.Equal(t, fallbackCalls, r.callNames())
	})

	t.Run("no host connected", func(t *testing.T) {
		a, r, _ := newPluginTestApp(t)
		installBridge(a, &fakeBridge{connected: false})
		require.NoError(t, a.RunPluginAction("firefox-tabs", act))
		require.Equal(t, fallbackCalls, r.callNames())
	})

	t.Run("no bridge at all", func(t *testing.T) {
		a, r, _ := newPluginTestApp(t)
		require.NoError(t, a.RunPluginAction("firefox-tabs", act))
		require.Equal(t, fallbackCalls, r.callNames())
	})

	t.Run("fallback failure surfaces", func(t *testing.T) {
		a, r, _ := newPluginTestApp(t)
		boom := errors.New("no handler")
		a.plat.open = func(string, []string) error { return boom }
		require.Error(t, a.RunPluginAction("firefox-tabs", act))
		require.False(t, r.has("hide"), "a failed fallback keeps the bar up")
	})
}

func TestBuildFfextNoProfile(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// newTestApp pins firefoxBases to nil: no profile anywhere.
	require.Nil(t, a.buildFfext())
}

// firefoxProfileFixture builds a minimal discoverable profile tree.
func firefoxProfileFixture(t *testing.T) (base string) {
	t.Helper()
	base = t.TempDir()
	prof := filepath.Join(base, "abc.default")
	require.NoError(t, os.MkdirAll(prof, 0o755))
	ini := "[Profile0]\nName=default\nIsRelative=1\nPath=abc.default\nDefault=1\n"
	require.NoError(t, os.WriteFile(filepath.Join(base, "profiles.ini"), []byte(ini), 0o644))
	return base
}

func TestBuildFfextInstallsHostAndListens(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	base := firefoxProfileFixture(t)
	home := t.TempDir()
	sockDir, err := os.MkdirTemp("", "ffx")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "b.sock")

	a.plat.firefoxBases = func() []string { return []string{base} }
	a.plat.userHome = func() (string, error) { return home, nil }
	a.plat.getenv = func(k string) string {
		if k == ffext.EnvSocket {
			return sock
		}
		return ""
	}

	b := a.buildFfext()
	require.NotNil(t, b, "profile found: the bridge comes up")
	installBridge(a, b)
	t.Cleanup(a.shutdownFfext)

	// The native-messaging pieces landed: wrapper in the config dir,
	// manifest in the (fake) home, both naming this binary's stable
	// spelling and the firefox-host subcommand.
	cfgDir := os.Getenv(config.EnvConfigDir)
	wrapper, err := os.ReadFile(filepath.Join(cfgDir, "firefox-host.sh"))
	require.NoError(t, err)
	require.Contains(t, string(wrapper), "firefox-host")
	require.Contains(t, string(wrapper), "/test/bin/competent-search-thing")
	manifest, err := os.ReadFile(filepath.Join(home, ".mozilla", "native-messaging-hosts", ffext.HostName+".json"))
	require.NoError(t, err)
	require.Contains(t, string(manifest), ffext.ExtensionID)

	// The socket is live: a second Listen refuses.
	_, err = ffext.Listen(sock, ffext.ServerOptions{})
	require.ErrorIs(t, err, ffext.ErrAlreadyRunning)
}

func TestBuildFfextListenFailureDegrades(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	base := firefoxProfileFixture(t)
	home := t.TempDir()
	a.plat.firefoxBases = func() []string { return []string{base} }
	a.plat.userHome = func() (string, error) { return home, nil }
	a.plat.getenv = func(k string) string {
		if k == ffext.EnvSocket {
			// An unusable socket path (its parent is a FILE).
			f := filepath.Join(t.TempDir(), "plain")
			_ = os.WriteFile(f, nil, 0o600)
			return filepath.Join(f, "b.sock")
		}
		return ""
	}
	require.Nil(t, a.buildFfext(), "a listen failure degrades to no bridge")
}

func TestHTTPHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://www.example.com/docs": "www.example.com",
		"http://other.example:8080/x":  "other.example",
	} {
		got, ok := httpHost(raw)
		require.True(t, ok, raw)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{
		"about:preferences", "moz-extension://abc/x", "file:///etc/passwd",
		"view-source:https://example.com/", "https://", "", "://bad",
	} {
		_, ok := httpHost(raw)
		require.False(t, ok, raw)
	}
}
