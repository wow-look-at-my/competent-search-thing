# internal/app -- the Wails-bound App object

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/app` -- the Wails-bound App object and its methods
(Search/Open/Reveal/Hide/GetTheme/GetCustomCSS/Startup/DomReady/
Shutdown/QueryPlugins/RunPluginAction/CheatSheet/GetHistory/
AddHistory/GetStats/ResolveIcons/GetFileIcons/RecordPick/
FPSEnabled/RecordFPSSample/SetupWatch). Bound methods
appear in JS as `window.go.app.App.<Method>`. Holds the `index.Manager`; `Startup`
saves the runtime ctx, brings up the global hotkey once through a
session-dependent backend plan (hotkey.go: empty spec = skip, parse
failure = log once + run on; `hotkeyPlan(session, override)` picks
x11 session -> [x11], wayland+GNOME -> [portal, gsettings], wayland
other -> [portal, manual], unknown session (headless CI, windows,
darwin) -> [x11]; the `COMPETENT_SEARCH_HOTKEY_BACKEND` env var
(auto/x11/portal/gsettings/none, case-insensitive) forces exactly
one backend -- none = nothing, IPC still summons -- and an unknown
value warns once and acts as auto. The x11 backend is the
pre-Wayland native path, behavior-identical (plat.startHotkey with
toggle, "hotkey: %s summons the searchbar"); portal+gsettings run
sequentially on ONE goroutine (portal Register can block minutes on
the interactive approval) under a hotkeyCtx cancelled in Shutdown:
portal success stores the handle + logs the bound trigger,
ErrNoPortal/ErrNoGlobalShortcuts logs one line and falls through,
ErrDenied STOPS the chain (never write a keybinding after the user
said no), the gsettings backend refuses an empty executable-seam
path, filepath.Abs-resolves a relative one (gsd runs the command
with its own cwd/PATH), then prefers the STABLE spelling of that
path via platform.StableExecutable(exe, args0-seam) -- resolved
os.Executable dies with versioned symlinked installs (Homebrew
Cellar/Nix/stow) on every upgrade, so the PATH-shim, the structural
Homebrew mapping (brewpath.go: the Cellar path taken apart into the
linked <prefix>/<rest> then opt fallback -- needs no PATH/argv[0]
cooperation, which the gsd-boot context lacks), or the argv[0]
symlink wins whenever it is proven (os.SameFile) to be the running
binary, logged once when it differs -- calls
gsettings.EnsureBinding(hotkeyCtx, run, hk,
gsettings.ToggleCommand(exe)), logs ONE loud repair line ("hotkey:
repaired the GNOME keybinding command: <old> -> <new> ...") when
Applied.Repaired reports the self-heal, then logs one evidence line
quoting the read-back disk state ("hotkey: GNOME keybinding entry
<path>: binding <b>, command <c>, in custom-keybindings list: <v>")
followed by EXACTLY ONE loud summary that is HONEST: the "hotkey:
GNOME keybinding active: <accel>" / "(requested <accel> is taken by
GNOME; using fallback)" / "hotkey: using existing GNOME keybinding
<accel> (edit in GNOME Settings > Keyboard)" wordings fire ONLY
when Applied.Verified (read-back confirmed list membership +
binding + command) AND the mediaKeysDaemon seam (production
gsettings.DaemonRunning; probe errors = no session bus = skip
silently) sees org.gnome.SettingsDaemon.MediaKeys owned; otherwise
the one summary is a WARNING naming what is missing (VerifyNote /
daemon absent) plus the manual-fix instructions, and a.hotkeyDesc
stays empty (never advertise a summon key that cannot fire). A plan
that runs dry logs the manual
bind-a-key-to-'competent-search-thing toggle' instructions. The
effective summon description (hk.String(), the portal's
bound-trigger description, or the verified installed accelerator)
is stored on the App (a.hotkeyDesc, read via hotkeyDescription() --
EMPTY unless a summon path actually registered, and consumed by the
tray tooltip), starts the tray icon once (tray.go in this package:
linux-only goos gate -- windows/darwin get nothing for now --
Options.TrayDisabled = config tray.enabled=false logs "tray: disabled in
config" and skips; otherwise the `newTray` builder seam (production
buildTray = tray.New over trayOptions()) yields the handle and ONE
goroutine runs Start under a ctx cancelled in Shutdown -- the tray
package degrades quietly by itself, nothing on the startup path
waits for the bus, and the menu REUSES app behavior: Show/Hide +
icon activation -> the same toggle path the hotkey uses (pending-
show deferral included), Rescan now -> requestRescan (the !rescan
behavior minus the bar-hide; still-building = friendly logged
error), Open config -> showConfig (the !config behavior: launch
the settings-window process; the
config FILE stays reachable via the OpenConfigFile bound method),
Quit -> runBuiltin("quit"); the tooltip getter wraps
hotkeyDescription(), so no shortcut is promised until one is
proven), arms the darwin dismiss-on-Space-change once (spaceOnce ->
startSpaceWatch in window.go over the plat.watchSpaceChanges seam --
nil off-darwin via defaultSpaceWatch, the defaultProcTree pattern;
production native.WatchSpaceChanges -- and the spaceChanged callback
runs the EXISTING Hide path only when the bar is visible, so
lastHide is stamped and toggle-gap dismiss semantics hold, while a
hidden bar keeps its pending-show latch untouched; decision (b) of
the space-switch ghost fix: Spotlight itself dismisses on a Space
switch), starts the system-stats sampler once (stats.go in this
package: the `newStats` builder seam -- production buildStats does a
fresh config.Load (translucent.go pattern), stats.enabled=false = one
"stats: disabled in config" log + nil, else sysstats.New wired with
OnUpdate = emitStats (the guarded "stats:update" emit) and
log.Printf -- and a non-nil sampler is Start()ed under a dedicated
ctx cancelled in Shutdown; the sampler idles until the bar first
shows, so startup cost is zero and newTestApp-stubbed apps spawn
nothing), kicks the automatic login-service registration once
(service.go in this package: the `newService` builder seam --
production buildService = service.NewManager, nil + one log line on
construction failure -- gated on COMPETENT_SEARCH_NO_SERVICE
(plat.getenv; both CI smoke scripts set it so runners never write
units) and goos linux/darwin, then ONE goroutine runs
service.Ensure under svcCancel (cancelled in Shutdown) and
logServiceOutcome prints at most ONE honest line -- loud
registered/repaired (the gsettings old->new precedent), informative
yield (owner + optional brew stop-once hint + our-leftover-file
hint)/opt-out/unavailable, SILENT current/unsupported; newTestApp
stubs the seam nil, and the decision matrix itself lives in
internal/service ensure.go, see its CLAUDE.md entry), runs the dev-only
fps hooks once (fps.go:
COMPETENT_SEARCH_FPS=1 through the plat.getenv seam -- the bound
FPSEnabled gates the whole frontend meter loop (false = the
frontend registers NOTHING), RecordFPSSample re-validates every
echoed summary (the RecordPick defense-in-depth stance: finite
0..1000 rates, 0..100 pct, 100..60000ms window, bounded frames/Hz;
meter off = silent no-op) and logs ONE inline-metrics line
("fps: 59.8 avg, 118.9 max, 2% frames >20ms over 5.0s (rAF
~120Hz)"), and startFPSInfo logs the display/power context line
("fps: meter on; display 120Hz max, lowPowerMode=off,
thermalState=nominal") over the plat.powerInfo seam (production
native.DisplayPowerInfo, darwin only, nil elsewhere -- honest
"unavailable" wording then) plus arms plat.watchPowerChanges
(native.WatchPowerChanges) so every Low-Power-Mode/thermal flip
logs a "fps: power state changed:" line) -- and, independent of
the meter, applies the WebKit near-60 uncap (fps.go
applyNear60Uncap over the plat.uncapNear60 seam, production
native.WebViewUncapNear60, darwin only: flips
PreferPageRenderingUpdatesNear60FPSEnabled OFF through guarded
WKPreferences SPI so ProMotion panels render at their real refresh
rate -- and LPM's halving lands on 60, not 30, there; attempted at
Startup (pre-first-render when possible), retried at DomReady
where the FINAL outcome logs once (transient no-window/no-webview
misses stay quiet on the early attempt); uncapDone (mu) latches
across attempts; COMPETENT_SEARCH_KEEP_NEAR60=1 is the escape
hatch, default ON per the never-below-60 ruling),
builds the icon resolver once (icons.go in this package:
the `newIcons` builder seam, production buildIcons =
icons.NewService over Options{NativeAppIcon: native.AppIconPNG} --
the OS-rendering darwin fallback wired unconditionally, the
!darwin stub answers nil -- plus, when faviconProfileDir() resolves
a Firefox profile (config profileDir overrides first, open-tabs
then frequent-sites via a fresh config.Load -- the translucent.go
pattern -- else FindProfile over plat.firefoxBases),
Options.FaviconLookup = a firefox.FaviconReader over that profile's
favicons.sqlite bounded by the app-lifetime firefox ctx; no
profile = no offline favicon tier, quietly. Zero IO at build, the
first
Resolve pays initialization -- behind the bound
`ResolveIcons(keys, size) map[string]string`, which the frontend
calls with batched per-render icon keys; nil resolver (newTestApp)
= empty non-nil maps, and resolution runs on the bound method's own
goroutine so the query path never waits on icon IO. noteFavicon
(same file) feeds browser-reported favicon locations into the
resolver through the OPTIONAL faviconNoter interface assertion
(the `prioritized` extension pattern -- plain test fakes need not
implement it), nil-safe and IO-free), wires the
single-instance IPC handlers when Options.IPC is set (Toggle =
toggle, Show = showIfHidden, Hide = Hide, Config = showConfig,
Quit = quitViaIPC -- the version-skew handshake's graceful half:
the same runtime-quit path as the !quit builtin, the
no-runtime-ctx guard logged instead of surfaced; Options.
ShowOnStartup
latches a pending show) -- wired FIRST in Startup, before
registerHotkey, which can block briefly on darwin's Cocoa main-loop
race: the handlers are pre-init-safe (summons latch pendingShow
until DomReady, Hide no-ops without a runtime ctx), so an IPC
summon during registration is acked instead of answered "err not
ready" -- brings the plugin
layer up once (plugins.go: an appctx.Cache over the plat.appSource
seam + RefreshInstalledAsync, then the registry via the
`newRegistry` builder seam, whose production value `buildRegistry`
re-reads config.json, LoadDirs <configDir>/plugins, passes Version,
the installedApps getter and openWindowsGetter() -- the
session-gated OpenWindows seam: x11 = the openWindows adapter
(uint32 ids -> decimal strings), wayland = nil + ONE
openWindowsLogOnce log line (NEVER probe X there: an XWayland
client list is misleadingly partial), unknown = the adapter only if
a synchronous source probe can actually list (headless CI/windows/
darwin cannot) -- and the Firefox getters (firefox.go:
`firefoxSources(cfg)` resolves BOTH sections -- frequent sites and
open tabs -- around ONE shared discovery: a section's config
profileDir override wins for that section, the override-less ones
share a single firefox.FindProfile pass over the `plat.firefoxBases`
seam (production firefox.DefaultBaseDirs); discovery finding nothing
= ONE quiet "firefox: no profile found; the Firefox result sections
are disabled" line + nil getters, so those builtin providers never
register; otherwise a firefox.Cache (sites) and firefox.TabCache
(tabs) whose refresh goroutines are bounded by the app-lifetime
firefoxCtx -- created on first use under pluginMu, SHARED across
registry reloads so !reload builds fresh caches with fresh config
but can never leak an unbounded refresh, cancelled in Shutdown and
left cancelled afterwards), and logs every registry Errors()
entry once with a "plugin:" prefix -- missing plugins dir =
builtins only, no noise), brings the Firefox tab-switching bridge
up once (ffext.go in this package: the `newFfext` builder seam over
the ffextBridge interface -- production buildFfext gates on
firefox.FindProfile over plat.firefoxBases (no profile = one quiet
log + nil), then installFfextHost writes/self-heals the
native-messaging host pieces (ffext.InstallHost over
platform.StableExecutable(exe, args0) + the plat.userHome seam +
config.Dir(); Repaired = ONE loud old->new wrapper-command line,
the gsettings precedent; any failure = log + run on), then
ffext.Listen on ffext.SocketPath(plat.getenv) -- listen failure =
log + nil, the Options.IPC degrade twin. The handle is APP-LIFETIME
under ffextMu: registry reloads never own or restart it (a reload
must not sever the extension's connection), Shutdown closes it
beside the IPC server, and newTestApp stubs the seam nil so no
test ever creates a socket or probes the real home. liveTabs()
converts the bridge snapshot to plugin.TabInfo rows carrying
ffext.Token(conn,tab,window) -- served only when Connected() AND
fresh within ffextTabTTL (15s, the sessionstore TTL twin), rows
http(s)-filtered like the sessionstore reader, fresh-but-empty
still wins, and each kept row's bridge-reported FavIconURL is fed
to noteFavicon (the icons.go hint side-channel); the openTabs
getter (firefox.go) prefers it and falls
back to the TabCache byte-identically otherwise -- noting each
fallback row's sessionstore image attribute the same way.
activateTab()
routes one activation through the bridge -- no bridge/no conn =
ffext.ErrNotConnected after the ffextInactiveOnce quiet heads-up,
other failures log per occurrence -- and RunPluginAction's
activate_tab case falls back to Open(url) on ANY of it),
starts theme hot reload (theme.go: a
dedicated fsnotify watcher on the config dir + its themes/ subdir,
events debounced 300ms into "theme:changed"; any failure = log +
run on without live reload), builds the startup progress printer
once (progress.go in this package: the `newProgress` builder seam,
production buildProgress = progress.New(os.Stderr,
progress.IsTerminal(os.Stderr), log.Printf); a TTY printer renders
the "indexing..." line in place AND intercepts the standard logger
-- installProgressLog does log.SetOutput(printer), restored to
stderr as Shutdown's last step -- while non-TTY means throttled log
lines; a nil seam degrades to an inert io.Discard printer, and
buildIndex runs the same progressOnce so direct-call tests get the
printer too), and kicks the initial disk walk in a
goroutine (under a cancellable context) whose ticks render through
the printer (Done clears the line before the completion/error
logs); buildIndex FIRST arms the live-watch backend
(startEarlyWatch in watch.go: newWatchLayer -- the construction
shared with startWatch -- then watch.StartDeferred, stored in
a.earlyWatcher + ONE "watch: backend %s armed before the initial
index build ..." line, suppressed for the "none" backend; failure
= one log line + the old watch-after-build ordering) so changes
landing during the walk are queued instead of lost -- the
cancel/failure paths Stop-and-detach it (takeEarlyWatcher; Shutdown
and restartIndexLayer's in-flight branch do the same, the latter
because the early watcher runs the PREVIOUS config), never leaking
its marks; the BuildFromDisk window runs under a lowered GOGC
(gcbound.go: boundBuildGC over the plat.setGCPercent seam,
production debug.SetGCPercent, buildGCPercent 40, restored
immediately after BuildFromDisk returns on every path -- walk churn
otherwise doubles the peak heap at GOGC=100, and nothing else
bounds it on darwin where the cgroup GOMEMLIMIT guard is inert; a
percentage composes with any external GOMEMLIMIT, which is why
SetGCPercent was chosen over a derived byte limit); when the walk
finishes,
`startWatch` brings up the `watch.Watcher` + `watch.Rescanner` +
`watch.Sweeper` trio honoring the Options watcher knobs
(WatchMaxWatches, WatchExcludes -> a second watch-only Excluder,
WatchBackend -> watch.Options.Backend, SweepInterval,
SweepDisabled = no Sweeper + one loud warning; see the
internal/watch entry) -- ADOPTING the armed pre-build watcher when
one exists (wire the trio around it, then watch.Release: the fill
runs against the just-swapped index and the held events apply;
no early watcher = the old New+Start path) -- then announces the
effective backend ONCE:
`watchBackendFor(st.Backend)` builds the "watch:backend" payload
{backend "fanotify"|"fsevents"|"inotify"|"kqueue"|"windows"|
"none", full bool (fanotify AND fsevents), hint string (empty when
full; the pinned hintPartialWatch (linux/windows per-dir) /
hintPartialWatchDarwin (kqueue -> points at fsevents, not setcap) /
hintWatchOff (generalized: "the configured backend is required but
unavailable") texts otherwise, hintWatchFailed when the watcher
itself failed to start -> backend forced to "none")}, and when NOT
full `logFanotifyGrant()` first logs -- once per App, linux only
(plat.goos), the grant lines BEFORE the emit (tests synchronize on
the recorded event, then read the log) -- "watch: enable
full-filesystem watching by running 'competent-search-thing
setup-watch' (or manually: sudo setcap
cap_sys_admin,cap_dac_read_search+ep <path>)" -- this is the manual
fallback hint that fires when the automatic internal/watchsetup path
did not enable fanotify (declined, disabled, or unsupported); the
path is through
platform.ResolvedExecutable (the REAL file: setcap refuses
symlinks, and the brew bin/ shim IS one -- the field failure; only
when resolution fails does it fall back to the old
platform.StableExecutable(exe, args0) spelling), followed by ONE
persistence-caveat line ("file capabilities stick to that exact
file -- re-run the setcap command after any upgrade that replaces
the binary (e.g. brew upgrade)") -- deliberately the OPPOSITE
path preference from hotkey.go's keybinding command, which must
survive upgrades -- and ONE secure-exec tradeoff line (file caps
set AT_SECURE: GOTRACEBACK forced to none + non-dumpable, so
caps-on crashes report one line; ambient caps are the verified
full-visibility alternative -- issue #58 "secure-exec facts",
README "Crash-visibility tradeoff" carries the capsh command);
after
startWatch returns (it waits for the watcher's initial
registration), buildIndex logs ONE "index: startup complete: N
entries in D, R ram" summary -- after watch establishment, so the
elapsed covers build + watch setup; never on the error/cancel
paths;
`Shutdown` (wired to Wails OnShutdown) closes the IPC server first
(when present) and the ffext bridge beside it (shutdownFfext: the
other owned listener; unlinks its socket, the host relay just
retries until the next launch), releases the hotkey (native stop func, cancel of
the async portal/gsettings chain, idempotent+nil-safe close of the
active portal handle -- a handle the chain stores after Shutdown
ran is closed by the chain itself), closes the tray (cancels a
Start still waiting on the bus, then the nil-safe idempotent
Close), cancels the stats sampler's goroutines (statsCancel +
detach; nothing else to close), cancels the in-flight plugin
generation + Close()s the registry + cancels the firefox refresh
context (an in-flight places.sqlite copy/query aborts between
chunks), cancels a still-running
initial build (its walk aborts promptly, logs "index: initial
build cancelled", discards the partial store, and never starts the
watch layer), and stops rescanner+sweeper+watcher (in that order;
sweeper nil-tolerated when disabled) plus a still-armed pre-build
early watcher (idempotent with the build goroutine's own stop) and
the theme watcher
cleanly -- every step bounded, so quit never waits out a disk
walk. Summons that arrive before
the frontend can render are deferred: `DomReady` (wired to Wails
OnDomReady) first applies the Spotlight-style panel collection
behavior exactly once (panelOnce over the plat.configurePanel seam,
production native.ConfigurePanel, darwin-only effect -- DomReady is
the earliest point every platform has a native window, and it
precedes the pending show), then executes at most ONE pending show
(ShowOnStartup or an
early hotkey/IPC toggle/show; Hide cancels the pending flag), and
after DomReady summons act immediately. `showIfHidden` is the IPC
show handler: visible = plain re-WindowShow (no capture, no
reposition), hidden = the same capture+position+show path toggle
uses. GetTheme re-loads config.json
(the theme field is consumed live, plus window.translucent for the
darwin tuning below) and returns theme.Resolve's
token map -- errors are logged once per distinct message and fall
back to dark -- then tuneDarwinTranslucent (theme.go) substitutes
bg-opacity "0.65" ONLY when goos==darwin AND translucent AND the
resolved value still equals a BUILTIN default
(builtinDefaultBgOpacity: dark's 0.97 OR light's 0.98 -- light
overrides the token, so comparing dark alone misses it; both read
opaque over the NSVisualEffectView and defeat the frosted look):
any user-customized bg-opacity passes through
untouched (a user value equal to a builtin default is
indistinguishable and tunes too), and every other platform/flag
combination is
byte-identical (linux has no compositor blur -- lower alpha there
would put text over desktop noise; dark.json itself is untouched,
the style.css :root block being sync_test-locked to it);
GetCustomCSS returns <configDir>/themes/custom.css
verbatim when <= 64KB (the unvalidated escape hatch), else "". The
hotkey callback `toggle` (rate-limited 250ms against key
autorepeat) hides the bar when visible; a toggle finding the bar
hidden but hidden within the last toggleGap (lastHide, stamped by
every Hide) is DROPPED, not re-summoned -- pressing the combo on an
OPEN bar can hide it through a side channel before the callback
runs (grab activation delivers FocusOut to the focused bar ->
frontend blur handler -> Hide; on the gsettings backend the toggle
then arrives a "<exe> toggle" process spawn + IPC later), and
branching on the visible flag alone turned exactly those dismiss
presses into re-summons, so the combo could never dismiss there;
when hidden beyond that window it FIRST
captures app context (`captureAppContext`: CaptureFocused +
RefreshRunningAsync + RefreshWindowsAsync +
EnsureFreshInstalled(5m) + kickFfextRefresh (the nil-safe async
live-tab list refresh, so the bridge snapshot is warm by first
keystroke) + the async frecency cwd derivation
(captureFrecencyCwd in frecency.go: focused PID -> the
plat.procTree per-capture snapshot factory (production
appctx.NewProcTree("/proc"), linux only, nil elsewhere) ->
frecency.DeriveCwd on a goroutine -> setFrecencyCwd swaps a FRESH
immutable Blend copy into the Manager; skipped without a factory, a
store, or a positive CwdWeight; no focused PID or no meaningful cwd
CLEARS the boost rather than leaving it stale) -- the bar window
steals focus, so this precedes showing), then
`showOnCursorDisplay`: platform.CursorDisplays -> PickDisplay ->
CLAMP-TO-SCREEN (the desired winW/winH limited to the picked
display's UsableRect + the 320x240 floors via platform.ClampSize,
re-evaluated EVERY summon so multi-monitor moves re-fit and
re-grow; applySizeIfChanged in size.go dedupes against the
appliedW/H tracker -- seeded from the construction size in New --
so a fitting size issues zero native calls; the DESIRED winW/winH
is never clamped away, a hand-set 5000 stays for a bigger monitor)
-> BarPosition (absolute coords, the CLAMPED size), then darwin =
native.MoveWindow,
linux/windows = translate via DisplayForWindow + WailsPosition (Wails
WindowSetPosition is RELATIVE to the window's current monitor -- and
to the WORK AREA origin on Windows -- while WindowGetPosition is
absolute; verified in the v2.13.0 sources), successful placements
recorded via notePlacement (placedX/Y, the drag-resize anchor --
darwin cannot read positions back), any failure ->
clampForFallbackShow (the plat.windowWorkArea probe) + WindowCenter;
then WindowShow + "app:shown". EXCEPTION: on a detected Wayland
session (platform.DetectSession via the detectSession seam, cached
once per process) the whole cursor-display flow is skipped --
Wails is a native Wayland client there and gtk_window_move /
keep-above are silent no-ops, the compositor owns placement -- so
the show path is clampForFallbackShow (plat.windowWorkArea =
native.WindowWorkArea, cs_get_workarea: gdk_monitor_get_workarea on
the GTK thread, the ONE clamp source Wayland has) + WindowCenter
(best-effort) + WindowShow, with a
once-per-run "placement is decided by the compositor" log; the
X11/unknown path is untouched (CI's Xvfb has DISPLAY set and no
XDG_SESSION_TYPE, which detects as x11). DRAG-EDGE RESIZING
(resize.go): the bound `ResizeDrag(w, h)` (per animation frame) +
`ResizeCommit(w, h)` (once, on release) implement the frontend's
edge drags -- dragAnchor latches per drag (the hosting display +
the anchored top y from placedX/Y, rt.getPos as the linux/windows
fallback; Hide and the commit clear it), every frame clamps to the
display UsableRect + floors (anchored-top growth additionally stops
at the area bottom), horizontal resizes recenter about the
display's horizontal center (moveTo, placement-deduped; skipped on
Wayland -- compositor placement -- and when the position is
unknown), and ONLY the commit persists: config.Load -> the dragged
size into window.width/height (base) or preview.windowWidth/Height
(pane mounted -- the dragged size describes the CURRENT layout) ->
lastSavedSum recorded BEFORE the atomic Save (self-write
suppression) -> the cfgCurrent baseline patched in place (no
applier pass -- the size is already live; base-mode commits also
update resultsW); a failed Load skips persistence rather than
rewriting a file it could not read. maxDragDimension (32767)
bounds frontend-echoed values. `QueryPlugins(query string, gen
int) plugin.TargetInfo` stores gen (atomic), cancels the previous
generation's context (aborting plugin subprocesses/HTTP/debounces;
empty query or nil registry = cancel only, zero TargetInfo),
converts the appctx Snapshot to the plugin wire types, and
dispatches; providers answer async via "plugin:results" events
whose emit path drops stale generations. `CheatSheet()
plugin.Emission` returns the registry's bang cheat sheet (see
internal/plugin) under the same pluginMu the reload swap uses --
synchronous, dispatch-free, nil registry = zero Emission, Results
always non-nil so JS sees results: []. `GetHistory() []string` /
`AddHistory(entry string)` (history.go) wrap the internal/history
store Startup builds once: <configDir>/history.json, persist =
!Options.HistoryPersistDisabled (main.go wires config's
the inverse of history.persistEnabled there, like TrayDisabled); an
unresolvable
config dir or a failed Load logs once with a "history: " prefix
and the app runs on -- nil store = GetHistory returns a non-nil
empty slice and AddHistory no-ops, so newTestApp needs no extra
wiring. The frontend commits a query only after its activation
actually ran. Frecency wiring (frecency.go): Startup's
startFrecency (skipped when Options.Frecency.Disabled -- main.go
wires config search.frecency) builds the open-count store
(<configDir>/frecency.json, persist on; unresolvable config dir =
memory-only + one log line; Load runs ASYNC, corrupt = one log
line + empty store) plus the recency probe OVER THE plat.lstat
SEAM (tests never stat the real disk) and hands the Manager the
index.Blend; `recordOpen(path)` is the ONE capture hook -- called
from the success paths of Open and Reveal ONLY, which covers the
open_path plugin action (it executes through Open), while open_url
values sharing Open are filtered by its absolute-path guard, and
openConfigFile bypasses Open deliberately -- recording async, write
errors logged once (frecErrOnce), never blocking or failing the
action. Pick-memory priors wiring (priors.go in this package):
Startup's startPriors (config search.priors, ON by default -- the
tray.enabled convention; search.priors.enabled=false is a debug escape
hatch: no store, no file reads, no goroutines) builds the
internal/priors Store, installs store.PriorFunc as frecBlend.Prior
(riding the SAME blend the cwd stash re-swaps, so the resolver
survives those swaps; with frecency disabled the Manager gets a
prior-only Blend, which the engine still activates), and rebuilds
the tables asynchronously -- once at Startup and after every
successful Open/Reveal (kickPriorsRefresh beside recordOpen:
single-flight + one pending re-run coalescing bursts, no timers,
priorsClosed stops re-arms during Shutdown's priorsWG drain beside
frecWG) -- by reading <configDir>/telemetry.jsonl(.1) oldest-first
plus frecency.json for the bootstrap; read errors log once
(priorsErrOnce) and degrade to whatever parsed, and ONE startup log
line reports the table sizes. Ranking-log wiring
(telemetry.go in this package): Startup's startTelemetry (config
search.telemetry, ALWAYS ON -- deliberately no off switch, the log
is private by staying on the machine; the unresolvable-config-dir
degrade (one log line + nil layer) is the only off path) builds
the telemetryLayer -- an internal/telemetry Store
at <configDir>/telemetry.jsonl plus an 8-slot query->signals
impression ring; Search routes through queryWithTelemetry (nil
layer = exactly Manager.Query; normally Manager.QueryTraced
capturing index.ResultSignals + a ring stash keyed by the TRIMMED
query, blendActive from Blend.Active()); `RecordPick(rep
telemetry.PickReport) error` is the frontend's activation-success
report (called beside AddHistory): nil layer or blank query =
silent no-op, everything echoed back RE-validated
(telemetry.ValidatePickReport -- the RunPluginAction defense-in-
depth stance), file-row features joined EXCLUSIVELY from the ring
(the report carries row identities plus plugin-row titles -- the
one display field only the frontend knows -- so the frontend can
never forge signal values; a missing ring entry = Joined false,
features zero), and the append runs async (telWG + telErrOnce, the
recordOpen pattern) with Shutdown draining telWG beside frecWG;
search.telemetry.maxSizeKB (the section's only knob) hot-applies
through applyTelemetry (the sectionAppliers row in
configapply.go).
Learned-arbitration wiring (arbiter.go in this package): Startup's
startArbiter (config search.arbiter, ON by default -- the
tray.enabled convention; search.arbiter.enabled=false is the debug
escape hatch / kill switch: no store, no file reads, no
goroutines, emissions untouched) builds the arbiterLayer -- an internal/arbiter Store plus its OWN
8-slot query->ResultSignals impression ring -- and applies the
model at BOTH composition seams: (1) frecBlend.Model =
arbBlendModel (the startPriors riding pattern; the resolver
answers nil per query until the activation gate passes, pinned
byte-identical) converts each merged candidate's signals to an
arbiter.Row and returns the clamped FileDelta; (2) QueryPlugins'
emit closure routes every emission through arbitrateEmission --
inactive = the emission returned UNTOUCHED (same rows, same
Priority); active = rows stable-re-ordered within the section by
model score and a priority-0 section promoted to Priority 1 when
its best row outscores bestFileScore over the ring's stashed
impression for the same trimmed query (no stash = placement
untouched) -- all before the one emit, so the frontend still
paints each section once (no new bridge calls; the frontend's
existing priority>0 zone + identity reconcile need no changes).
queryWithTelemetry stashes into the arbiter ring only while a
gate-passed model is installed (activeArbLayer; inactive =
today's exact query path). Training runs async (the priors
single-flight pattern: arbBusy/arbAgain/arbClosed + arbWG drained
in Shutdown after telWG): refreshArbiterNow reads
telemetry.jsonl(.1) oldest-first via arbiter.ReadLogFile (read
errors log once, arbErrOnce), arbiter.Train applies the gate, and
the outcome swaps into the store -- kicked at Startup/apply and by
noteArbiterPick after every arbiter.RetrainEvery (50)
SUCCESSFULLY APPENDED picks (counted in RecordPick's append
goroutine after the record hit disk); the gate verdict logs on
the first run (arbLogOnce) and on every activation flip
(arbActive). Config changes hot-apply through applyArbiter (the
applyPriors shape: disable detaches Model + swaps the layer out
live, enable rebuilds + kicks a training run, unresolvable config
dir = reported apply error).
`GetStats() sysstats.Snapshot` (stats.go) returns the
sampler's cached snapshot -- instant, never IO on this path -- with
Enabled stamped true (the sampler itself never sets that field;
emitStats stamps the event payloads the same way); nil sampler
(disabled, pre-Startup, post-Shutdown) = zero Snapshot, Enabled
false = the frontend hides the #stats row entirely, while Enabled
true with per-metric OK=false renders dashes. Bar
visibility drives the sampler through nil-safe statsVisible:
showOnCursorDisplay -- the ONE shared show helper every summon path
funnels through (hotkey toggle, IPC showIfHidden's hidden branch,
the DomReady deferred show) -- calls SetVisible(true) right before
WindowShow (the kick's baseline sample is in flight while the
window maps), and Hide() calls SetVisible(false); both are flag
flips + a non-blocking kick, never IO. `RunPluginAction(pluginID
string, action plugin.Action) error` RE-validates every action the
frontend echoes back (defense in depth), logs it, then executes:
copy_text -> ClipboardSetText (bar stays open); open_path (abs
path only) and open_url (http/https + host only) -> the open seam;
run_command (1..16 non-empty <=1024-byte argv; a non-empty
DesktopID must be a bare *.desktop file name per
launch.ValidDesktopID) -> runCommandAction (launch.go): with a
DesktopID on linux it resolves handlerByID and takes the
credentialed path -- dbus Activate for DBusActivatable apps (what
focuses an already-running app), else the validated argv through
the run seam WITH the credential env -- plus watcher + launch log;
without one, byte-identical old behavior (run seam, detached, nil
env); run_builtin -> rescan (Rescanner.Request;
friendly error while the index is still building) / reload
(newRegistry, swap under mutex, Close the old) / config
(showConfig: open the settings window, bar stays up) /
version (copy `Version`, stays open) / quit
(runtime Quit); activate_window (parseWindowID: non-empty base-10
uint32) -> the activateWindow seam (production
native.ActivateWindow); activate_tab (ffext.ParseToken on the
internal-only Tab field -- strict c<conn>:<tab>:<window> digits --
PLUS validHTTPURL on Value, the fallback URL) -> activateTab
through the bridge, Hide on success, and on ANY bridge failure
(not connected, timeout, tab gone) the case returns a.Open(Value)
-- the pick never surfaces an error when the fallback works;
everything else hides the bar on success.
CONFIG EDITOR (configui.go + configapply.go + configwindow.go):
settings live in their OWN PROCESS with an ordinary window --
Wails v2 gives one window per process and this one is a
hide-on-blur always-on-top panel, so an editor mode of it could be
lost behind other windows (the reported bug). `showConfig()` (IPC
"config", the !config builtin, the tray item) therefore SPAWNS
`<platform.StableExecutable> config` through the plat.run seam and
leaves the bar alone; configwindow.go is the other end
(Options.ConfigWindow: GetStartupMode answers "config", the
frontend wires the editor alone, Startup runs only
startConfigWindow -- config baseline + schema sidecar + theme/
config watcher + IPC handlers whose summons raiseConfigWindow (a
PLAIN show: an ordinary window the user placed must never be
repositioned or clamped) -- and CloseConfigWindow quits the
process, refusing to run in the searchbar). Nothing talks to the
searchbar: the editor saves config.json and the running app's own
config watcher hot-applies it, which is also why the settings
window works with no app running. applyConfig short-circuits in
this process (nothing live to apply) after storing cfgCurrent.
Bound
methods: `GetConfigSchema()` (the embedded
schemas.ConfigSchemaJSON), `GetConfigForEdit()` (fresh
Load+Normalize as indented JSON + config path + LoadWarning +
config.UnknownKeys of the RAW file -- keys a GUI save would drop,
"$schema" included), `SaveConfig(raw)` (strict
DisallowUnknownFields decode with line-numbered error messages ->
force on-disk rootsVersion (a GUI save must never re-trigger the
Load migrations) -> Normalize -> atomic config.Save -> record
sha256 of the saved bytes (lastSavedSum, self-write suppression) ->
applyConfig(next, "gui-save")), and `OpenConfigFile()` (the file
escape hatch, wraps openConfigFile). The LIVE-APPLY ENGINE
(configapply.go): `applyConfig(next, origin)` diffs old->new per
section over the `sectionAppliers` table (cfgCurrent baseline,
seeded by Startup's startConfigState fresh Load, swapped per pass;
nil baseline = apply-all, appliers are idempotent; whole passes
serialized by applyMu and skipped once shuttingDown), runs each
changed row's apply plus each named GROUP once per pass, and
returns ApplyResult{Applied, Pending, Errors, NextLaunch}. The
table is TOTAL -- Pending stays empty; every section applies live:
maxResults (Manager.SetMaxResults), search.fuzzyEnabled
(Manager.SetFuzzyDisabled + registry), theme (existing
GetTheme/watcher machinery), plugins/bangs/rewrites/firefox (the
groupRegistry reloadRegistry), roots/excludes/watcher/
rescanIntervalMinutes (groupIndexLayer = restartIndexLayer in
watch.go: Manager.SetRoots/SetExcludes + swap the live watchConfig
(seeded from Options in New, consumed by startWatch) + stop the
trio in Shutdown's order + startWatch + one background
Rescanner.Request so the index converges while queries keep
serving; an in-flight initial build just stores the values and
arms rescanOnWatchUp (startWatch requests the rescan at watch-up),
a FAILED initial build (buildFinished + trio down) is revived with
a fresh buildIndex), hotkey (applyHotkey in hotkey.go:
teardownHotkey -- shared with Shutdown, bumps the hkGen generation
so a stale async chain discards instead of storing over the
replacement -- then startHotkeyBackends(spec, force=true); force
reaches the gsettings backend as
gsettings.EnsureBindingWith(BindingOptions{ForceBinding}), the ONE
path allowed to rewrite the sticky GNOME accelerator (Applied
gains Rebound/PreviousBinding/RebindSkipped; all-taken keeps the
working binding with an honest notice, never an error); empty spec
= release only), search.frecency (applyFrecencyConfig: rebuild
store+blend over the SAME frecency.json, disabled = SetBlend of a
Prior-only blend when priors ride it else nil -- frecBlend.Prior is
PRESERVED across both rebuild paths, so a live frecency change can
never drop an enabled priors layer), search.priors (applyPriors:
the teardown-plus-rebuild shape over the priors store + blend
resolver;
disable detaches Prior and re-installs the blend only if it stays
Active, enable rebuilds the layer and kicks a table build, an
unresolvable config dir is a reported apply error),
search.telemetry (applyTelemetry: rebuild the always-on layer at
the incoming maxSizeKB -- the impression ring restarts empty,
in-flight appends drain via telWG, and an unresolvable config dir
is a reported apply error, unlike startTelemetry's quiet degrade),
search.arbiter (applyArbiter: the applyPriors shape over the
arbiter layer + blend Model resolver; disable detaches Model and
re-installs the blend only if it stays Active, enable rebuilds the
layer and kicks a training run),
tray/stats (teardown + rebuild through startTrayIcon/startStats;
disabling stats emits one Enabled-false snapshot so the row
hides), history (fresh store at the new persist flag: disk seed +
in-memory replay preserves recall), preview (applyPreview: live
previewCfg swap under previewMu + dispatcher rebuild;
GetPreviewConfig answers from live state), window.width/height
(groupWindowSize = applyWindowSize, fed by the window AND preview
rows: stores the live DESIRED winW/winH/resultsW the positioning
math and GetPreviewConfig read, then applies the desired size
CLAMPED to the current display (currentDisplayArea: the display
list via cursorInfo + DisplayForWindow/PickDisplay -- never probed
on Wayland -- else the plat.windowWorkArea toolkit probe; the
preview-mount growth on a small screen renders clamped, the
reported field bug) through applySizeIfChanged -- dedup against
appliedW/H, then plat.setWindowSize --
production native.SetWindowSize, linux-only GTK-thread
gtk_window_set_default_size + gtk_window_resize, because for a
DisableResize window GTK3 pins min=max to MAX(default size,
request) (gtk-3-24 gtk_window_update_fixed_size) so the Wails
runtime's bare gtk_window_resize can never shrink below the boot
size -- falling back to the rt.setSize runtime call, sufficient on
darwin/windows). The ONE ruled next-launch knob:
window.translucent (construction-time RGBA visual) is reported by
name in NextLaunch (ApplyResult/SaveResult/config:changed) with
one honest log line -- never a generic restart mechanism, and no
other knob may join it without an explicit ruling. External
config.json edits hot-apply through the
THEME watcher (theme.go: a debounce batch touching cfgPath also
runs handleConfigFileChange -- skip when the file's sha256 equals
lastSavedSum (our own save), else fresh Load -> applyConfig
(origin=external-edit) -> emit "config:changed"; Load failure =
log + emit with the error, previous config stays applied). Events
emitted (all guarded so a nil ctx no-ops): "index:progress"
{indexed,done,seconds}, "watch:degraded"
{watched,dropped,overflows}, "watch:backend" {backend,full,hint}
(once, from startWatch; see above), "app:shown", "theme:changed" (no
payload; frontend refetches GetTheme/GetCustomCSS),
"config:changed" (payload
{applied,pending,nextLaunch,error} -- an external edit hot-applied
or failed to load; nextLaunch lists only the ruled
window.translucent),
"plugin:results" (payload plugin.Emission
{plugin,name,gen,results,priority} -- priority omitempty,
section-ORDERING metadata: the frontend renders every section
above the file results (files last), priority ordering sections
among themselves), "stats:update" (payload
sysstats.Snapshot {enabled,cpuPct,cpuOk,gpuPct,gpuOk,memUsed,
memTotal,memOk,swapUsed,swapTotal,swapOk,netRxBps,netTxBps,netOk};
enabled always true on the event -- it only ever fires from a live
sampler). ALL Wails
runtime calls and platform hooks sit behind seam structs
(`runtimeSeams` incl. clipboardSetText/quit and `platformSeams`
incl. run/activateWindow/configurePanel/watchSpaceChanges/appSource plus getenv/lookPath/executable/args0/detectSession/
startPortal/ensureGnomeBinding/procTree/userHome AND the launch
seams --
open/reveal/run take extraEnv now (reveal also startupID),
launchExec, resolveHandler, handlerByID, mintCredential,
prepareLaunch, dbusLaunch, watchState, snRemove -- in window.go;
defaults in New, plus
the `newRegistry`, `newTray`, `newStats`, `newProgress`,
`newIcons` and `newFfext` seams);
unit tests MUST
replace them (see
newTestApp, which also nils appSource, procTree AND
watchSpaceChanges (non-nil on the darwin CI job's production
seams), stubs
newRegistry, newTray, newStats, newIcons, newFfext
AND newProgress (an inert non-TTY io.Discard printer), and pins
userHome to an error so no config,
X11, session-bus, /proc//sys, ~/.mozilla or global-log-output IO
happens, pins goos to
"linux" -- identical launch-path behavior on the darwin CI job;
tests exercising other OSes set goos themselves -- pins getenv to
"" (no DISPLAY = raise watcher off) and detectSession to
the unknown session -- keeping every test on the native
hotkey/positioning path unless it overrides detectSession -- makes
startPortal/ensureGnomeBinding recording fakes, and stubs the
launch seams: the handler never resolves, the mint yields none,
prepareLaunch stays deliberately silent; launch_test.go overrides
members per test) -- real
runtime funcs abort the process without a Wails context. Open/Reveal
run the CREDENTIALED LAUNCH PATH (launch.go) and hide the bar on
success: linux-only (launchEnabled gates on goos; macOS/Windows
keep the plain launcher call), ordering per launch = resolve the
handler (resolveHandler seam; reveal resolves Target{IsDir:true} =
the file MANAGER) -> mint a credential while the bar still holds
focus (mintCredential seam, gated by launch.ShouldMint -- no mint
for handlers with neither StartupNotify nor DBusActivatable) ->
watcherBefore snapshot (only when getenv DISPLAY != "" and the
watchState seam works) -> transport cascade (dispatchOpen: dbus
org.freedesktop.Application Open via launch.ApplicationDBusCall +
dbusLaunch seam, then the handler's own launch.ExpandExec argv via
launchExec seam unless Terminal, then the open seam = xdg-open
candidates; every tier carries launch.CredentialEnv, every
fall-through is logged) -> armRaiseWatcher (launch.RunWatcher
goroutine bounded by the app-lifetime launchCtx, cancelled in
Shutdown and left cancelled; when it ends -- and immediately on a
failed dispatch, or after launchReapDelay when no watcher could
arm -- endStartupSequence reaps an x11-sn sequence via the
snRemove seam) -> ONE log line launch.LogLine ("launch: <verb>
<target> handler=... credential=<kind>:<id8> transport=... watcher=
on|off"). openConfigFile routes through openTarget too. Startup
runs announceLaunch once (linux only): prepareLaunch seam (native
Wayland serial listener) + "launch: activation credentials enabled
(session=<kind>)". Blank targets skip straight to the launcher's
own validation. `app.Result` is a type alias of `index.Result`
(the JSON tags path/name/isDir plus the optional hint live in
internal/index). Search with an absolute-path query and ZERO index
results may return ONE synthetic hint result (hint.go): the path
must Clean to abs, exist on disk via the `lstat` platform seam
(production os.Lstat; newTestApp pins it to not-exist), and lie
OUTSIDE every configured root (pathWithinAny, ported isWithin
semantics) -- then Result{path, base, IsDir, Hint: "outside indexed
roots -- add <top dir> to roots in config.json"} with <top dir> the
first path component; inside-roots existing paths stay hint-free
(indexing gap, not scope gap), and the frontend renders the hint in
the dim parent-dir slot. Startup also logs each
Options.ConfigNotes line once with a "config:" prefix (the roots
migration notes wired from cfg.MigrationNotes in main.go). The app
`Version` constant lives in plugins.go. Unit-tested.
