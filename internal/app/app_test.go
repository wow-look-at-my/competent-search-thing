package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/portal"
	"github.com/wow-look-at-my/competent-search-thing/internal/progress"
)

// seamRecorder captures every Wails-runtime and platform call an App
// makes, so tests can drive show/hide/toggle/open flows headlessly.
// The real runtime seams abort the process without a Wails context;
// newTestApp therefore replaces ALL of them before any test runs
// Startup.
type seamRecorder struct {
	mu      sync.Mutex
	calls   []string
	emits   []emittedEvent
	setPosX []int
	setPosY []int
	// gcPercents records every setGCPercent value in call order: the
	// build-window bound writes buildGCPercent, its restore writes the
	// fake's fixed previous value back.
	gcPercents []int

	winX, winY int  // what getPos returns
	cursorOK   bool // what cursorInfo returns
	cursorX    int
	cursorY    int
	displays   []platform.Display
	moveOK     bool // what moveWindow returns
}

type emittedEvent struct {
	name    string
	payload []interface{}
}

func (r *seamRecorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

func (r *seamRecorder) callNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *seamRecorder) has(name string) bool {
	for _, c := range r.callNames() {
		if c == name {
			return true
		}
	}
	return false
}

func (r *seamRecorder) emitted(name string) []emittedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []emittedEvent
	for _, e := range r.emits {
		if e.name == name {
			out = append(out, e)
		}
	}
	return out
}

