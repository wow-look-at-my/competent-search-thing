package app

import (
	"context"
	"log"
	"os"
	goruntime "runtime"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
	"github.com/wow-look-at-my/competent-search-thing/internal/firefox"
	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform/native"
)

// toggleGap rate-limits the hotkey toggle: X11 and Windows both
// deliver key autorepeat while the combination is held, which would
// otherwise flicker the bar.
const toggleGap = 250 * time.Millisecond

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
	startHotkey   func(hk platform.Hotkey, onDown func()) (stop func(), err error)
	// startPortal registers the summon shortcut through the XDG portal
	// (may block on interactive approval; ctx aborts); production is
	// startPortalShortcut in hotkey.go.
	startPortal func(ctx context.Context, hk platform.Hotkey, onActivated func()) (portalHandle, error)
	// ensureGnomeBinding installs/refreshes the GNOME custom
	// keybinding running command; production wraps
	// gsettings.EnsureBinding with the real gsettings CLI runner.
	ensureGnomeBinding func(ctx context.Context, hk platform.Hotkey, command string) (gsettings.Applied, error)
	// mediaKeysDaemon reports whether gsd-media-keys owns its
	// session-bus name -- the daemon a GNOME keybinding is inert
	// without; production is gsettings.DaemonRunning. An error means
	// the check could not run (no session bus) and is skipped quietly.
	mediaKeysDaemon func(ctx context.Context) (bool, error)
	cursorInfo      func() (cx, cy int, ds []platform.Display, ok bool)
	moveWindow      func(x, y int) bool
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
	// (startup-notification id or activation token); best-effort, a
	// none-credential on timeout or unsupported backends.
	mintCredential func() launch.Credential
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
	appSource      appctx.Source
	// firefoxBases lists the Firefox profiles.ini base directories the
	// frequent-sites discovery probes; production is
	// firefox.DefaultBaseDirs (the real home), tests pin it.
	firefoxBases func() []string
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
		startHotkey:   native.StartHotkey,
		startPortal:   startPortalShortcut,
		ensureGnomeBinding: func(ctx context.Context, hk platform.Hotkey, command string) (gsettings.Applied, error) {
			return gsettings.EnsureBinding(ctx, gsettings.Run, hk, command)
		},
		mediaKeysDaemon: gsettings.DaemonRunning,
		cursorInfo:      native.CursorDisplays,
		moveWindow:      native.MoveWindow,
		lstat:           os.Lstat,
		open:            launcher.OpenEnv,
		reveal:          launcher.RevealEnv,
		run:             launcher.Run,
		launchExec:      launcher.Launch,
		resolveHandler:  native.ResolveHandler,
		handlerByID:     native.HandlerByDesktopID,
		mintCredential: func() launch.Credential {
			return native.MintLaunchCredential(launchMintTimeout)
		},
		prepareLaunch: native.PrepareLaunch,
		dbusLaunch: func(call launch.DBusCall) error {
			ctx, cancel := context.WithTimeout(context.Background(), launchDBusTimeout)
			defer cancel()
			return launch.DBusActivate(ctx, call)
		},
		watchState:     native.WatchState,
		snRemove:       native.RemoveStartupSequence,
		activateWindow: native.ActivateWindow,
		appSource:      native.AppSource(),
		firefoxBases:   firefox.DefaultBaseDirs,
	}
}

// runtimeCtx returns the Wails context, or nil before Startup.
func (a *App) runtimeCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctx
}

// emitEvent forwards an event to the frontend; a no-op before Startup
// (unit tests, early build progress), so emitting is always safe.
func (a *App) emitEvent(name string, payload ...interface{}) {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.emit(ctx, name, payload...)
}

// Hide hides the searchbar window. It is a no-op before Startup has
// run: calling Wails runtime functions without the runtime context
// would abort the process.
func (a *App) Hide() {
	a.mu.Lock()
	a.visible = false
	// A hide also cancels a show that has not executed yet (e.g. an
	// IPC hide racing a pre-DomReady summon): the ordered outcome is
	// hidden.
	a.pendingShow = false
	a.mu.Unlock()
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	a.rt.hide(ctx)
}

