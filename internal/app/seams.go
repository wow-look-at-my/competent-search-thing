package app

import (
	"context"
	"os"
	goruntime "runtime"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/firefox"
	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform/native"
)

// runtimeSeams carries the Wails runtime calls the App makes. Calling
// any of the real functions without a genuine Wails context aborts the
// process, so every call site first checks runtimeCtx() != nil, and
// unit tests replace the whole struct with fakes.
type runtimeSeams struct {
	show             func(ctx context.Context)
	hide             func(ctx context.Context)
	center           func(ctx context.Context)
	setPos           func(ctx context.Context, x, y int)
	getPos           func(ctx context.Context) (int, int)
	setSize          func(ctx context.Context, w, h int)
	emit             func(ctx context.Context, name string, data ...interface{})
	clipboardSetText func(ctx context.Context, text string) error
	quit             func(ctx context.Context)
}

func defaultRuntimeSeams() runtimeSeams {
	return runtimeSeams{
		show:             runtime.WindowShow,
		hide:             runtime.WindowHide,
		center:           runtime.WindowCenter,
		setPos:           runtime.WindowSetPosition,
		getPos:           runtime.WindowGetPosition,
		setSize:          runtime.WindowSetSize,
		emit:             runtime.EventsEmit,
		clipboardSetText: runtime.ClipboardSetText,
		quit:             runtime.Quit,
	}
}