// newTestApp builds an App whose runtime and platform seams are all
// safe fakes recording into the returned seamRecorder. It points the
// config package (and therefore the theme layer Startup brings up) at
// a private temp dir, and shuts the app down at test end -- Shutdown
// is idempotent, so tests exercising it themselves stay valid.
func newTestApp(t *testing.T, m *index.Manager, opt Options) (*App, *seamRecorder) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	a := New(m, opt)
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	r := &seamRecorder{}
	a.rt = runtimeSeams{
		show:   func(context.Context) { r.call("show") },
		hide:   func(context.Context) { r.call("hide") },
		center: func(context.Context) { r.call("center") },
		setPos: func(_ context.Context, x, y int) {
			r.call("setPos")
			r.mu.Lock()
			r.setPosX = append(r.setPosX, x)
			r.setPosY = append(r.setPosY, y)
			r.mu.Unlock()
		},
		getPos: func(context.Context) (int, int) {
			r.call("getPos")
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.winX, r.winY
		},
		setSize: func(_ context.Context, w, h int) {
			r.call(fmt.Sprintf("setSize:%dx%d", w, h))
		},
		emit: func(_ context.Context, name string, data ...interface{}) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.emits = append(r.emits, emittedEvent{name: name, payload: data})
		},
		clipboardSetText: func(_ context.Context, text string) error {
			r.call("clipboard:" + text)
			return nil
		},
		quit: func(context.Context) { r.call("quit") },
	}
	a.plat.startHotkey = func(platform.Hotkey, func()) (func(), error) {
		r.call("startHotkey")
		return func() { r.call("stopHotkey") }, nil
	}
	// Ambient bits pinned down: a fixed goos (the linux launch path,
	// so the app tests behave identically on the darwin CI job; tests
	// exercising other-OS behavior set goos themselves), no real env
	// reads, a fixed executable path, and an unknown session -- which
	// keeps every test on the pre-Wayland native hotkey and
	// positioning paths unless a test overrides detectSession itself.
	a.plat.goos = "linux"
	a.plat.getenv = func(string) string { return "" }
	a.plat.executable = func() (string, error) { return "/test/bin/competent-search-thing", nil }
	// An empty argv[0] plus the nonexistent executable path above keep
	// the stable-path selection inert: no candidate can pass its
	// same-binary guard, so the seam's path is written verbatim.
	a.plat.args0 = func() string { return "" }
	a.plat.detectSession = func() platform.Session { return platform.Session{} }
	// A recording GC seam so no test flips the real runtime's GOGC;
	// the fake's previous value is a recognizable non-default.
	a.plat.setGCPercent = func(pct int) int {
		r.mu.Lock()
		r.gcPercents = append(r.gcPercents, pct)
		r.mu.Unlock()
		return testPrevGCPercent
	}
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		r.call("startPortal")
		return nil, portal.ErrNoPortal
	}
	a.plat.ensureGnomeBinding = func(context.Context, platform.Hotkey, string, bool) (gsettings.Applied, error) {
		r.call("ensureGnomeBinding")
		return gsettings.Applied{}, errors.New("ensureGnomeBinding not stubbed")
	}
	// The daemon probe answers "running" so gsettings-backend tests
	// exercise the verified-summary path unless they override it; the
	// real probe would touch the session bus.
	a.plat.mediaKeysDaemon = func(context.Context) (bool, error) {
		r.call("mediaKeysDaemon")
		return true, nil
	}
	a.plat.cursorInfo = func() (int, int, []platform.Display, bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.cursorX, r.cursorY, r.displays, r.cursorOK
	}
	a.plat.moveWindow = func(x, y int) bool {
		r.call("moveWindow")
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.moveOK
	}
	// The panel hook reports "nothing to configure" like every
	// non-darwin platform; deliberately not recorded so sequence
	// assertions stay untouched. The DomReady test overrides it with a
	// counting fake.
	a.plat.configurePanel = func() bool { return false }
	// The native resize seam reports "no native path" (recorded), so
	// the window-size applier falls through to the rt.setSize fake and
	// tests see both breadcrumbs.
	a.plat.setWindowSize = func(w, h int) bool {
		r.call(fmt.Sprintf("setWindowSize:%dx%d", w, h))
		return false
	}
	// The toolkit work-area probe answers "unknown" (the production
	// value would eat runOnGTKThread's 2s timeout headlessly).
	a.plat.windowWorkArea = func() (platform.Rect, bool) { return platform.Rect{}, false }
	// The hint probe answers "nothing exists" so Search never touches
	// the real disk; hint tests override it (some with the real
	// os.Lstat over temp trees).
	a.plat.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	a.plat.open = func(path string, _ []string) error { r.call("open:" + path); return nil }
	a.plat.reveal = func(path string, _ []string, _ string) error { r.call("reveal:" + path); return nil }
	a.plat.run = func(argv, _ []string) error { r.call("run:" + strings.Join(argv, " ")); return nil }
	// The credentialed launch path's seams: recorders whose defaults
	// keep every test on the pre-credential behavior -- the handler
	// never resolves (so the dispatch falls through to the open/run
	// seams), the mint yields no credential, and with getenv pinned to
	// "" (no DISPLAY) the raise watcher never arms. Launch tests
	// override individual members.
	a.plat.resolveHandler = func(tg launch.Target) (launch.Handler, bool) {
		r.call("resolve:" + tg.Raw)
		return launch.Handler{}, false
	}
	a.plat.handlerByID = func(id string) (launch.Handler, bool) {
		r.call("handlerByID:" + id)
		return launch.Handler{}, false
	}
	a.plat.mintCredential = func(string) launch.Credential {
		r.call("mint")
		return launch.Credential{Kind: launch.KindNone}
	}
	// Silent on purpose: announceLaunch runs it at every Startup, and
	// recording it would pollute every sequence assertion; the launch
	// tests override it with a recording fake.
	a.plat.prepareLaunch = func() {}
	a.plat.dbusLaunch = func(call launch.DBusCall) error {
		r.call("dbusLaunch:" + call.Dest + ":" + call.Method)
		return nil
	}
	a.plat.launchExec = func(argv, _ []string) (int, error) {
		r.call("launchExec:" + strings.Join(argv, " "))
		return 0, nil
	}
	a.plat.watchState = func() (launch.XState, bool) { return launch.XState{}, false }
	a.plat.snRemove = func(id string) error { r.call("snRemove:" + id); return nil }
	a.plat.activateWindow = func(id uint32) error { r.call(fmt.Sprintf("activateWindow:%d", id)); return nil }
	// A nil Source degrades the app-context cache to a no-op; tests
	// that exercise capture inject a fake Source before Startup.
	a.plat.appSource = nil
	// No Firefox profile discovery against the real home; tests that
	// exercise the frequent-sites wiring point this at fixtures.
	a.plat.firefoxBases = func() []string { return nil }
	// No real home either: the ffext manifest install must never write
	// into ~/.mozilla from a test (install tests use temp dirs).
	a.plat.userHome = func() (string, error) { return "", errors.New("no home in tests") }
	// No real /proc walks: the frecency cwd derivation stays inert
	// unless a test injects a fake process tree.
	a.plat.procTree = nil
	// No real NSWorkspace observers (the seam is non-nil when the
	// darwin CI job builds the production seams); Space-watch tests
	// inject a recording fake.
	a.plat.watchSpaceChanges = nil
	// Same for the fps meter's darwin seams (power probe, power-state
	// observer, WebKit near-60 uncap): nil so no test touches Cocoa or
	// the WKPreferences SPI; fps tests inject fakes per test.
	a.plat.powerInfo = nil
	a.plat.watchPowerChanges = nil
	a.plat.uncapNear60 = nil
	// No config.json or plugins-dir IO in unit tests; tests that
	// exercise the real builder restore a.buildRegistry explicitly.
	a.newRegistry = func() dispatcher { return nil }
	// No session-bus IO either: the tray seam yields nothing, so
	// startTray is a no-op. Tray tests inject a recording fake.
	a.newTray = func() trayHandle { return nil }
	// No stats goroutines or /proc//sys probes: the stats seam yields
	// nothing. Stats tests inject a recording fake (or restore
	// a.buildStats explicitly).
	a.newStats = func() statsSource { return nil }
	// No icon-theme detection (gsettings exec) or disk lookups:
	// ResolveIcons answers empty maps. Icon tests inject a fake.
	a.newIcons = func() iconResolver { return nil }
	// No ffext bridge socket or manifest writes (ffext_test.go fakes).
	a.newFfext = func() ffextBridge { return nil }
	// No login-service registration: the production builder stats the
	// real home and Ensure execs launchctl/systemctl. Service tests
	// inject a recording fake registrar.
	a.newService = func() serviceRegistrar { return nil }
	// The progress printer is inert: non-TTY (never intercepts the
	// global log output), io.Discard target, dropped non-TTY lines.
	// Progress tests inject recording printers.
	a.newProgress = func() *progress.Printer { return progress.New(io.Discard, false, nil) }
	// Nil by default so SetupWatch never probes fanotify or spawns
	// pkexec; watch-setup tests inject a recording fake.
	a.newWatchSetup = func() watchSetupRunner { return nil }
	return a, r
}

