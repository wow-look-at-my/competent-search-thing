package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// geditHandler is a resolved, fully capable handler fixture.
var geditHandler = launch.Handler{
	DesktopID:       "org.gnome.gedit.desktop",
	Exec:            "gedit %U",
	WMClass:         "gedit",
	Exe:             "/usr/bin/gedit",
	DBusActivatable: true,
	StartupNotify:   true,
}

// launchTestApp is newTestApp plus a resolved handler and a real
// credential, the launch flow's happy-path fixture.
func launchTestApp(t *testing.T, h launch.Handler, cred launch.Credential) (*App, *seamRecorder) {
	t.Helper()
	a, r := newTestApp(t, nil, Options{})
	a.plat.resolveHandler = func(tg launch.Target) (launch.Handler, bool) {
		r.call("resolve:" + tg.Raw)
		return h, true
	}
	a.plat.mintCredential = func(desktopID string) launch.Credential {
		r.call("mint:" + desktopID)
		return cred
	}
	return a, r
}

func TestOpenDBusTransport(t *testing.T) {
	cred := launch.Credential{ID: "cred-1", Kind: launch.KindWaylandGDK}
	a, r := launchTestApp(t, geditHandler, cred)
	var gotCall launch.DBusCall
	a.plat.dbusLaunch = func(call launch.DBusCall) error {
		r.call("dbusLaunch:" + call.Dest + ":" + call.Method)
		gotCall = call
		return nil
	}
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/x.txt", "mint:org.gnome.gedit.desktop", "dbusLaunch:org.gnome.gedit:Open", "hide"},
		r.callNames(), "resolve -> mint -> dbus dispatch -> hide; exec and xdg-open never run")
	require.Equal(t, []string{"file:///tmp/x.txt"}, gotCall.URIs)
	require.Equal(t, map[string]string{
		"desktop-startup-id": "cred-1",
		"activation-token":   "cred-1",
	}, gotCall.PlatformData, "the credential rides platform-data under both keys")
}

func TestOpenDBusFailureFallsBackToExec(t *testing.T) {
	cred := launch.Credential{ID: "cred-2", Kind: launch.KindWaylandGDK}
	a, r := launchTestApp(t, geditHandler, cred)
	a.plat.dbusLaunch = func(call launch.DBusCall) error {
		r.call("dbusLaunch:" + call.Dest + ":" + call.Method)
		return errors.New("no bus")
	}
	var execEnv []string
	a.plat.launchExec = func(argv, env []string) (int, error) {
		r.call("launchExec:" + strings.Join(argv, " "))
		execEnv = env
		return 4242, nil
	}
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/x.txt", "mint:org.gnome.gedit.desktop", "dbusLaunch:org.gnome.gedit:Open",
			"launchExec:gedit file:///tmp/x.txt", "hide"},
		r.callNames(), "a failed dbus activation falls through to the handler's own Exec")
	require.Equal(t, launch.CredentialEnv(cred), execEnv, "the credential rides the child env")
}

func TestOpenExecFailureFallsBackToXdgOpen(t *testing.T) {
	h := geditHandler
	h.DBusActivatable = false // no dbus tier
	cred := launch.Credential{ID: "cred-3", Kind: launch.KindWaylandGDK}
	a, r := launchTestApp(t, h, cred)
	a.plat.launchExec = func(argv, _ []string) (int, error) {
		r.call("launchExec:" + strings.Join(argv, " "))
		return 0, errors.New("gone")
	}
	var openEnv []string
	a.plat.open = func(path string, env []string) error {
		r.call("open:" + path)
		openEnv = env
		return nil
	}
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/x.txt", "mint:org.gnome.gedit.desktop", "launchExec:gedit file:///tmp/x.txt",
			"open:/tmp/x.txt", "hide"},
		r.callNames(), "a failed exec falls through to the xdg-open candidates")
	require.Equal(t, launch.CredentialEnv(cred), openEnv,
		"the credential still rides the fallback child env")
}

