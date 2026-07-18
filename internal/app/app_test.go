package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/portal"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
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
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		r.call("startPortal")
		return nil, portal.ErrNoPortal
	}
	a.plat.ensureGnomeBinding = func(context.Context, platform.Hotkey, string) (gsettings.Applied, error) {
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
	require.Equal(t, Result{Path: "/notes/shopping-list.txt", Name: "shopping-list.txt", IsDir: false}, got[0])

	miss := a.Search("no-such-entry")
	require.NotNil(t, miss, "no-match result is empty but non-nil")
	require.Empty(t, miss)
}

func TestStartupKicksOffIndexBuildAndEmitsProgress(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world.md"), []byte("x"), 0o644))

	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{})
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return m.LiveCount() == 2 },
		5*time.Second, 10*time.Millisecond, "background build fills the index")

	// The final progress callback (done=true) reaches the frontend.
	require.Eventually(t, func() bool {
		for _, e := range r.emitted(eventIndexProgress) {
			if p, ok := e.payload[0].(indexProgress); ok && p.Done {
				return p.Indexed == 2 && p.Seconds >= 0
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "done progress event carries the totals")

	// A second Startup (e.g. context refresh) must not rebuild.
	a.Startup(context.Background())
	require.Len(t, a.Search("hello"), 1)
}

// watchUp reports whether the live-update layer has been installed.
func watchUp(a *App) bool {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	return a.watcher != nil && a.rescanner != nil
}

func TestStartupBringsUpWatcherAndAppliesEvents(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond, "watch layer comes up after the initial build")

	// End to end: a file created NOW reaches Search via fsnotify.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live-created.txt"), []byte("x"), 0o644))
	require.Eventually(t, func() bool { return len(a.Search("live-created")) == 1 },
		20*time.Second, 10*time.Millisecond, "live update flows through to Search")

	a.Shutdown(context.Background())
	a.Shutdown(context.Background()) // idempotent
	require.False(t, watchUp(a), "shutdown tears the watch layer down")
}

func TestShutdownBeforeBuildFinishesSkipsWatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Shutdown(context.Background()) // before Startup: sets the flag, stops nothing
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return m.LiveCount() == 1 },
		20*time.Second, 10*time.Millisecond, "build still completes")
	require.Never(t, func() bool { return watchUp(a) },
		400*time.Millisecond, 20*time.Millisecond, "watch layer never starts after Shutdown")
}

func TestStartWatchToleratesBadExcluder(t *testing.T) {
	// A malformed exclude pattern cannot panic startWatch; the watcher
	// runs with a nil Excluder (excludes nothing).
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a, _ := newTestApp(t, m, Options{})
	a.startWatch()
	require.True(t, watchUp(a))
	a.Shutdown(context.Background())
}

func TestBuildIndexLogsAndSurvivesFailure(t *testing.T) {
	// A malformed exclude pattern makes BuildFromDisk fail; buildIndex
	// must swallow it (log only), never panic.
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a, _ := newTestApp(t, m, Options{})
	a.buildIndex(context.Background())
	require.Equal(t, 0, m.LiveCount())
}

func TestBuildIndexCancelledDiscardsPartialAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "never-indexed.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.buildIndex(ctx)

	require.Contains(t, buf.String(), "index: initial build cancelled")
	require.NotContains(t, buf.String(), "initial build failed")
	require.Equal(t, 0, m.LiveCount(), "the partial store is discarded, never swapped in")
	require.False(t, watchUp(a), "a cancelled build never starts the watch layer")
}

func TestShutdownCancelsInitialBuild(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "one.txt"), []byte("x"), 0o644))
	a, _ := newTestApp(t, index.NewManager([]string{dir}, nil, 0), Options{})
	a.Startup(context.Background())

	// Startup stored the build walk's cancel func; wrap it to observe
	// the call (the tiny build may already have finished -- cancelling
	// a finished build's context is a harmless no-op, the point is that
	// Shutdown pulls the trigger at all).
	called := make(chan struct{})
	a.watchMu.Lock()
	orig := a.buildCancel
	a.buildCancel = func() { orig(); close(called) }
	a.watchMu.Unlock()
	require.NotNil(t, orig, "Startup wires a cancellable context into the initial build")

	a.Shutdown(context.Background())
	select {
	case <-called:
	default:
		t.Fatal("Shutdown did not cancel the initial build context")
	}

	a.watchMu.Lock()
	cleared := a.buildCancel == nil
	a.watchMu.Unlock()
	require.True(t, cleared, "Shutdown clears the stored cancel func")
}

func TestEmitDegradedPayload(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.emitDegraded(watch.Stats{WatchedDirs: 7, DroppedWatches: 3, Overflows: 2, Degraded: true})

	events := r.emitted(eventWatchDegraded)
	require.Len(t, events, 1)
	require.Equal(t, watchDegraded{Watched: 7, Dropped: 3, Overflows: 2}, events[0].payload[0])
}

func TestEmitEventBeforeStartupIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.emitDegraded(watch.Stats{})
	require.Empty(t, r.emits, "no context yet: nothing emitted, nothing crashed")
}

func TestOpenRunsPlatformOpenAndHides(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x"))
	require.Equal(t, []string{"resolve:/tmp/x", "mint", "open:/tmp/x", "hide"}, r.callNames(),
		"resolve then mint (bar still focused) then dispatch then hide")
}

func TestOpenErrorKeepsBarVisible(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	boom := errors.New("no handler")
	a.plat.open = func(string, []string) error { return boom }
	require.ErrorIs(t, a.Open("/tmp/x"), boom)
	require.False(t, r.has("hide"), "a failed open does not hide the bar")
}

func TestRevealRunsPlatformRevealAndHides(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	require.NoError(t, a.Reveal("/tmp/y"))
	require.Equal(t, []string{"resolve:/tmp/y", "mint", "reveal:/tmp/y", "hide"}, r.callNames(),
		"reveal resolves the directory handler, mints, dispatches, hides")

	boom := errors.New("nope")
	a.plat.reveal = func(string, []string, string) error { return boom }
	require.ErrorIs(t, a.Reveal("/tmp/y"), boom)
}

func TestOpenRevealUseRealLauncherValidation(t *testing.T) {
	// The default seams go through platform.Launcher, which rejects
	// empty paths without running anything.
	a := New(nil, Options{})
	require.Error(t, a.Open(""))
	require.Error(t, a.Reveal(""))
}

func TestHideWithoutContextIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Hide()
	require.Empty(t, r.callNames(), "no context yet: the runtime hide is not reached")
}
