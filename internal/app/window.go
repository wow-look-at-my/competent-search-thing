package app

import (
	"context"
	"log"
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

// toggleGap rate-limits the hotkey toggle: X11 and Windows both
// deliver key autorepeat while the combination is held, which would
// otherwise flicker the bar. The same window classifies a toggle that
// arrives just after a Hide as the dismiss press that caused that
// hide through a side channel (grab FocusOut -> frontend blur; see
// toggle) -- it comfortably covers the gsettings backend's process
// spawn + IPC latency while staying below deliberate
// dismiss-then-resummon typing speed.
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
	appSource         appctx.Source
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
	}
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
	a.lastHide = a.plat.now()
	// A hide also cancels a show that has not executed yet (e.g. an
	// IPC hide racing a pre-DomReady summon): the ordered outcome is
	// hidden. A latched summon-into-config dies with it -- and so does
	// an in-flight drag-resize anchor (the next drag re-latches).
	a.pendingShow = false
	a.pendingConfig = false
	a.dragActive = false
	a.dragDispOK = false
	a.dragPosOK = false
	a.mu.Unlock()
	// The stats sampler goes idle with the bar (a flag flip, no IO;
	// runs even pre-Startup, where it is a nil-safe no-op).
	a.statsVisible(false)
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
//
// A toggle that finds the bar hidden BUT hidden only within the last
// toggleGap is dropped, not re-summoned: pressing the combo on an
// open bar can hide it through a side channel before this callback
// even runs -- activating an X11 grab (the app's own XGrabKey, or
// gsd's for a GNOME media-keys binding) delivers FocusOut to the
// focused bar, the frontend's blur handler calls Hide, and on the
// gsettings backend the toggle then arrives a whole process spawn +
// IPC round-trip later ("<exe> toggle"). Branching on the visible
// flag alone turned exactly those dismiss presses into re-summons
// (the bar flickered and stayed open). Treating a just-hidden bar as
// "this press already dismissed it" makes the combo dismiss
// deterministic regardless of which side of the race this callback
// lands on; a summon later than toggleGap after an Esc/blur hide is
// untouched.
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
	justHidden := now.Sub(a.lastHide) < toggleGap
	a.mu.Unlock()
	if visible {
		a.Hide()
	} else if !justHidden {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
}

// startSpaceWatch arms the dismiss-on-Space-change behavior once, at
// Startup, on platforms with a Space concept (the seam is nil
// elsewhere). Decision (b) of the space-switch ghost fix: Spotlight
// itself dismisses on a Space switch, and the alternative -- fighting
// the compositor for key-window status during the transition
// animation -- is exactly the 1-frame ghost being fixed.
func (a *App) startSpaceWatch() {
	if a.plat.watchSpaceChanges == nil {
		return
	}
	if a.plat.watchSpaceChanges(a.spaceChanged) {
		log.Printf("panel: space-change dismiss armed")
	}
}

// spaceChanged is the active-Space-change callback: a visible bar is
// dismissed through the EXISTING Hide path -- stamping lastHide, so
// the toggle-gap dismiss semantics hold for a summon racing the
// switch -- while a hidden bar is left completely alone (in
// particular the pending-show latch stays untouched; only Hide on a
// VISIBLE bar may clear it).
func (a *App) spaceChanged() {
	a.mu.Lock()
	visible := a.visible
	a.mu.Unlock()
	if visible {
		a.Hide()
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
// input. A no-op before Startup. Every path first clamps the desired
// window size to the hosting display's usable area (clamp-to-screen,
// re-evaluated per summon so multi-monitor moves re-fit -- and
// re-grow -- the window; the config value itself is never touched).
// On a Wayland session the cursor-display positioning is skipped
// entirely: the app is a native Wayland client there, gtk_window_move
// and friends are silent no-ops, and the compositor owns placement --
// centering is requested best-effort and the situation is logged
// once; the clamp still applies, through the toolkit's own work-area
// probe (the one source Wayland has).
func (a *App) showOnCursorDisplay() {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return
	}
	if a.session().Kind == platform.SessionWayland {
		a.waylandPlaceOnce.Do(func() {
			log.Printf("hotkey: wayland session: window placement is decided by the compositor")
		})
		a.clampForFallbackShow(ctx)
		a.rt.center(ctx)
	} else if !a.positionOnCursorDisplay(ctx) {
		a.clampForFallbackShow(ctx)
		a.rt.center(ctx)
	}
	a.mu.Lock()
	a.visible = true
	a.mu.Unlock()
	// Wake the stats sampler BEFORE the window maps: the kick's
	// baseline sample is then already in flight while the show
	// happens, and every show path (hotkey toggle, IPC show, the
	// DomReady deferred show) funnels through here.
	a.statsVisible(true)
	a.rt.show(ctx)
	a.emitEvent(eventShown)
}

// clampForFallbackShow applies the clamp-to-screen rule on the show
// paths that have no picked display (the Wayland show and the
// center fallback): the toolkit work-area probe decides, and when
// even that is unavailable the size is left alone -- there is nothing
// to clamp against.
func (a *App) clampForFallbackShow(ctx context.Context) {
	if a.plat.windowWorkArea == nil {
		return
	}
	area, ok := a.plat.windowWorkArea()
	if !ok {
		return
	}
	w, h := a.clampWindowSize(area, true)
	a.applySizeIfChanged(ctx, w, h)
}

// positionOnCursorDisplay implements the positioning flow; false means
// the caller should center instead. The window size is the configured
// (desired) size clamped to the summon display's usable area, resized
// natively when that differs from what the window currently has --
// the clamp-to-screen rule, re-evaluated against the display the bar
// appears on. Wails' WindowSetPosition is
// relative to the monitor the window is CURRENTLY on (verified in the
// v2.13.0 sources, all platforms), so absolute target coordinates are
// translated against that monitor (platform.WailsPosition) on Linux
// and Windows -- WindowGetPosition IS absolute there -- while macOS
// moves the window natively (its Cocoa coordinate flip cannot be
// expressed as a translation). Successful placements are remembered
// (notePlacement) as the drag-resize anchor.
func (a *App) positionOnCursorDisplay(ctx context.Context) bool {
	cx, cy, displays, ok := a.plat.cursorInfo()
	if !ok {
		return false
	}
	target, ok := platform.PickDisplay(displays, cx, cy)
	if !ok {
		return false
	}
	winW, winH := a.clampWindowSize(target.UsableRect(), true)
	a.applySizeIfChanged(ctx, winW, winH)
	x, y := platform.BarPosition(target, winW, winH)
	if a.plat.goos == "darwin" {
		if !a.plat.moveWindow(x, y) {
			return false
		}
		a.notePlacement(x, y)
		return true
	}
	wx, wy := a.rt.getPos(ctx)
	cur, ok := platform.DisplayForWindow(displays, wx, wy, winW, winH)
	if !ok {
		return false
	}
	rx, ry := platform.WailsPosition(a.plat.goos, cur, x, y)
	a.rt.setPos(ctx, rx, ry)
	a.notePlacement(x, y)
	return true
}

// notePlacement records the last absolute top-left position the app
// gave the window -- the drag-resize anchor (reading the position
// back is not an option on darwin, and the app is the only thing
// that ever moves this frameless window).
func (a *App) notePlacement(x, y int) {
	a.mu.Lock()
	a.placedX, a.placedY = x, y
	a.placedOK = true
	a.mu.Unlock()
}