func TestOpenTerminalHandlerSkipsExec(t *testing.T) {
	h := launch.Handler{DesktopID: "vim.desktop", Exec: "vim %f", Terminal: true, StartupNotify: true}
	a, r := launchTestApp(t, h, launch.Credential{Kind: launch.KindNone})
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/x.txt", "mint:vim.desktop", "open:/tmp/x.txt", "hide"},
		r.callNames(), "Terminal=true handlers go straight to xdg-open (which knows terminals)")
}

func TestOpenPlainHandlerSkipsTheMint(t *testing.T) {
	h := launch.Handler{DesktopID: "plain.desktop", Exec: "plain %f", Exe: "/usr/bin/plain"}
	a, r := launchTestApp(t, h, launch.Credential{ID: "never", Kind: launch.KindX11SN})
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/x.txt", "launchExec:plain /tmp/x.txt", "hide"},
		r.callNames(),
		"no StartupNotify, no DBusActivatable: minting would dangle a busy cursor, so it is skipped")
}

func TestOpenTotalFailureReapsSequenceAndKeepsBar(t *testing.T) {
	cred := launch.Credential{ID: "sn-id-1", Kind: launch.KindX11SN}
	a, r := launchTestApp(t, launch.Handler{}, cred)
	// Unresolve the handler but keep the recorded mint credential:
	// only the xdg-open tier runs, and it fails.
	a.plat.resolveHandler = func(tg launch.Target) (launch.Handler, bool) {
		r.call("resolve:" + tg.Raw)
		return launch.Handler{}, false
	}
	boom := errors.New("no handler at all")
	a.plat.open = func(string, []string) error { return boom }
	a.Startup(context.Background())
	require.ErrorIs(t, a.Open("/tmp/x.txt"), boom)
	require.False(t, r.has("hide"), "a failed open keeps the bar visible")
	require.True(t, r.has("snRemove:sn-id-1"),
		"nothing was launched, so the x11 startup sequence is reaped immediately")
}

func TestOpenArmsWatcherActivatesAndReaps(t *testing.T) {
	cred := launch.Credential{ID: "sn-id-2", Kind: launch.KindX11SN}
	h := geditHandler
	h.DBusActivatable = false
	a, r := launchTestApp(t, h, cred)
	a.plat.getenv = func(k string) string {
		if k == "DISPLAY" {
			return ":0"
		}
		return ""
	}
	var mu sync.Mutex
	polls := 0
	a.plat.watchState = func() (launch.XState, bool) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls == 1 { // the pre-launch snapshot
			return launch.XState{Windows: []launch.XWindow{{ID: 1, Instance: "competent-search-thing"}}, Active: 1}, true
		}
		// Later polls: the launched editor window appeared.
		return launch.XState{Windows: []launch.XWindow{
			{ID: 1, Instance: "competent-search-thing"},
			{ID: 5, Class: "gedit"},
		}, Active: 1}, true
	}
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.True(t, r.has("hide"), "the launch dispatch itself never waits for the watcher")
	require.Eventually(t, func() bool { return r.has("activateWindow:5") },
		5*time.Second, 20*time.Millisecond, "the watcher raises the new gedit window")
	require.Eventually(t, func() bool { return r.has("snRemove:sn-id-2") },
		5*time.Second, 20*time.Millisecond, "the finished watcher reaps the startup sequence")
}

func TestOpenLogsTheLaunchLine(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	cred := launch.Credential{ID: "0123456789", Kind: launch.KindWaylandGDK}
	a, _ := launchTestApp(t, geditHandler, cred)
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.Contains(t, buf.String(),
		"launch: open /tmp/x.txt handler=org.gnome.gedit.desktop credential=wayland-gdk:01234567 transport=dbus watcher=off")
}

func TestOpenTargetClassification(t *testing.T) {
	var got []launch.Target
	a, _ := newTestApp(t, nil, Options{})
	a.plat.resolveHandler = func(tg launch.Target) (launch.Handler, bool) {
		got = append(got, tg)
		return launch.Handler{}, false
	}
	dir := t.TempDir()
	a.plat.lstat = os.Lstat
	a.Startup(context.Background())

	require.NoError(t, a.Open(dir))
	require.NoError(t, a.Open("https://example.com/x"))
	a.plat.lstat = func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }
	require.NoError(t, a.Reveal("/tmp/file.txt"))

	require.Len(t, got, 3)
	require.True(t, got[0].IsDir, "a real directory resolves as inode/directory")
	require.True(t, got[1].IsURL)
	require.Equal(t, "https", got[1].Scheme)
	require.True(t, got[2].IsDir, "reveal resolves the file MANAGER, i.e. the directory handler")
	require.Equal(t, "/tmp/file.txt", got[2].Raw)
}