// platformSeams carries the platform-layer hooks (hotkey, displays,
// open/reveal/run, app-context source, the Wayland hotkey backends)
// plus the ambient bits (GOOS, clock, env, executable path, session
// detection) tests pin down.
type platformSeams struct {
	goos       string
	now        func() time.Time
	getenv     func(string) string
	executable func() (string, error)
	// args0 returns the process's argv[0] -- the spelling the binary
	// was launched by, possibly an unresolved symlink ("" when
	// unknown); the stable-path selection for the GNOME keybinding
	// command consumes it as a fallback candidate.
	args0         func() string
	detectSession func() platform.Session
	// setGCPercent swaps the runtime's GOGC value and returns the
	// previous one; the initial index build lowers it temporarily to
	// bound the walk's peak heap (gcbound.go). Production is
	// debug.SetGCPercent, whose percentage composes with any
	// externally installed GOMEMLIMIT.
	setGCPercent func(pct int) int
	startHotkey  func(hk platform.Hotkey, onDown func()) (stop func(), err error)
	// startPortal registers the summon shortcut through the XDG portal
	// (may block on interactive approval; ctx aborts); production is
	// startPortalShortcut in hotkey.go.
	startPortal func(ctx context.Context, hk platform.Hotkey, onActivated func()) (portalHandle, error)
	// ensureGnomeBinding installs/refreshes the GNOME custom
	// keybinding running command; production wraps
	// gsettings.EnsureBindingWith with the real gsettings CLI runner.
	// force (the config live-apply path only) rewrites an existing
	// entry's accelerator to the new config hotkey; the default sticky
	// path never touches it.
	ensureGnomeBinding func(ctx context.Context, hk platform.Hotkey, command string, force bool) (gsettings.Applied, error)
	// mediaKeysDaemon reports whether gsd-media-keys owns its
	// session-bus name -- the daemon a GNOME keybinding is inert
	// without; production is gsettings.DaemonRunning. An error means
	// the check could not run (no session bus) and is skipped quietly.
	mediaKeysDaemon func(ctx context.Context) (bool, error)
	cursorInfo      func() (cx, cy int, ds []platform.Display, ok bool)
	moveWindow      func(x, y int) bool
	// configurePanel applies the Spotlight-style panel collection
	// behavior to the app window (darwin; other platforms report false,
	// nothing to configure). Called exactly once, at DomReady -- the
	// earliest point every platform has a native window.
	configurePanel func() bool
	// setWindowSize resizes the native window to w x h, moving the
	// non-resizable window's fixed-size floor with it (linux: GTK
	// default-size + resize on the GTK thread -- the Wails runtime's
	// bare gtk_window_resize cannot shrink below the construction-time
	// default; production native.SetWindowSize, false off linux or
	// when the GTK loop is unreachable, and the caller falls back to
	// the Wails runtime setSize, which is sufficient on
	// darwin/windows).
	setWindowSize func(w, h int) bool
	// windowWorkArea reports the usable area of the monitor the bar
	// window currently sits on, straight from the toolkit -- the
	// clamp-to-screen source when the display list is unavailable, and
	// the ONLY one on Wayland (production native.WindowWorkArea: gdk
	// on the GTK thread, linux only; false elsewhere, where
	// cursorInfo's Work rects cover the clamp).
	windowWorkArea func() (platform.Rect, bool)
	// lstat probes the disk for the outside-roots hint (hint.go) and
	// the launch path's directory check; production is os.Lstat, tests
	// pin it so no real IO happens.
	lstat func(path string) (os.FileInfo, error)
	// open/reveal/run execute launches; extraEnv carries the minted
	// launch credential to the child (nil = old behavior), and
	// reveal's startupID rides the FileManager1 ShowItems argument.
	open   func(path string, extraEnv []string) error
	reveal func(path string, extraEnv []string, startupID string) error
	run    func(argv, extraEnv []string) error
	// launchExec spawns one resolved handler command line under the
	// launcher's observed-grace semantics and reports the child pid
	// for the raise watcher; production is Launcher.Launch.
	launchExec func(argv, extraEnv []string) (int, error)
	// resolveHandler and handlerByID look up the default application
	// for a target / a .desktop id (launch capabilities included);
	// production is the native gio glue, linux only.
	resolveHandler func(t launch.Target) (launch.Handler, bool)
	handlerByID    func(id string) (launch.Handler, bool)
	// mintCredential mints one launch credential on the GTK thread
	// (startup-notification id or activation token), described by the
	// resolved handler's desktop id ("" = a synthesized appinfo);
	// best-effort, a none-credential on timeout or unsupported
	// backends.
	mintCredential func(desktopID string) launch.Credential
	// prepareLaunch performs the one-time native launch setup (the
	// Wayland input-serial listener); called once at Startup.
	prepareLaunch func()
	// dbusLaunch performs one org.freedesktop.Application activation
	// (the D-Bus launch transport); production wraps
	// launch.DBusActivate with a bounded timeout.
	dbusLaunch func(call launch.DBusCall) error
	// watchState reads the raise watcher's X snapshot (stacking-order
	// windows + active window); ok=false when there is no X server.
	watchState func() (launch.XState, bool)
	// snRemove broadcasts the startup-notification remove message
	// that reaps an X11 startup sequence our launchee never completed
	// (chromium-family apps); production is the native xgb broadcast.
	snRemove func(id string) error
	// activateWindow raises and focuses one open window by its
	// window-system id (the activate_window plugin action and the
	// raise watcher); production is the native EWMH client message
	// with a fresh server timestamp.
	activateWindow func(id uint32) error
	// watchSpaceChanges arms the active-Space-change observer and
	// reports whether it installed; onChange fires on every switch.
	// Darwin only -- nil on every other platform (the defaultProcTree
	// pattern), production native.WatchSpaceChanges.
	watchSpaceChanges func(onChange func()) bool
	// powerInfo probes the display's max refresh rate plus the Low
	// Power Mode / thermal states behind the fps meter's context lines
	// (fps.go). Darwin only -- nil elsewhere (the watchSpaceChanges
	// pattern), production native.DisplayPowerInfo.
	powerInfo func() (platform.PowerInfo, bool)
	// watchPowerChanges arms the power/thermal-state change observer
	// (fps.go logs each flip); darwin only -- nil elsewhere,
	// production native.WatchPowerChanges.
	watchPowerChanges func(onChange func()) bool
	// uncapNear60 flips WebKit's near-60 rendering cap off through the
	// guarded WKPreferences SPI and reports what happened; darwin only
	// -- nil elsewhere, production native.WebViewUncapNear60.
	uncapNear60 func() platform.UncapStatus
	appSource   appctx.Source
	// firefoxBases lists the Firefox profiles.ini base directories the
	// frequent-sites discovery probes; production is
	// firefox.DefaultBaseDirs (the real home), tests pin it.
	firefoxBases func() []string
	// userHome resolves the home directory for the Firefox
	// native-messaging manifest location (ffext.go); production is
	// os.UserHomeDir, tests pin it so no test can ever write into the
	// real ~/.mozilla.
	userHome func() (string, error)
	// procTree builds a fresh process-tree snapshot for one focused-app
	// cwd derivation (the frecency cwd boost, see frecency.go); nil
	// means the platform has no source and the boost stays off.
	// Production: appctx.NewProcTree("/proc") per capture, linux only.
	procTree func() frecency.ProcTree
}

