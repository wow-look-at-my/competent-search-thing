# internal/platform/native

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/platform/native` -- the thin OS glue, DELIBERATELY NO test
files (go-toolchain skips coverage for packages without tests; the
code needs a live display server). Keep it minimal and defensive;
logic worth testing belongs in internal/platform. Per OS: linux =
pure-Go X11 via jezek/xgb (StartHotkey: XGrabKey on the root window
incl. CapsLock/NumLock variants + KeyPress loop; CursorDisplays:
QueryPointer + Xinerama with root-geometry fallback; no X server ->
error/ok=false, the app degrades). golang.design/x/hotkey is NOT
used on linux: its x11 init() PANICS the process when no display is
reachable (verified v0.6.1) -- do not "simplify" back to it. windows
= golang.design/x/hotkey (RegisterHotKey) + user32 syscalls
(GetCursorPos, EnumDisplayMonitors with a package-level
syscall.NewCallback, GetMonitorInfoW -> rcMonitor + rcWork).
darwin = Carbon RegisterEventHotKey via the Cocoa/Carbon shim
(hotkey_darwin.go + platform_darwin.h/.m: NO Accessibility/TCC
permission -- the old golang.design/x/hotkey CGEventTap path
errored without it and never prompted; registration hops to the
main thread through runOnMain, presses arrive via the Carbon event
dispatcher on the main run loop -- which [NSApp run] pumps -- and
are drained to onDown on a private goroutine; ONE hotkey slot, a
second concurrent StartHotkey errors, stop unregisters async so
shutdown never blocks on a stopping main loop) + the same shim's
cursor via CGEventCreate, screens via NSScreen with
bottom-left->top-left conversion, MoveWindow via setFrameOrigin on
the first NSWindow, all on the main thread, and ConfigurePanel
(panel_darwin.go over csConfigurePanel; panel_other.go = false on
!darwin): the Spotlight-style collectionBehavior canJoinAllSpaces
+ fullScreenAuxiliary + ignoresCycle plus hidesOnDeactivate NO on
the first NSWindow, false while no window exists yet -- and it
FIRST sets the Dock/Cmd-Tab icon once (dockIconOnce ->
tray.MagnifierRGBA(128) -> csSetDockIcon: NSBitmapImageRep over
premultiplied RGBA -> NSApp.applicationIconImage; the raw-binary
install ships no .app bundle/.icns, so a bare Mach-O would show the
generic icon while running). WatchSpaceChanges
(spacewatch_darwin.go + the !darwin always-false stub): the
NSWorkspaceActiveSpaceDidChangeNotification observer
(csObserveSpaceChanges, block-based, token retained forever under
MRC -- the shim compiles without ARC) feeding the csHotkeyFired
channel pattern (buffered(1), non-blocking send, one app-lifetime
drain goroutine) into the first caller's onChange -- the app's
dismiss-on-Space-change (window.go spaceChanged).
DisplayPowerInfo/WatchPowerChanges (powerinfo_darwin.go + the
!darwin stubs; the fps meter's probe): csPowerInfo reads NSScreen
maximumFramesPerSecond + NSProcessInfo lowPowerModeEnabled behind
@available(macOS 12) guards (older systems answer 0Hz/off) plus
thermalState, and csObservePowerChanges arms power+thermal change
observers on the csSpaceChanged channel pattern (csPowerChanged
export, buffered(1), forever-drain). WebViewUncapNear60
(webkit_darwin.go + stub -- the package's ONE -framework WebKit
link): csWebViewUncapNear60 walks windows[0].contentView.subviews
for the WKWebView (wails v2.13.0 adds it there) and flips
PreferPageRenderingUpdatesNear60FPSEnabled OFF through
respondsToSelector-guarded WKPreferences feature SPI (the unified
+_features list (macOS 13.3+) with -_setEnabled:forFeature:, then
the experimental/internalDebug splits; the SPI setters are
declared as a category so the (BOOL, id) calls compile), returning
the honest CS_UNCAP_* status -- a WebKit that drops the SPI
degrades to a status code, never a crash.
AppIconPNG (appicon_darwin.go + the !darwin nil stub; the icons
service's NativeAppIcon production seam): csAppIconPNG =
[NSWorkspace iconForFile:] -- the icon Launchpad/Finder/the Dock
show, Assets.car included -- drawn offscreen into an NSBitmapImageRep
(Copy op so the uninitialized buffer never shows through) and
returned as a malloc'd PNG the Go side copies+frees; deliberately
NO runOnMain hop (NSWorkspace lookup + NSImage drawing are
thread-safe, the caller is the ResolveIcons goroutine, and the
darwin unit-test binary pumps no main queue -- a dispatch_sync
there would deadlock), body under its own @autoreleasepool
(Go threads have none), missing paths refused (iconForFile answers
a generic icon for ANY string). The ONE shim entry point the mac CI
job actually EXERCISES (internal/icons real_darwin_test.go sweeps
the runner's real /Applications through it).
display_darwin.go also carries `#cgo LDFLAGS: -framework
UniformTypeIdentifiers` on Wails' behalf: the v2 darwin frontend
references UTType without linking that framework, and newer Xcode
SDKs fail the production-tag link with _OBJC_CLASS_$_UTType
undefined (first hit: the macos-latest runner's Xcode 26.5) -- do
not remove it just because no code in the package uses it.
Also per OS: `AppSource() appctx.Source` (appsource_*.go), the
app-context glue -- linux = EWMH over conn-per-call xgb
(_NET_ACTIVE_WINDOW / _NET_CLIENT_LIST -> per-window _NET_WM_PID,
WM_CLASS class for Name, _NET_WM_NAME falling back to WM_NAME for
Title, exe/comm via appctx.ProcInfo("/proc", pid); RunningApps
dedupes by pid keeping the first window's title, skips pid==0, caps
64, sorts by Name; InstalledApps = appctx.ScanDesktopDirs; no X ->
ok=false; OpenWindows (winlist_linux.go) = the same client-list
walk kept per-WINDOW: skips untitled windows + os.Getpid()'s own,
caps 100, no pid dedup -- and winlist_linux.go's ActivateWindow(id)
= EWMH _NET_ACTIVE_WINDOW ClientMessage to the root window (format
32, source indication 2 = pager, SubstructureRedirect|Notify mask)
now carrying a FRESH X server timestamp (launchwatch_linux.go's
serverTime: zero-length property-append on a scratch InputOnly
window + PropertyNotify, the gdk_x11_get_server_time trick; 0
fallback = old behavior) so it passes mutter's staleness gate and
may switch workspaces;
winlist_other.go (!linux) = OpenWindows not-ok + ActivateWindow
error, so the open-windows feature does not exist on
windows/darwin yet). LAUNCH GLUE (all linux-only, stubs in
launch_other.go): identity_linux.go = cgo init() g_set_prgname
("competent-search-thing") at import time, before wails builds the
window -- wails sets no prgname, so the window had NO WM_CLASS and
NO wayland app_id (the CI screenshot script matches by title +
geometry, unaffected); launchmint_linux.{go,h,c} = the GTK-thread
credential mint: runOnGTKThread (g_main_context_is_owner inline
check, else g_idle_add + cgo.Handle trampoline csRunOnGtk, bounded
wait, abandoned callbacks self-clean) -- also reused by
windowsize_linux.go's `SetWindowSize(w,h)` (windowsize_other.go =
always false off linux), the config editor's live window resize:
cs_set_window_size (launchmint_linux.c, cs_find_toplevel) runs
gtk_window_set_default_size THEN gtk_window_resize on the GTK
thread, because GTK3 pins a non-resizable window's hints to
min=max=MAX(default size, request) on every move-resize
(gtk-3-24 gtk_window_update_fixed_size), making the construction
default a permanent shrink floor for the Wails runtime's bare
gtk_window_resize; moving the default moves the floor (verified
end-to-end under Xvfb: 780x550 -> 900x600 -> 640x480) -- and by
`WindowWorkArea()` (same files; windowsize_other.go = always
false), the clamp-to-screen work-area probe: cs_get_workarea =
cs_find_toplevel -> gdk_display_get_monitor_at_window ->
gdk_monitor_get_workarea on the GTK thread -- the ONE source that
answers on Wayland (darwin/windows report work areas through
CursorDisplays' Work rects instead) -- and
cs_mint(desktop_id) --
the mint DESCRIBES the launch with a real GAppInfo (the resolved
handler's desktop entry, else a synthesized commandline appinfo
flagged SUPPORTS_STARTUP_NOTIFICATION): GLib >= 2.76 asserts
G_IS_APP_INFO and returns NULL for a NULL info (verified
empirically on 2.80; never pass NULL) -- (X11 = gdk
app-launch-context startup-notify id incl. the libsn "new:"
broadcast; Wayland = the same call (notify_launch uuid on 3.24.33,
real token on >= 3.24.35) falling back to a hand-rolled
xdg_activation_v1 token on a DEDICATED wl_event_queue via proxy
wrappers -- never dispatching gdk's queue -- authenticated by the
last wl_keyboard serial from our own listener (cs_prepare_wayland,
scheduled at Startup via PrepareLaunch; the keymap fd is closed
per event) and the toplevel's live wl_surface fetched AT MINT TIME
(gdk recreates wayland objects per hide/show; never cache));
xdg-activation-v1-client-protocol.h +
xdg-activation-v1-protocol_linux.c are COMMITTED wayland-scanner
output (provenance + regen command in their headers;
ASCII-sanitized copyright sign; the _linux.c suffix keeps them off
darwin builds); launchresolve_linux.go = thread-safe gio handler
resolution (ResolveHandler: content-type guess by file NAME or
inode/directory, or URI scheme; HandlerByDesktopID; both fill
launch.Handler incl. DBusActivatable/StartupNotify/StartupWMClass/
Terminal/Exec/Exe); launchwatch_linux.go = WatchState (stacking
client list -- _NET_CLIENT_LIST_STACKING falling back to
_NET_CLIENT_LIST, read via windowPropPresent because a
present-but-EMPTY list (the bar hides right after a launch; an
otherwise empty desktop is the NORMAL post-launch state) must poll
as zero windows, not as "no EWMH WM" -- + active window +
per-window pid/WM_CLASS
instance+class/_NET_STARTUP_ID/_NET_WM_USER_TIME, conn-per-call,
cap 100), internAtomAlways (only_if_exists=false -- scratch atoms
must be CREATED), scratchWindow, serverTime, and
RemoveStartupSequence = the libsn "remove:" broadcast (20-byte
format-8 ClientMessages to root, first chunk typed
_NET_STARTUP_INFO_BEGIN then _NET_STARTUP_INFO, PropertyChange
event mask, scratch sender window) reaping sequences
chromium-family launchees never complete; windows = GetForegroundWindow / EnumWindows (package-
level callback) + IsWindowVisible + GetWindowTextW +
GetWindowThreadProcessId + OpenProcess/QueryFullProcessImageNameW
(Name = exe base sans extension), InstalledApps = HKLM+HKCU
uninstall keys (native + WOW6432Node; DisplayName, skip
SystemComponent=1, Exec from a plausible-.exe DisplayIcon with the
",N" index stripped and spaces re-quoted in .desktop syntax);
darwin = NSWorkspace via the Cocoa shim (frontmostApplication /
runningApplications with regular activation policy; Title always
empty -- titles need the AX API), InstalledApps = /Applications +
~/Applications *.app scan (Exec = `open -a "<path>"`, Icon = the
absolute bundle path -- internal/icons' darwin ref shape).
windows/darwin files compile only on their OSes -- the CI `linux`
job builds linux/amd64 + a windows/amd64 cross-compile but only
ever RUNS the linux binary, and the `darwin` job cgo-compiles
darwin/arm64 + runs the unit-test suite on a mac runner (no GUI
run) -- so keep them boring and conventional.