func TestNewHasNoContext(t *testing.T) {
	a := New(nil, Options{})
	require.Nil(t, a.ctx)
	require.NotNil(t, a.plat.open, "production seams are populated")
	require.NotNil(t, a.rt.emit)
}

func TestStartupSavesContext(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	a.Startup(ctx)
	require.Equal(t, ctx, a.runtimeCtx())
}

func TestStartupLogsConfigNotesOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{ConfigNotes: []string{
		"index roots upgraded to the whole-filesystem default (/)",
		"system exclude patterns added for whole-filesystem indexing: /proc",
	}})
	a.Startup(context.Background())
	a.Startup(context.Background()) // a second Startup must not repeat them

	// Drain the async layer goroutines (priors/arbiter/telemetry log
	// through the global logger) before reading the shared buffer.
	a.Shutdown(context.Background())
	out := buf.String()
	require.Equal(t, 1, strings.Count(out, "config: index roots upgraded to the whole-filesystem default (/)"),
		"each migration note is logged exactly once, config-prefixed")
	require.Equal(t, 1, strings.Count(out, "config: system exclude patterns added"))
}

func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	a, _ := newTestApp(t, index.NewManager(nil, nil, 0), Options{})
	for _, q := range []string{"", "   ", "\t \n"} {
		got := a.Search(q)
		require.NotNil(t, got)
		require.Empty(t, got)
	}
}

func TestSearchWithoutManagerReturnsEmpty(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	got := a.Search("hello")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestSearchQueriesManager(t *testing.T) {
	m := index.NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/notes", "shopping-list.txt", false))
	require.NoError(t, m.Add("/notes", "projects", true))
	a, _ := newTestApp(t, m, Options{})

	got := a.Search("  shopping  ") // query is trimmed
	require.Len(t, got, 1)
	require.Equal(t, Result{
		Path: "/notes/shopping-list.txt", Name: "shopping-list.txt", IsDir: false,
		MatchRanges: [][2]int{{0, 8}}, // the engine highlights the matched prefix
	}, got[0])

	miss := a.Search("no-such-entry")
	require.NotNil(t, miss, "no-match result is empty but non-nil")
	require.Empty(t, miss)
}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Hide()
	require.Empty(t, r.callNames(), "no context yet: the runtime hide is not reached")
}