// toggle is the global hotkey and IPC "toggle" callback: hide when
// visible, summon onto the cursor's display when hidden. Presses
// within toggleGap of the last accepted one are dropped (key
// autorepeat). Before the frontend is ready (DomReady) the summon is
// deferred: DomReady executes it once the bar can render. On the
// summon path the app context is captured FIRST: showing the bar
// steals focus, so the focused app must be read before the window
// appears.
func (a *App) toggle() {
	a.mu.Lock()
	now := a.plat.now()
	if now.Sub(a.lastToggle) < toggleGap {
		a.mu.Unlock()
		return
	}
	a.lastToggle = now
	if !a.domReady {
		a.pendingShow = true
		a.mu.Unlock()
		return
	}
	visible := a.visible
	a.mu.Unlock()
	if visible {
		a.Hide()
	} else {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
}

// showIfHidden is the IPC "show" callback: show-only, never hides.
// Before the frontend is ready the show is deferred to DomReady; when
// the bar is already visible it is just re-shown (raised), without
// re-capturing app context or repositioning; when hidden it takes the
// same capture-context-then-show path toggle uses.
func (a *App) showIfHidden() {
	a.mu.Lock()
	if !a.domReady {
		a.pendingShow = true
		a.mu.Unlock()
		return
	}
	visible := a.visible
	ctx := a.ctx
	a.mu.Unlock()
	if visible {
		if ctx != nil {
			a.rt.show(ctx)
		}
		return
	}
	a.captureAppContext()
	a.showOnCursorDisplay()
}

// showOnCursorDisplay positions the bar on the display the cursor is
// on (falling back to centering when the platform cannot say), marks
// it visible, shows it, and tells the frontend so it can focus the
// input. A no-op before Startup. On a Wayland session the
// cursor-display positioning is skipped entirely: the app is a native
// Wayland client there, gtk_window_move and friends are silent no-ops,
// and the compositor owns placement -- centering is requested
// best-effort and the situation is logged once.
func (a *App) showOnCursorDisplay() {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	if a.session().Kind == platform.SessionWayland {
		a.waylandPlaceOnce.Do(func() {
			log.Printf("hotkey: wayland session: window placement is decided by the compositor")
		})
		a.rt.center(ctx)
	} else if !a.positionOnCursorDisplay(ctx) {
		a.rt.center(ctx)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()
	a.rt.show(ctx)
	a.emitEvent(eventShown)
}

// positionOnCursorDisplay implements the positioning flow; false means
// the caller should center instead. Wails' WindowSetPosition is
// relative to the monitor the window is CURRENTLY on (verified in the
// v2.13.0 sources, all platforms), so absolute target coordinates are
// translated against that monitor (platform.WailsPosition) on Linux
// and Windows -- WindowGetPosition IS absolute there -- while macOS
// moves the window natively (its Cocoa coordinate flip cannot be
// expressed as a translation).
func (a *App) positionOnCursorDisplay(ctx context.Context) bool {
	cx, cy, displays, ok := a.plat.cursorInfo()
	if !ok {
		return false
	}
	target, ok := platform.PickDisplay(displays, cx, cy)
	if !ok {
		return false
	}
	x, y := platform.BarPosition(target, WindowWidth, WindowHeight)
	if a.plat.goos == "darwin" {
		return a.plat.moveWindow(x, y)
	}
	wx, wy := a.rt.getPos(ctx)
	cur, ok := platform.DisplayForWindow(displays, wx, wy, WindowWidth, WindowHeight)
	if !ok {
		return false
	}
	rx, ry := platform.WailsPosition(a.plat.goos, cur, x, y)
	a.rt.setPos(ctx, rx, ry)
	return true
}