func TestRevealCarriesCredentialAndStartupID(t *testing.T) {
	nautilus := launch.Handler{
		DesktopID:       "org.gnome.Nautilus.desktop",
		Exec:            "nautilus %U",
		Exe:             "/usr/bin/nautilus",
		DBusActivatable: true,
		StartupNotify:   true,
	}
	cred := launch.Credential{ID: "reveal-cred", Kind: launch.KindWaylandGDK}
	a, r := launchTestApp(t, nautilus, cred)
	var gotEnv []string
	var gotSID string
	a.plat.reveal = func(path string, env []string, sid string) error {
		r.call("reveal:" + path)
		gotEnv, gotSID = env, sid
		return nil
	}
	a.Startup(context.Background())
	require.NoError(t, a.Reveal("/tmp/y.txt"))
	require.Equal(t,
		[]string{"resolve:/tmp/y.txt", "mint:org.gnome.Nautilus.desktop", "reveal:/tmp/y.txt", "hide"},
		r.callNames())
	require.Equal(t, "reveal-cred", gotSID, "the minted id rides the ShowItems startup-id argument")
	require.Equal(t, launch.CredentialEnv(cred), gotEnv, "and the fallback child env")
}

func TestRevealFailureReapsSequence(t *testing.T) {
	cred := launch.Credential{ID: "sn-id-3", Kind: launch.KindX11SN}
	a, r := launchTestApp(t, launch.Handler{StartupNotify: true, DesktopID: "fm.desktop"}, cred)
	boom := errors.New("no file manager")
	a.plat.reveal = func(string, []string, string) error { return boom }
	a.Startup(context.Background())
	require.ErrorIs(t, a.Reveal("/tmp/y.txt"), boom)
	require.False(t, r.has("hide"))
	require.True(t, r.has("snRemove:sn-id-3"))
}

func TestRunCommandActionDBusActivation(t *testing.T) {
	code := launch.Handler{
		DesktopID:       "org.gnome.gedit.desktop",
		Exec:            "gedit %U",
		Exe:             "/usr/bin/gedit",
		DBusActivatable: true,
		StartupNotify:   true,
	}
	a, r := newTestApp(t, nil, Options{})
	a.plat.handlerByID = func(id string) (launch.Handler, bool) {
		r.call("handlerByID:" + id)
		return code, true
	}
	a.plat.mintCredential = func(desktopID string) launch.Credential {
		r.call("mint:" + desktopID)
		return launch.Credential{ID: "run-cred", Kind: launch.KindWaylandGDK}
	}
	var gotCall launch.DBusCall
	a.plat.dbusLaunch = func(call launch.DBusCall) error {
		r.call("dbusLaunch:" + call.Dest + ":" + call.Method)
		gotCall = call
		return nil
	}
	a.Startup(context.Background())
	err := a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"gedit"}, DesktopID: "org.gnome.gedit.desktop"})
	require.NoError(t, err)
	require.Equal(t,
		[]string{"handlerByID:org.gnome.gedit.desktop", "mint:org.gnome.gedit.desktop",
			"dbusLaunch:org.gnome.gedit:Activate", "hide"},
		r.callNames(), "a DBusActivatable app launches via Activate -- raising its existing window")
	require.Nil(t, gotCall.URIs)
	require.Equal(t, "run-cred", gotCall.PlatformData["activation-token"])
	require.False(t, r.has("run:gedit"), "the argv spawn never runs when activation succeeded")
}