func defaultPlatformSeams() platformSeams {
	launcher := platform.NewLauncher()
	return platformSeams{
		goos:       goruntime.GOOS,
		now:        time.Now,
		getenv:     os.Getenv,
		executable: os.Executable,
		args0: func() string {
			if len(os.Args) == 0 {
				return ""
			}
			return os.Args[0]
		},
		detectSession: func() platform.Session { return platform.DetectSession(os.Getenv) },
		setGCPercent:  debug.SetGCPercent,
		startHotkey:   native.StartHotkey,
		startPortal:   startPortalShortcut,
		ensureGnomeBinding: func(ctx context.Context, hk platform.Hotkey, command string, force bool) (gsettings.Applied, error) {
			return gsettings.EnsureBindingWith(ctx, gsettings.Run, hk, command, gsettings.BindingOptions{ForceBinding: force})
		},
		mediaKeysDaemon: gsettings.DaemonRunning,
		cursorInfo:      native.CursorDisplays,
		moveWindow:      native.MoveWindow,
		configurePanel:  native.ConfigurePanel,
		setWindowSize:   native.SetWindowSize,
		windowWorkArea:  native.WindowWorkArea,
		lstat:           os.Lstat,
		open:            launcher.OpenEnv,
		reveal:          launcher.RevealEnv,
		run:             launcher.Run,
		launchExec:      launcher.Launch,
		resolveHandler:  native.ResolveHandler,
		handlerByID:     native.HandlerByDesktopID,
		mintCredential: func(desktopID string) launch.Credential {
			return native.MintLaunchCredential(launchMintTimeout, desktopID)
		},
		prepareLaunch: native.PrepareLaunch,
		dbusLaunch: func(call launch.DBusCall) error {
			ctx, cancel := context.WithTimeout(context.Background(), launchDBusTimeout)
			defer cancel()
			return launch.DBusActivate(ctx, call)
		},
		watchState:        native.WatchState,
		snRemove:          native.RemoveStartupSequence,
		activateWindow:    native.ActivateWindow,
		appSource:         native.AppSource(),
		firefoxBases:      firefox.DefaultBaseDirs,
		userHome:          os.UserHomeDir,
		procTree:          defaultProcTree(goruntime.GOOS),
		watchSpaceChanges: defaultSpaceWatch(goruntime.GOOS),
		powerInfo:         defaultPowerInfo(goruntime.GOOS),
		watchPowerChanges: defaultPowerWatch(goruntime.GOOS),
		uncapNear60:       defaultUncapNear60(goruntime.GOOS),
	}
}

// defaultPowerInfo returns the per-OS display/power probe behind the
// fps meter's context lines: the native NSScreen/NSProcessInfo read on
// darwin, nil elsewhere (no probe exists; fps.go says so honestly).
func defaultPowerInfo(goos string) func() (platform.PowerInfo, bool) {
	if goos != "darwin" {
		return nil
	}
	return native.DisplayPowerInfo
}

// defaultPowerWatch returns the per-OS power-state change observer:
// the native NSProcessInfo observer on darwin, nil elsewhere.
func defaultPowerWatch(goos string) func(onChange func()) bool {
	if goos != "darwin" {
		return nil
	}
	return native.WatchPowerChanges
}

// defaultUncapNear60 returns the per-OS WebKit near-60 uncap: the
// guarded WKPreferences SPI flip on darwin, nil elsewhere (WebKitGTK
// has no such cap; fps.go simply skips).
func defaultUncapNear60(goos string) func() platform.UncapStatus {
	if goos != "darwin" {
		return nil
	}
	return native.WebViewUncapNear60
}

// defaultSpaceWatch returns the per-OS Space-change observer: the
// native NSWorkspace observer on darwin, nil elsewhere (Spaces are a
// macOS concept; linux/windows never arm the dismiss).
func defaultSpaceWatch(goos string) func(onChange func()) bool {
	if goos != "darwin" {
		return nil
	}
	return native.WatchSpaceChanges
}

// defaultProcTree returns the per-capture process-tree factory for
// the frecency cwd derivation: a fresh /proc snapshot on linux, nil
// elsewhere (windows/darwin have no /proc; the cwd boost simply does
// not exist there yet, documented in the README).
func defaultProcTree(goos string) func() frecency.ProcTree {
	if goos != "linux" {
		return nil
	}
	return func() frecency.ProcTree { return appctx.NewProcTree("/proc") }
}