func TestRunCommandActionDBusFailureFallsBackToArgv(t *testing.T) {
	code := launch.Handler{DesktopID: "org.gnome.gedit.desktop", DBusActivatable: true, StartupNotify: true}
	a, r := newTestApp(t, nil, Options{})
	a.plat.handlerByID = func(id string) (launch.Handler, bool) { return code, true }
	cred := launch.Credential{ID: "run-cred-2", Kind: launch.KindWaylandGDK}
	a.plat.mintCredential = func(string) launch.Credential { return cred }
	a.plat.dbusLaunch = func(launch.DBusCall) error { return errors.New("activation refused") }
	var gotEnv []string
	a.plat.run = func(argv, env []string) error {
		r.call("run:" + strings.Join(argv, " "))
		gotEnv = env
		return nil
	}
	a.Startup(context.Background())
	err := a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"gedit", "--new"}, DesktopID: "org.gnome.gedit.desktop"})
	require.NoError(t, err)
	require.True(t, r.has("run:gedit --new"), "failed activation falls back to the validated argv")
	require.Equal(t, launch.CredentialEnv(cred), gotEnv)
	require.True(t, r.has("hide"))
}

func TestRunCommandActionNonDBusHandlerSpawnsWithEnv(t *testing.T) {
	plainApp := launch.Handler{DesktopID: "term.desktop", StartupNotify: true}
	a, r := newTestApp(t, nil, Options{})
	a.plat.handlerByID = func(id string) (launch.Handler, bool) { return plainApp, true }
	cred := launch.Credential{ID: "run-cred-3", Kind: launch.KindX11SN}
	a.plat.mintCredential = func(string) launch.Credential { return cred }
	var gotEnv []string
	a.plat.run = func(argv, env []string) error {
		r.call("run:" + strings.Join(argv, " "))
		gotEnv = env
		return nil
	}
	a.Startup(context.Background())
	err := a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"thing"}, DesktopID: "term.desktop"})
	require.NoError(t, err)
	require.False(t, r.has("dbusLaunch:"), "no dbus tier without DBusActivatable")
	require.Equal(t, launch.CredentialEnv(cred), gotEnv)
}

func TestRunCommandActionWithoutDesktopIDIsOldBehavior(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	err := a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: []string{"firefox"}})
	require.NoError(t, err)
	require.Equal(t, []string{"run:firefox", "hide"}, r.callNames(),
		"no desktop id: no resolve, no mint, no watcher -- byte-identical old behavior")
}

func TestRunCommandActionRejectsBadDesktopID(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	for _, bad := range []string{"../evil.desktop", "a/b.desktop", "code", ".desktop"} {
		err := a.RunPluginAction("apps", plugin.Action{
			Type: plugin.ActionRunCommand, Argv: []string{"x"}, DesktopID: bad})
		require.Error(t, err, "desktop id %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid desktop ids never reach any seam")
}

func TestRunCommandActionSpawnFailureReapsSequence(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.handlerByID = func(id string) (launch.Handler, bool) {
		return launch.Handler{DesktopID: id, StartupNotify: true}, true
	}
	a.plat.mintCredential = func(string) launch.Credential {
		return launch.Credential{ID: "sn-id-4", Kind: launch.KindX11SN}
	}
	boom := errors.New("spawn failed")
	a.plat.run = func([]string, []string) error { return boom }
	a.Startup(context.Background())
	err := a.RunPluginAction("apps", plugin.Action{
		Type: plugin.ActionRunCommand, Argv: []string{"x"}, DesktopID: "x.desktop"})
	require.ErrorIs(t, err, boom)
	require.False(t, r.has("hide"))
	require.True(t, r.has("snRemove:sn-id-4"))
}

func TestWatcherlessX11SequenceReapedAfterDelay(t *testing.T) {
	old := launchReapDelay
	launchReapDelay = 20 * time.Millisecond
	defer func() { launchReapDelay = old }()

	cred := launch.Credential{ID: "sn-id-5", Kind: launch.KindX11SN}
	h := launch.Handler{DesktopID: "app.desktop", Exec: "app %f", StartupNotify: true}
	a, r := launchTestApp(t, h, cred)
	// getenv stays pinned to "": no DISPLAY, so no watcher -- and yet
	// the sequence must still be reaped after the grace delay.
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.False(t, r.has("snRemove:sn-id-5"), "not reaped synchronously")
	require.Eventually(t, func() bool { return r.has("snRemove:sn-id-5") },
		5*time.Second, 10*time.Millisecond)
}

func TestLaunchWatchCtxCancelledByShutdown(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	ctx := a.launchWatchCtx()
	select {
	case <-ctx.Done():
		t.Fatal("fresh launch context must be live")
	default:
	}
	a.Shutdown(context.Background())
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown must cancel the launch context")
	}

	// An app shut down before any launch parks a pre-cancelled context.
	b, _ := newTestApp(t, nil, Options{})
	b.Shutdown(context.Background())
	select {
	case <-b.launchWatchCtx().Done():
	default:
		t.Fatal("post-shutdown launch context must already be cancelled")
	}
}

func TestAnnounceLaunch(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, r := newTestApp(t, nil, Options{})
	prepared := false
	a.plat.prepareLaunch = func() { prepared = true; r.call("prepareLaunch") }
	a.Startup(context.Background())
	a.Startup(context.Background()) // once only
	require.True(t, prepared)
	// Drain a's async layers before touching the shared buffer: the
	// always-on priors/arbiter/telemetry goroutines log through the
	// global logger, and a bytes.Buffer read or Reset racing one of
	// those writes corrupts the buffer (a Reset can even resurrect
	// pre-Reset content). Shutdown waits all of them out and is
	// idempotent under the Cleanup-registered second call.
	a.Shutdown(context.Background())
	require.Equal(t, 1, strings.Count(buf.String(), "launch: activation credentials enabled (session=unknown)"))

	buf.Reset()
	b, br := newTestApp(t, nil, Options{})
	b.plat.goos = "darwin"
	b.plat.prepareLaunch = func() { br.call("prepareLaunch") }
	b.Startup(context.Background())
	require.False(t, br.has("prepareLaunch"), "non-linux: no native prep")
	b.Shutdown(context.Background()) // same drain before the final read
	require.NotContains(t, buf.String(), "activation credentials enabled")
}

func TestOpenOffLinuxIsOldBehavior(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.goos = "windows"
	a.Startup(context.Background())
	require.NoError(t, a.Open(`C:\x.txt`))
	require.Equal(t, []string{`open:C:\x.txt`, "hide"}, r.callNames(),
		"off linux: no resolve, no mint, no watcher")
	require.NoError(t, a.Reveal(`C:\x.txt`))
	require.Equal(t, []string{`open:C:\x.txt`, "hide", `reveal:C:\x.txt`, "hide"}, r.callNames())
}

func TestWatcherBeforeGates(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// No DISPLAY (default getenv stub): off.
	before, ok := a.watcherBefore()
	require.False(t, ok)
	require.Nil(t, before)

	a.plat.getenv = func(k string) string { return ":0" }
	// watchState says no X server: off.
	a.plat.watchState = func() (launch.XState, bool) { return launch.XState{}, false }
	_, ok = a.watcherBefore()
	require.False(t, ok)

	a.plat.watchState = func() (launch.XState, bool) {
		return launch.XState{Windows: []launch.XWindow{{ID: 7}, {ID: 9}}}, true
	}
	before, ok = a.watcherBefore()
	require.True(t, ok)
	require.Equal(t, map[uint32]bool{7: true, 9: true}, before)
}

func TestDispatchOpenSkipsEmptyExpandedExec(t *testing.T) {
	// A handler whose Exec expands to nothing must not spawn an empty
	// argv; the launch falls through to xdg-open.
	h := launch.Handler{DesktopID: "weird.desktop", Exec: "%i", StartupNotify: true}
	a, r := launchTestApp(t, h, launch.Credential{Kind: launch.KindNone})
	a.Startup(context.Background())
	require.NoError(t, a.Open("/tmp/x.txt"))
	require.False(t, strings.HasPrefix(strings.Join(r.callNames(), ","), "launchExec"),
		"no exec spawn for an empty expansion")
	require.True(t, r.has("open:/tmp/x.txt"))
}

/* --- the basic Open/Reveal paths (moved from app_test.go when it hit
   the 750-line cap; they exercise the same launch pipeline the rest
   of this file covers) --------------------------------------------- */

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
