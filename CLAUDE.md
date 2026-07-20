# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

## Architecture map

- `main.go` -- glue only: embeds `frontend/dist` (go:embed) and calls
  cli.Execute(app.Version, runGUI); runGUI configures the window
  (frameless, always-on-top, start-hidden, hide-on-close,
  non-resizable, sized by app.PreviewWindowSize() (internal/app
  previewsize.go: fresh config.Load, same standalone-read pattern as
  translucent.go; preview off or any config error = the configured
  base size window.width/height -- the app.WindowSize() read,
  internal/app size.go; Load repairs zero/too-small values to the
  780x550 defaults / 320x240 floors even on error -- while
  preview.enabled widens to preview.windowWidth/Height) -- the SAME
  two values are wired into app Options WindowWidth/WindowHeight so
  the positioning math always matches the native window; zero
  Options fall back to the defaults via the unexported
  App.windowSize(), which keeps newTestApp wiring-free), binds the
  App object and wires OnStartup / OnDomReady /
  OnShutdown. When app.WindowTranslucent() (internal/app
  translucent.go: fresh config.Load, window.translucent, any error =
  false) reports true, runGUI adds BackgroundColour = zero RGBA
  (alpha 0) + Linux{WindowIsTranslucent: true, WebviewGpuPolicy:
  Never} for the per-pixel-alpha window; the GPU policy MUST stay
  pinned to Never -- wails' nil-Linux default (#2977 workaround)
  lives only in the nil branch, so an unpinned non-nil Linux block
  silently flips it to OnDemand -- plus wailsOpts.Mac =
  app.MacWindowOptions() (internal/app macwindow.go: fresh
  config.Load, nil on flag-off/any error; flag-on =
  {WindowIsTranslucent (the NSVisualEffectView BehindWindow frosted
  glass -- wails v2.13.0 has NO raw setOpaque:NO passthrough, and
  Spotlight is vibrancy anyway), WebviewIsTransparent
  (drawsBackground=NO), Appearance tracking the theme: the light
  builtin -> NSAppearanceNameVibrantLight, everything else -> the
  "NSAppearanceNameVibrantDark" literal (wails ships no VibrantDark
  constant; AppearanceType is a plain string passed verbatim to
  [NSAppearance appearanceNamed:])}; the pure decision half
  macWindowOptionsFor is headless-tested, and options.Mac is read
  ONLY by the darwin frontend so linux behavior cannot change) --
  and with the flag off all three fields
  stay nil, byte-identical to the pre-flag call (CI screenshots run
  flag-off). runGUI also wires RunOptions.OpenConfig ->
  app Options.OpenConfigOnStartup (the CLI config subcommand's
  start-into-editor path). Zero-arg invocation boots the GUI exactly
  as before the
  CLI existed (CI screenshots rely on that). Deliberately has NO test
  file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/GetTheme/GetCustomCSS/Startup/DomReady/
  Shutdown/QueryPlugins/RunPluginAction/CheatSheet/GetHistory/
  AddHistory/GetStats/ResolveIcons/RecordPick/FPSEnabled/
  RecordFPSSample). Bound methods
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
  Options.TrayDisabled = config tray.disabled logs "tray: disabled in
  config" and skips; otherwise the `newTray` builder seam (production
  buildTray = tray.New over trayOptions()) yields the handle and ONE
  goroutine runs Start under a ctx cancelled in Shutdown -- the tray
  package degrades quietly by itself, nothing on the startup path
  waits for the bus, and the menu REUSES app behavior: Show/Hide +
  icon activation -> the same toggle path the hotkey uses (pending-
  show deferral included), Rescan now -> requestRescan (the !rescan
  behavior minus the bar-hide; still-building = friendly logged
  error), Open config -> showConfig (the !config behavior: summon
  into the in-app config editor, pre-DomReady deferral included; the
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
  fresh config.Load (translucent.go pattern), stats.disabled = one
  "stats: disabled in config" log + nil, else sysstats.New wired with
  OnUpdate = emitStats (the guarded "stats:update" emit) and
  log.Printf -- and a non-nil sampler is Start()ed under a dedicated
  ctx cancelled in Shutdown; the sampler idles until the bar first
  shows, so startup cost is zero and newTestApp-stubbed apps spawn
  nothing), runs the dev-only fps hooks once (fps.go:
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
  icons.NewService over its defaults -- zero IO at build, the first
  Resolve pays initialization -- behind the bound
  `ResolveIcons(keys, size) map[string]string`, which the frontend
  calls with batched per-render icon keys; nil resolver (newTestApp)
  = empty non-nil maps, and resolution runs on the bound method's own
  goroutine so the query path never waits on icon IO), wires the
  single-instance IPC handlers when Options.IPC is set (Toggle =
  toggle, Show = showIfHidden, Hide = Hide; Options.ShowOnStartup
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
  still wins; the openTabs getter (firefox.go) prefers it and falls
  back to the TabCache byte-identically otherwise. activateTab()
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
  internal/watch bullet) -- ADOPTING the armed pre-build watcher when
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
  full-filesystem watching with: sudo setcap
  cap_sys_admin,cap_dac_read_search+ep <path>" with the path through
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
  history.persistDisabled there, like TrayDisabled); an unresolvable
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
  tray.disabled convention; search.priors.disabled is a debug escape
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
  tray.disabled convention; search.arbiter.disabled is the debug
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
  (showConfig: summon into the in-app config editor, bar stays up) /
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
  CONFIG EDITOR (configui.go + configapply.go): `showConfig()` is the
  one summon-into-editor path (IPC "config", the !config builtin, the
  tray item, Options.OpenConfigOnStartup -- which latches
  pendingShow+pendingConfig at Startup): pre-DomReady = latch both
  (Hide cancels both; DomReady runs the show then emits
  "config:open"), hidden = the capture+show path then "config:open",
  visible = "config:open" only -- the mode event ALWAYS follows
  "app:shown" (the frontend's app:shown handler re-renders). Bound
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
  maxResults (Manager.SetMaxResults), search.fuzzyDisabled
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
  payload; frontend refetches GetTheme/GetCustomCSS), "config:open"
  (no payload; enter config-editor mode), "config:changed" (payload
  {applied,pending,nextLaunch,error} -- an external edit hot-applied
  or failed to load; nextLaunch lists only the ruled
  window.translucent),
  "plugin:results" (payload plugin.Emission
  {plugin,name,gen,results,priority} -- priority omitempty, > 0 =
  the frontend's above-files zone), "stats:update" (payload
  sysstats.Snapshot {enabled,cpuPct,cpuOk,gpuPct,gpuOk,memUsed,
  memTotal,memOk,swapUsed,swapTotal,swapOk,netRxBps,netTxBps,netOk};
  enabled always true on the event -- it only ever fires from a live
  sampler). ALL Wails
  runtime calls and platform hooks sit behind seam structs
  (`runtimeSeams` incl. clipboardSetText/quit and `platformSeams`
  incl. run/activateWindow/configurePanel/watchSpaceChanges/appSource plus getenv/executable/args0/detectSession/
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
- `internal/cli` -- the cobra command line, the real process entry
  point (main.go calls cli.Execute(app.Version, runGUI)). Bare
  invocation = the GUI path: ipc.Listen on ipc.SocketPath(os.Getenv);
  ErrAlreadyRunning = Send "show" to the running instance and report
  HONESTLY (showRunningInstance): Reply.OK -> "already running;
  showing it" + exit 0, Reply.NotReady() -> "already running (still
  starting up)" + exit 0, no/garbage reply -> stderr "already running
  but did not respond: ..." + exit 1 (cobra's own error line
  suppressed, garbage quoted from Reply.Raw); any other listen error
  = log + run the GUI with a
  NIL server (degraded, no IPC -- the app must still work). toggle /
  show send their command to the running instance; when none runs
  they start the GUI in this process with ShowOnStartup=true (on an
  ErrAlreadyRunning race they fall back to Send "show"); a NotReady()
  reply counts as success but prints a one-line "still
  starting up; it may take a moment to respond" notice (the instance
  is booting and shows itself). hide never starts the app: not
  running = plain notice on
  stderr + exit 1 (cobra error/usage output suppressed). config
  (config.go) opens the in-app config editor: Send CmdConfig to the
  running instance (OK -> "opening the config editor in the running
  instance" exit 0; NotReady() -> the still-starting notice exit 0;
  Reply.UnknownCommand() -- a running JSON daemon predating the
  config command -- -> "older version without the config command;
  restart it" on stderr, exit 1, cobra error line suppressed); not
  running -> start the GUI in-process with RunOptions{Server,
  ShowOnStartup: true, OpenConfig: true} (ErrAlreadyRunning race ->
  Send CmdConfig again). firefox-host (firefoxhost.go) is the
  native-messaging relay Firefox spawns through the generated
  wrapper: it never boots the GUI and never touches the
  single-instance socket -- it only runs ffext.RunHost over its
  stdio and ffext.SocketPath(os.Getenv), tolerates and ignores
  Firefox's positional args (manifest path, extension id), exits
  cleanly on stdin EOF, and keeps STDOUT strictly for protocol
  frames (cobra out is os.Stdout, so diagnostics go to the stderr
  logger, NEVER cmd.OutOrStdout(); env.hostIn/hostOut inject the
  stdio in tests, which drive a full relay round-trip against an
  in-process ffext.Server). The CLI
  branches ONLY on ipc.Reply fields (checkReply/summonReply/
  configReply); ALL
  wire parsing lives in ipc.Send -- a non-JSON reply (e.g. a
  still-running pre-JSON daemon's raw line) arrives in-band via Raw
  and lands in the "unexpected reply" error path. Convention:
  ONE self-registering subcommand per file (init -> registerCommand);
  newRoot() consumes the builder registry so Execute -- and every
  test -- gets a fresh command tree. RunOptions{Server,
  ShowOnStartup, OpenConfig} is the runGUI contract (main.go wires
  OpenConfig to app Options.OpenConfigOnStartup); the App takes
  ownership of
  the server (Shutdown closes it). Unit-tested headlessly: fake
  runGUI, real ipc servers on temp sockets, COMPETENT_SEARCH_SOCKET
  (t.Setenv) isolation.
- `internal/ipc` -- the single-instance unix-socket IPC layer, pure
  and headless-tested. SocketPath: $COMPETENT_SEARCH_SOCKET override,
  else $XDG_RUNTIME_DIR/competent-search-thing.sock, else a per-uid
  name under os.TempDir(). ONE request per conn (2s conn deadline,
  4 KiB line cap), one newline-terminated JSON object each way --
  JSON is the ONLY wire shape (the legacy v1 line protocol is
  DELETED): request {"cmd":"toggle|show|hide|config|version|ping"}
  with unknown JSON fields IGNORED on both sides (the documented
  tolerance contract), response {"ok":true} (ping) /
  {"ok":true,"version":v} / {"ok":true,"accepted":cmd} /
  {"ok":false,"error":"not ready"|"unknown command"|
  "invalid request"}; a request line that does not parse as JSON --
  incl. the old protocol's bare command words and arbitrary garbage
  -- earns "invalid request" and runs nothing (pinned by
  TestNonJSONRequestsAreRejected). Commands answer "not ready"
  until SetHandlers wires the app (nil handler members stay not
  ready; version/ping always answer), and the ack is
  written BEFORE the toggle/show/hide/config handler runs -- ack =
  accepted,
  not completed, so an app whose main thread is briefly stalled
  (startup indexing) can never time the client out; the handler then
  runs on the same conn goroutine, so Close still waits for in-flight
  handlers. The config command (config_cmd_test.go) rides handlerFor
  exactly like the others, so only its own JSON shapes are pinned.
  Send(path, cmd, timeout) speaks JSON and returns a parsed
  Reply{OK, Accepted, Version, Err, Raw} + NotReady() +
  UnknownCommand() (an older JSON daemon rejecting a newer command;
  the CLI's version-skew message branches on it) -- ALL
  reply parsing lives here, callers branch on fields, never wire
  strings. A reply line that does not parse as JSON -- incl. every
  pre-JSON daemon reply line -- comes back in-band (Raw set, OK
  false, empty Err) for callers to quote as "unexpected reply", the
  restart-the-old-instance-once upgrade signal
  (TestSendReturnsOldDaemonReplyInBand pins one conn, no retry);
  only transport failures are errors, dial
  failures wrapped in ErrNotRunning (test with IsNotRunning); timeout
  is ONE absolute deadline across dial + exchange. Listen
  recovers stale sockets:
  EADDRINUSE -> 500ms probe dial; an answer = ErrAlreadyRunning, a
  dead socket = os.Remove + retry ONCE; after listening the file is
  chmodded 0600 (filesystem perms are the only auth). Close is
  idempotent + nil-safe: stops the accept loop, unlinks the socket,
  waits for in-flight conns. Handlers run on conn
  goroutines and must be goroutine-safe. Deliberately NO schema in
  schemas/ (an internal two-party protocol, like history.json).
- `internal/ffext` -- the Firefox companion-extension bridge, pure
  and headless-tested (real temp sockets, scripted fakes, pipe-driven
  stdio): the pure half of switch-to-tab (webextension/ is the
  extension, internal/app ffext.go the wiring, internal/cli
  firefox-host the relay entry). Topology: extension <->
  native-messaging frames <-> host process <-> JSON lines on a SECOND
  unix socket <-> the app's bridge Server; the host only reframes
  bytes, both hops carry ONE message shape (requests
  {id,type:listTabs|activate,tabId,windowId}; replies {id,ok,tabs?|
  error}; unsolicited pushes {type:tabsChanged,tabs} -- unknown
  fields ignored, the ipc tolerance contract). Constants HostName
  "competent_search_thing" (Firefox's ^\w+(\.\w+)*$ rule),
  ExtensionID (the pinned gecko id), ProtocolVersion, Msg* -- all
  LOCKSTEP with webextension/logic.mjs via sync_test.go (the theme
  sync_test precedent; it also pins manifest.json's permissions
  exactly [nativeMessaging, tabs], MV2 persistent background page,
  and that the wrapper names the firefox-host subcommand).
  frame.go: ReadFrame/WriteFrame -- 4-byte NATIVE-endian length
  prefix (binary.NativeEndian per MDN) + JSON body, single-Write
  frames, caps MaxOutFrame 1 MB (Firefox kills the port beyond it)
  / MaxInFrame 8 MiB, torn stream = ErrUnexpectedEOF, clean end =
  io.EOF. SocketPath: $COMPETENT_SEARCH_FFEXT_SOCKET override, else
  $XDG_RUNTIME_DIR/competent-search-thing-ffext.sock, else per-uid
  under os.TempDir() (the ipc.SocketPath mirror). token.go:
  Token/ParseToken "c<conn>:<tab>:<window>" -- digits ONLY (strconv
  alone would take a leading sign), conn >= 1, 64-byte cap; the
  activate_tab wire token. manifest.go: ManifestPath per OS (linux+
  unix-likes ~/.mozilla/native-messaging-hosts/, darwin ~/Library/
  Application Support/Mozilla/NativeMessagingHosts/, windows =
  configDir + HKCU registry via registry_windows.go, stub elsewhere),
  WrapperPath/WrapperContent (configDir/firefox-host.{sh,bat};
  sh single-quote escaping, exec "<stable exe>" firefox-host "$@"),
  ManifestContent (name/description/path/type stdio/
  allowed_extensions=[ExtensionID]), InstallHost = read-compare-
  atomic-write both pieces (config.Save temp+rename shape, wrapper
  0700) with self-heal: unchanged = zero writes, changed wrapper
  reports PreviousExe for the app's loud repair log (the gsettings
  precedent). server.go: Listen = the ipc stale-socket recovery +
  chmod 0600 + ErrAlreadyRunning, but conns are PERSISTENT (8 MiB
  line cap): per-conn pending map correlates replies by id --
  listTabs replies store their dump ON THE CONN GOROUTINE (pendingReq
  .isList) so snapshot updates are strictly arrival-ordered vs
  tabsChanged pushes (a requester-side store could clobber a newer
  push); Tabs() = the merged per-conn snapshot (Tab tagged with the
  owning conn id, negative wire ids skipped, float lastAccessed
  tolerated) + newest update time (the app's freshness gate);
  KickRefresh = single-flight async listTabs fan-out (also fired
  once per fresh conn); Activate routes to the owning conn
  (ErrNotConnected when gone) under activateTimeout 1200ms /
  listTimeout 1000ms / writeTimeout 2s; dead conns drop their tabs
  and close their pending channels; Close idempotent+nil-safe.
  host.go: RunHost, the relay loop -- stdin frames -> socket lines
  (json.Compact when a frame carries newlines, drop-with-log while
  the app is down, one log per episode), socket lines -> stdout
  frames, reconnect with capped exponential backoff (1s..30s,
  seam-shrinkable), stdin EOF = clean nil return (Firefox closed the
  port), shutdownConn refuses a dial that lands after teardown.
  Deliberately NO schema in schemas/ (internal two-party protocol,
  the ipc stance).
- `internal/match` -- THE shared matching engine, pure (stdlib only),
  consumed by internal/index AND internal/plugin: ONE fold definition
  (FoldTable/FoldRune/FoldPattern, the per-string ASCII+rune helpers;
  index re-exports them under the old names), ONE tier ladder
  (TierTriggered > TierExact > TierPrefix > TierWordStart >
  TierSubstring > TierFuzzy > TierNone; MatchTerm per term+target,
  word = letter/digit runs), ONE multi-term semantics (Terms =
  strings.Fields + per-term fold; MatchFields = every term must match
  ANY of the ordered fields, candidate tier = worst per-term best
  tier, WorstField = worst best-field index), ONE position-aware
  scorer (score.go: the fzf-v2 constants/bonuses, DPState.Align =
  DP<=MaxDPUnits else greedy, PrepareASCII/PrepareFold fill
  units+bonus, TermScore one-shot, NormalizeScore for band scaling),
  ONE per-character position implementation (positions.go: Range =
  [2]int half-open RUNE pairs on the DISPLAY string; Positions =
  union over terms of the tier-earning occurrence -- prefix start /
  word-start occurrence / first substring / AlignPositions = the
  backpointered DP recovering the optimal fuzzy alignment, greedy
  past the bound; computed only for selected rows, never in scans),
  and ONE ranking mint (rank.go: Candidate{Display, Texts, TieBreak,
  SortKey, Hint, Payload} deliberately has NO score/position fields;
  Ranked's fields are unexported with no constructor so only Rank can
  mint; canonical wire bands triggered 86..100 (86+0.14*hint), exact
  83, prefix 73, word-start 63, substring 53, ScoreListed 50, fuzzy
  16..46+nudge, hint = external self-score demoted to a +/-2
  intra-tier nudge; modes: PreRanked = keep order, 100-i floored at
  86; Claimed = triggered tier, hint-ordered, source order on ties;
  Targeted+no-terms = list all at 50; default = MatchFields gate +
  sort tier/WorstField/score/TieBreak/foldedDisplay/Display/SortKey +
  cap + Positions). Exhaustively unit-tested (fold parity vs
  strings.ToLower, the fire/fox/firefox repro at engine level,
  AlignPositions score==Align cross-check on randomized inputs).
- `internal/index` -- the index engine. `Store`: compact
  column-oriented data (interned parent-dir table; ONE original-case
  name blob with 0x00 separators and one offset table -- deliberately
  no lowercased twin of the names or the dir table, case-insensitivity
  is folded in at scan time; tombstone removals). fold.go keeps the
  BLOB scan machinery (ciScan/ciIndexASCII + the static
  name-frequency anchor table) while the FOLD DEFINITION lives in
  internal/match and is re-exported under the historical names:
  foldPattern picks the regime per query --
  all-ASCII queries fold byte-wise (foldTable, 'A'-'Z' only) and scan
  the blob with ciIndexASCII (rarest-byte anchor via a static
  name-frequency table, bytes.IndexByte over both case variants,
  fold-verify around each candidate); queries with non-ASCII runes
  fold per rune with unicode.ToLower (decodeRuneAt, foldPrefixLen,
  foldContains, foldHasSuffix -- generic over []byte|string) on a
  per-entry slow path, O(hay*pat), correct but linear (hundreds of ms
  at tens of millions). SEMANTICS (pinned in fold_test.go): ASCII
  queries against ASCII data are byte-identical to the old
  strings.ToLower behavior; the two runes whose simple lowercase IS
  ASCII (U+0130 dotted I -> i, U+212A Kelvin -> k) are NO LONGER
  matched by plain-ASCII queries (the fast path never decodes stored
  UTF-8), while queries containing them still match both forms;
  invalid UTF-8 still compares as U+FFFD per byte. The naive test
  reference models share the fold definition (foldPattern + the
  testFold helper) but keep independent stdlib-strings matching.
  `Store.Query`: case-insensitive substring
  search, sharded across NumCPU goroutines with per-shard bounded
  top-K heaps; ranking exact > prefix > substring > fuzzy, dirs before
  files, shorter then numeric-aware lexicographic paths (aligned
  digit runs DESC -- numorder.go, below). QueryWith dispatches by
  match.Terms: whitespace-only = nil, ONE term = the pre-multi engine
  byte-identical (a padded query behaves as its trimmed term), 2+
  terms = multiterm.go: ALL terms must match the name order-free;
  classSub when every term substring-matches, classFuzzy when all
  match with >=1 subsequence-only term (never exact/prefix; score =
  summed per-term alignment); the ASCII fast path is a DRIVER-term
  scan (driver = term whose rarest byte has the fewest blob
  occurrences by exact histogram, any zero-count term = nil fast
  reject; phase A = the anchored substring scan for the driver
  fully judging candidates against the rest -- substring first,
  subsequence fallback -- marking every visited entry in the pooled
  bitset; phase B = the rarest-byte sweep for driver-subsequence-only
  entries, skipping marks, itself skipped when the phase-A classSub
  total fills the limit / fuzzy off / single-unit driver); any
  non-ASCII term = the sharded per-entry slow path
  (queryMultiFold). Every returned Result carries MatchRanges
  (half-open RUNE ranges on Name, [][2]int json matchRanges,
  computed POST-selection via match.Positions; path mode = the
  best-effort final-segment name-prefix range or nothing; the naive
  references model ranges via the same fill helpers while keeping
  matching/ordering independent -- multiterm_test.go holds the
  independent multi-term reference ladder and the "fire fox" /
  "my backup" repro pins). fuzzy.go is the fuzzy
  (subsequence) tier for name-mode queries: entries holding the query
  as an in-order-with-gaps subsequence (same fold regimes) match with
  classFuzzy (ordinal 3, shared with classPathSub -- modes never mix;
  cand gained score int32, 0 outside the fuzzy class, compared DESC
  inside it before the usual tie-breaks). Two-phase queryNamesFuzzy:
  phase 1 = the unchanged substring scan plus per-shard live-hit
  counts and a pooled per-entry bitset (shards rounded to 64 entries,
  word-disjoint writes); SKIP RULE: phase-1 total >= limit means no
  fuzzy hit can enter the top-limit, so phase 2 never runs for common
  queries (and never for single-unit patterns, whose subsequence ==
  substring); phase 2 (ASCII) sweeps the blob via ciScan for the
  pattern byte with the fewest ACTUAL blob occurrences (Store.byteFreq,
  a 256-entry histogram updated in appendEntry -- the static
  nameByteFreq table cannot see corpus-specific rarity), maps hits to
  entries like scanRange, skips tombstoned/marked entries, subsequence-
  checks survivors and scores passers; non-ASCII patterns take a
  per-entry rune subsequence walk. Scoring (only on subsequence
  passers, off the hot path): optimal-alignment DP, fzf-v2 style --
  match base + bonuses for name start/word boundary ('-','_','.',' ',
  letter<->digit)/camelCase step/consecutive run, minus capped affine
  gap penalties -- for names <= 512 units, greedy leftmost alignment
  beyond; tests pin score ORDERINGS, never absolute values, and the
  naive reference (naiveQueryFuzzy in fuzzy_test.go) reuses the score
  function but keeps matching/ordering independent.
  `QueryWith(q, limit, QueryOptions{FuzzyDisabled, Blend})` is the
  options path (config search.fuzzyDisabled -> main.go ->
  Manager.SetFuzzyDisabled): disabled dispatches to queryNamesSub,
  the pre-fuzzy scan, behavior-identical to the old engine. blend.go
  is the frecency ranking blend, applied at EXACTLY one stage --
  selectTop's post-scan merge over the <= workers*limit heap
  survivors, never the per-entry scan: per merged candidate boost =
  Signals.Boost, penalty = Signals.Penalty, cwd = Signals.CwdBoost,
  and for COLD candidates only (boost 0) one budget-bounded
  (RecencyBudget, default 15ms) Signals.Recency batch mapped through
  recencyScore (log-scaled age -> [0,1]: ~1 within the hour, ~0.5 a
  day, ~0.2 a week, 0 at 30d). Ordering: effective class (class - 1
  when boost > TierJump -- one tier max) then blended = score/64 +
  wF*boost + wR*recency + cwd - wN*penalty + prior DESC then the
  pre-blend
  chain; weights <= 0 disable each part. Blend.Prior is the
  pick-memory prior seam (internal/priors wired by internal/app
  priors.go): a per-query resolver QueryWith calls ONCE with the raw
  query on a per-query Blend copy (unexported priorFn -- no scan path
  or per-mode signature changes), whose returned func selectBlended
  consults once per merged candidate as the additive prior term;
  Prior alone activates the blend, nil resolver answers and
  zero-returning funcs are byte-identical no-ops
  (blendprior_test.go pins absent/zero no-op, within-class-only
  reordering, one-resolve-per-query, and the no-resurrection
  contract). Blend.Model is the learned-arbitration seam beside it
  (internal/arbiter wired by internal/app arbiter.go): the same
  resolve-once-per-query contract on the same per-query copy, except
  the returned func also receives the candidate's ResultSignals --
  filled inline from selectBlended's locals exactly as each
  component participated -- and its value joins `blended` AFTER the
  prior (within-effective-class only; the caller clamps magnitude,
  arbiter.FileDeltaClamp); Model alone activates the blend, and
  blendmodel_test.go mirrors the whole blendprior pin family
  (absent/nil-resolver/zero no-ops, within-class-only, signals
  delivery, prior+trace composition, no-resurrection). The Manager holds the Blend
  (SetBlend/Blend; swapped as an IMMUTABLE copy -- the app's cwd
  stash swaps fresh ones); nil or zero-value-Signals blends take the
  EXACT pre-blend selectTop path, byte-identical ordering pinned by
  TestBlendInactiveIsNoOp, and pruning stays pre-blend: a candidate
  outside its shard's top-limit heap cannot be resurrected
  (TestBlendMergedSetOnly, documented). candCompare's FINAL
  tie-break is the numeric-aware lexicographic path order
  (numorder.go, always on, every query mode incl. the shard heaps --
  selection at exact ties included): aligned digit runs compare
  numerically DESCENDING (datestamped/versioned families newest
  first -- the strverscmp-style lockstep walk, a true total order;
  equal-value runs continue, any other first difference keeps plain
  byte order, all-equal walks fall back to compareJoined); the naive
  test references share the rule via refPathLess (search_test.go) and
  numorder_test.go holds the family/stability pins; internal/match's
  Rank (plugin rows) deliberately untouched. signalstrace.go is the
  OPT-IN ranking-signals trace seam consumed by the app's telemetry:
  QueryOptions.Trace (*[]ResultSignals) + Manager.QueryTraced (Query
  itself untouched) fill one ResultSignals per returned Result --
  Path/Class/EffClass/Align/Boost/Recency/Cwd/Penalty/IsDir/PathLen,
  captured in selectTop's assembly tail and selectBlended exactly as
  the components participated (inactive blend = class/align only,
  EffClass == Class, signals zero) -- with the buffer riding an
  unexported field on a PER-QUERY Blend copy (traceBlend) so no scan
  path or per-mode query function changes; nil Trace is zero-cost
  byte-identical (TestTraceNilIsByteIdentical) and a non-nil Trace
  never changes results or order (TestTraceDoesNotChangeResults).
  Blend.Active() exports the participation probe. A query
  containing a path
  separator (on windows '/' too, normalized) dispatches to path mode
  (path.go): matched against the FULL path via a per-query dir-table
  prematch (dirs whose folded path+sep contains the query -- every
  child matches; matchDirASCII keeps byte-length arithmetic, its
  matchDirFold twin decides "covers all of V" by fold-equality
  because rune folds shift byte lengths) plus boundary splits
  q = S + R at the query's
  separators (S a sep-terminated dir suffix, R a name prefix
  fold-checked against the name blob); ranking exact-path >
  path-suffix >
  path-prefix > substring with the same tie-breaks. `Walk`: parallel walker (worker
  pool + LIFO queue) with exclude patterns (`Excluder`: bare pattern
  = base name, pattern with separator = full path; Match =
  MatchBase || MatchFull exactly, split plus HasFullPatterns for the
  walker hot path), symlinks indexed
  but never descended, permission errors counted not fatal, throttled
  progress callbacks. WALK ALLOCATION DIET (2026-07, recon-measured
  438 B / 4.26 allocs per entry before): base-name excludes are
  checked FIRST without any join; FILE entries join their full path
  into a per-worker reusable scratch buffer and hand MatchFull an
  unsafe.String view (nothing down that call retains or mutates it;
  only taken when HasFullPatterns) so no per-file path string is ever
  allocated, while DIRECTORY entries materialize the real string once
  and walkItem.full carries it into appendEntry -- whose signature is
  (pid, name, dirPath, isDir), interning the caller's string instead
  of re-joining (AddEntry joins for itself) -- and growChildren
  presizes children[pid] to the exact batch size before the append
  loop, so walk-built children slices end at cap == len (the
  append-ladder's measured 1.32x overshoot and copy churn are gone;
  pinned by TestWalkChildrenPresized, the scratch path by
  TestWalkFullPathPatternOnFiles + TestAppendJoinDir). WALK STRESS
  GATE (walkstress_test.go, the v395 field-crash regression rig:
  intermittent "growslice: len out of range" in appendName plus GC
  scanstack SIGSEGVs during startup indexing -- memory-corruption
  signatures): TestWalkStressIntegrity swaps readDirFn for an
  in-memory synthetic tree (~113k entries/walk, fresh name strings
  per call, base + full-path excludes so the per-file scratch/unsafe
  path runs, a stack-pump recursion per readdir so walker stacks
  grow-then-park as shrinkstack fodder) and runs 16 concurrent Walks
  under the production GOGC=40 window, verifying every store's full
  integrity (counts, monotonic offset table, NUL-free names, parent
  paths) per iteration; ~3s budget in CI,
  COMPETENT_SEARCH_STRESS_SECONDS / COMPETENT_SEARCH_STRESS_CONC
  extend investigation runs. The 2026-07-20 investigation (v395
  startup crash: intermittent growslice len-out-of-range / GC
  scanstack SIGSEGV on one field machine) CLEARED this package: no
  app-code defect (race-detector-clean; ~700M entries verified
  across plain/checkptr/clobberfree/gccheckmark/novarmake builds at
  up to 160 walker goroutines); compiler excluded (v395 disassembly
  matches stock go1.25.0 codegen for every crash-relevant function);
  and the same day's gosmopolitan cache-poisoning incident EXCLUDED
  for this build (stock-keyed action IDs are disjoint from the
  fork's go1.26.4cosmo collision namespace; the orchestrator
  predates the first-bad; opposite crash signatures -- team memory
  competent-search-thing-v395-not-fork-cache-poisoned). Root cause
  remains external to this repo: leading hypotheses are
  machine-local memory or an unattributed stock-runtime issue
  (golang/go#77955/#73259 family). The gate pins the walker/store
  concurrency+integrity invariants only -- it cannot detect wrong
  bytes linked into a binary -- so keep it green rather than
  re-litigating the walker's ownership story. `Manager`: owns the RWMutex contract (queries
  RLock, mutations Lock); roots/excludes are LIVE-mutable now
  (`SetRoots`/`SetExcludes`, the config editor's index-scope apply;
  Roots/Excludes read under the lock and `BuildFromDisk` latches one
  consistent copy at entry -- the live store is untouched until the
  next rebuild); `BuildFromDisk` walks into a fresh store and
  swaps it in, so queries keep working during rebuilds -- and first
  recomputes the mount skip list (mounts.go: `SystemMountSkips` reads
  /proc/self/mounts, linux-only, nil on any failure; pure
  `ParseMountSkips` returns mountpoints strictly under the roots whose
  fstype is kernel-virtual or network -- all fuse/fuse.* skipped,
  overlay deliberately KEPT (container roots) -- octal escapes
  decoded, "/" never returned, glob-metachar mountpoints dropped,
  capped at 256, a mountpoint equal to a configured root never
  skipped = the index-it-anyway escape hatch), appending it to the
  excludes as full-path patterns and logging the list; the `mountSkips`
  package var is the test seam; `RealMountpoints(roots)` / pure
  `ParseMountpoints` are the inverse view -- mountpoints of WALKABLE
  (non-virtual/network/FUSE) filesystems under (or equal to) the
  given roots, linux-only/nil elsewhere -- consumed by the watch
  sweeper's mount-diff and the fanotify notifier's extra-mount marks.
  `Add`/`Remove` are the watcher-phase entry points;
  `LiveDirsPage(start, max)` pages through the live (non-tombstoned)
  indexed directories releasing the read lock between pages
  (DefaultLiveDirsPage = 4096), and `ChildrenOf(dir)` returns a
  directory's direct children as Name/IsDir pairs -- the watch
  layer's shallow-reconcile and sweep enumeration surface. `Store.Footprint()` /
  `Manager.Footprint()` (footprint.go): exact byte accounting of every
  column/blob (len-based; 16B string headers) plus documented
  approximations for the dirIndex and children maps, and
  BytesPerEntry -- diagnostics for the whole-filesystem sizing work.
  A bare `Store` is NOT
  thread-safe. Benchmarks build synthetic 100k/1M-entry stores in
  memory (see bench_test.go) and a ~50k-entry disk tree. An env-gated
  measurement harness (measure_test.go + gated benches in
  bench_test.go; skip-by-default, CI-invisible) backs the efficiency
  numbers in PR bodies: COMPETENT_SEARCH_MEASURE=1 walks the whole
  container filesystem (BuildFromDisk-style excludes + mount skips)
  and reports Footprint/heap/forced-GC evidence (test phase; timings
  labeled coverage-instrumented) plus an un-instrumented walk bench;
  COMPETENT_SEARCH_MEASURE_HUGE=1 builds a shared 30M-entry synth
  store ENTIRELY in the benchmark phase (the test phase's 30s
  per-test budget cannot fit the build) for name+path+fuzzy query
  latency
  (BenchmarkSearchHuge) then footprint/heap/forced-GC evidence
  (BenchmarkHugeStoreMeasure, declared last -- it releases the store);
  COMPETENT_SEARCH_MEASURE_OUT writes the JSON + .txt report to a
  file.
- `internal/config` -- config.json load/save (roots, rootsVersion,
  excludes, hotkey,
  rescanIntervalMinutes, maxResults, search {fuzzyDisabled -- the
  fuzzy-tier kill switch, zero value = fuzzy ON per the tray.disabled
  convention; main.go wires it to Manager.SetFuzzyDisabled -- and
  frecency {disabled, halfLifeDays 14, weightFrecency/weightRecency/
  weightCwd/weightNoise 1.0, tierJumpCount 3.0}, the ranking-blend
  knobs main.go wires to app.Options.Frecency; NUMERIC CONVENTION
  UNIQUE HERE: Normalize repairs only the EXACT zero to the default
  (halfLifeDays repairs <= 0), a NEGATIVE value is the documented
  per-signal off switch and passes through, and the schema rejects
  the ambiguous literal 0 -- and priors {disabled}, the
  pick-memory priors knob (ON by default, the tray.disabled
  convention; disabled is a debug escape hatch, a single bool, so
  Normalize has nothing to repair) main.go wires to
  app.Options.Priors -- and telemetry {maxSizeKB 65536}, the
  ALWAYS-ON local ranking log's one knob (deliberately NO off
  switch: the log is private by staying on the machine; query text
  and plugin titles recorded in full) main.go wires to
  app.Options.Telemetry, Normalize repairs maxSizeKB <= 0 while the
  schema rejects it -- and arbiter {disabled}, the learned
  composition arbitration knob main.go wires to app.Options.Arbiter
  (ON by default, the tray.disabled convention; disabled is a debug
  escape hatch / kill switch, a single bool, so Normalize has
  nothing to repair)},
  watcher {maxWatches 0 = auto-budget / negative = unlimited,
  sweepMinutes 0 = the 20m default, sweepDisabled (zero value = sweeps
  ON, the tray.disabled convention), watchExcludes
  (json omitempty; excluder-syntax patterns never LIVE-WATCHED but
  still indexed + swept), backend (json omitempty; the
  WatcherBackend* constants "auto"/"fanotify"/"fsevents"/"inotify" --
  fanotify (linux) and fsevents (darwin) = STRICT, no per-dir
  fallback, and "kqueue" is deliberately NOT a config value (runtime
  label only); Normalize trims+lowercases and repairs
  empty/unknown to "auto", schema enum in lockstep) -- main.go copies
  all five into app.Options
  {WatchMaxWatches, SweepInterval, SweepDisabled, WatchExcludes,
  WatchBackend}},
  theme, plugins {disabled, entries
  {<id>: {disabled, settings}}}, bangs {sigils, aliases}, rewrites
  [{name, pattern, replacement, title?, icon?, disabled?}] (the regex
  rewrite rules; passed to plugin.Options.Rewrites), tray
  {disabled}, history {persistDisabled}, stats {disabled -- the
  system-stats sampler kill switch, zero value = on per the
  tray.disabled convention; internal/app's buildStats reads it, so it
  applies on the next launch}, window {translucent -- the
  per-pixel-alpha window flag main.go reads via
  app.WindowTranslucent(); zero value = opaque = the safe default,
  needs a compositor, README "Translucent window" holds the measured
  evidence; width/height -- the bar window size main.go reads via
  app.WindowSize(), defaults 780x550: Normalize repairs <= 0 (and
  absent) to the defaults and clamps positive values below the
  320x240 floors up to them, so the app never builds an unusably
  tiny window}, firefox {frequentSites
  {minVisitsMonth 11, minVisitsWeek 1, refreshMinutes 10, maxResults
  6, profileDir ""}, openTabs {maxResults 6, profileDir ""}} -- the
  frequentSites defaults encode ">10 visits in 30 days AND >=1 in 7";
  the numeric knobs are Normalize-repaired to defaults when <= 0,
  both profileDirs are passed through verbatim), preview {enabled,
  windowWidth 1600, windowHeight 800, textMaxKB 256, imageMaxEdge 800,
  dirMaxEntries 200, kagi {apiKey, baseUrl, maxResults 8}, openai
  {apiKey, baseUrl, model "gpt-5-mini", maxOutputTokens 1024}} -- the
  opt-in preview
  pane (zero value = off); numerics and an empty model are
  Normalize-repaired, the API keys AND base URLs pass through verbatim
  (empty baseUrl = the official endpoint; validation happens in
  internal/preview, not here) and are never
  logged. Lives under
  os.UserConfigDir(); the `COMPETENT_SEARCH_CONFIG_DIR` env var
  overrides the directory (tests rely on this); `Dir()` exposes that
  directory (the plugins/ and themes/ dirs and history.json live
  inside it, next to config.json). The app's OTHER env knobs live with their owners:
  `COMPETENT_SEARCH_SOCKET` (internal/ipc, the single-instance socket
  path) and `COMPETENT_SEARCH_HOTKEY_BACKEND` (internal/app hotkey.go,
  backend override) -- all three are documented in the README. Default
  roots are the WHOLE FILESYSTEM (migrate.go: defaultRootsFor -- "/"
  on linux/darwin, %SystemDrive% with C:\ fallback on windows; goos +
  getenv are parameters so tests cover the windows shape headlessly)
  and default excludes = baseExcludes (.git node_modules .cache --
  FROZEN as the v2-era set migrations compare against, new defaults
  never go there) + noiseExcludes (.hg .svn __pycache__ .mypy_cache
  .pytest_cache .ruff_cache .tox .nox .venv, the v3 high-churn set) +
  the system trees (/proc /sys /dev /run /tmp /var/tmp full-path +
  lost+found by name; unix-likes only -- windows gets the name
  patterns without system trees) + firmlinkExcludesFor (darwin only:
  /System/Volumes/Data full-path -- the APFS Data volume macOS ALSO
  exposes at the firmlinked canonical paths /Users, /Applications,
  ..., so an unguarded "/" walk indexes ~45% of the disk twice) +
  darwinNoiseExcludesFor (darwin only, the v5 set: Caches,
  DerivedData, _CodeSignature, CodeResources by name +
  /private/var/folders full-path -- macOS's real per-user temp tree,
  the /tmp and /var/tmp excludes only cover the symlinked spellings;
  wholesale .app/.framework internals and Application Support are
  DELIBERATELY not excluded -- they change search semantics and
  await an owner decision).
  THE $SCHEMA RESERVED KEY: Config's FIRST field is `Schema` (json
  `$schema,omitempty`), so Save/Encode emit `"$schema":
  "./config.schema.json"` (config.SchemaRef) as the document's first
  key -- Default() carries it, Normalize stamps an EMPTY value
  (existing configs gain it on their next save; a hand-set value
  passes through verbatim), the loader never validates it, the GUI
  strict decode accepts it, UnknownKeys knows it (never reported),
  and the editor hides it (x-editor-hidden). The referenced sidecar
  <configDir>/config.schema.json is written by internal/app's
  schemasidecar.go at every Startup (embedded
  schemas.ConfigSchemaJSON, byte-equal = skip, atomic temp+rename,
  before the config-dir watcher comes up; its file name never
  matches the config.json hot-apply path, so no watcher loop).
  rootsVersion (0 = legacy, current
  6) drives the one-shot Load migration (migrateRootsFor; goos and
  the RAW file bytes are parameters so tests cover the darwin shape
  and the old-key reads headlessly), each missing
  step applied in
  order: the v2 step moves configs whose roots are exactly the legacy
  home default (or empty) to the new default roots + appends the
  missing system excludes (user patterns untouched; customized roots
  stamped only); the v3 step appends the MISSING noiseExcludes -- but
  ONLY when the exclude list still contains ALL of baseExcludes
  (default-shaped); a curated-away or explicitly empty list is
  stamped only, with an informational note; the v4 step applies the
  identical policy to the darwin firmlink exclude (non-darwin = pure
  stamp, nothing added or announced); the v5 step applies it AGAIN to
  darwinNoiseExcludesFor -- a NEW version rather than an extension of
  v4, so configs a v4-era build already stamped still receive it --
  the v6 step (migrateRankingDefaults, reads the RAW bytes because
  the old keys left the struct) makes search.telemetry always-on
  (every old enabled/retainQueries key dropped outright, an explicit
  enabled:false included -- overruled by design and announced) and
  flips search.priors/arbiter to on-by-default (absent old key = on;
  enabled:false preserved as disabled:true; enabled:true = on, key
  dropped) -- and each step is gated on its
  own version so already-fired informational notes never repeat.
  Either way version 6 is
  Saved back, and every user-visible change lands in the
  non-serialized MigrationNotes (json:"-") that internal/app logs
  loudly at startup -- the scope never changes silently. `Load` never crashes: missing file -> defaults
  written, corrupt file -> current defaults + error returned for
  logging, failed migration rewrite -> migrated config + error.
  `Normalize` repairs zero values (empty theme -> dark, nil plugin
  entries/bang aliases -> empty maps, empty sigils -> the ! / @
  defaults; history needs nothing -- its zero value means persistence
  ON, the tray.disabled convention; non-positive firefox.frequentSites
  and firefox.openTabs numbers -> their defaults; negative
  watcher.sweepMinutes -> 0, while watcher.maxWatches keeps its sign
  and watchExcludes stays as written); entry settings are
  opaque json.RawMessage forwarded verbatim to that plugin. `Save` is
  ATOMIC (temp-file-then-rename in the config dir, the
  internal/history pattern at the file's historical 0644 perms; a
  crash never truncates config.json and watchers see one rename per
  save); `Encode(c)` returns exactly the bytes Save writes (two-space
  indent + trailing newline -- the app's self-write suppression hashes
  them); `CurrentRootsVersion()` exposes the build's rootsVersion
  stamp (the GUI save path preserves the on-disk stamp so a full-file
  rewrite can never re-trigger the Load migrations); and
  `UnknownKeys(raw)` (unknown.go) reflectively walks a raw
  config.json document against Config's json tags and reports the
  dotted paths of keys a full rewrite would drop ("$schema" included;
  maps by key, arrays by index, json.RawMessage settings opaque,
  wrong-shaped values skipped -- unknown KEYS only, the strict decode
  owns type errors), sorted; the config editor warns with it.
- `internal/history` -- the query-history store behind the frontend's
  Up/Down recall, pure and exhaustively unit-tested. `New(path,
  persist)`; `Load()` (missing file or memory-only store = empty +
  nil error; corrupt/non-string-array = empty + error returned for
  one-shot logging; loaded lists get the Add invariants: trimmed,
  blanks dropped, duplicates keep their newest occurrence, capped);
  `Add(entry)` (TrimSpace, blank = silent skip; exact-match
  move-to-newest dedup; cap 100 -- unexported const -- oldest
  dropped; when persist: atomic temp-file-then-rename write, 0600,
  MkdirAll the parent like config.Save -- the in-memory list updates
  even when the write fails, so in-session recall survives disk
  problems); `Entries()` (defensive copy, oldest -> newest, never
  nil). Mutex-guarded (the app's bound methods run on arbitrary
  goroutines); persist=false never touches the disk, not even reads.
  Persist format: a plain JSON array of strings at
  <configDir>/history.json (wired by internal/app history.go;
  config.json's history.persistDisabled opts out).
- `internal/frecency` -- the PURE half of result prioritization:
  the signal sources the ranking blend consumes (the blend itself is
  internal/index blend.go, the app wiring internal/app frecency.go,
  the production ProcTree internal/appctx proctree.go, the knobs
  config search.frecency). Modeled on internal/history
  throughout: mutex-guarded (RWMutex, queries RLock), injectable
  clocks/seams, atomic temp-file-then-rename 0600 persistence,
  missing file = empty + nil error, corrupt file = empty + ONE
  returned error for logging, defensive copies, nil-receiver-safe
  no-ops. Pieces: `Store` (frecency.go; New(path, Options{HalfLife
  default 14d, Cap default 4096, Now, Persist}); RecordOpen lazily
  decays the stored count to now (0.5^(dt/halfLife)), adds 1,
  persists {v:1,entries:{path:{c,t}}} -- the in-memory update
  survives a failed write; Boost = the READ-ONLY decayed count, 0
  unknown, no write amplification; the cap keeps the highest decayed
  counts (ties: newer touch, then path) on record AND load; Load
  keeps stored values verbatim (decay applies on read), drops
  garbage entries (empty path, c <= 0, zero t), rejects wrong
  versions; empty paths skipped, never trimmed). `PathPenalty`
  (penalty.go; pure location-noise score in [0, 1], a rank NUDGE
  never an exclusion: noise-class dir components (.cache/cache/.git/
  node_modules/tmp/.tmp/temp/.temp, case-insensitive -- /tmp and
  /var/tmp arrive via their own "tmp" component) cost 0.3 each,
  other dot-DIRS 0.1, depth past 6 components 0.05 each capped at
  0.25 so deep-but-clean < /tmp workspace < deep cache noise (the
  motivating log.txt report, pinned as ordering tests); the BASE
  name is exempt (dotfiles like .bashrc are prime targets); / and \
  both split). `Probe` (probe.go; NewProbe(ProbeOptions{TTL 5m,
  Lstat, Now, Workers 8}); BatchRecency(ctx, paths, budget) =
  max(atime, mtime) per path -- linux reads Stat_t.Atim
  (recency_linux.go), darwin/windows fall back to mtime-only
  (recency_other.go, keeps the windows/amd64 cross-compile pure) --
  behind a TTL cache (failures cached negatively; bounded 4096:
  sweep expired, then reset), bounded worker fan-out, and a HARD
  wall-clock budget/ctx cutoff: unstatted paths are simply absent
  (zero time on lookup), stragglers finish their one stat into the
  cache for the next query; the relatime atime-coarseness caveat
  lives in the package doc, mtime still catches just-downloaded).
  `DeriveCwd(tree ProcTree, rootPID)` (cwd.go; the focused-app
  working-directory heuristic over the injectable
  {Children,Cwd,Foreground} seam: the tpgid-style Foreground hint on
  the root wins when its cwd is meaningful, else the deepest
  meaningful cwd depth-first (root included, ties keep the earlier
  find, 32-deep cap + visited set so cycles are harmless); "/", the
  user home, and unreadable are NOT meaningful (ok=false when
  nothing qualifies) -- a terminal parks at ~, the shell/editor
  CHILD holds the real signal; the production /proc wiring is
  appctx.ProcTree, injected through the app's plat.procTree seam)
  plus `CwdBoost(path, cwd, weight)` (full
  weight for the cwd itself and direct children, halved per extra
  component, component-wise containment so /projX is not under
  /proj; weight 0 = disabled, "/" or "" cwd = no signal). `Signals`
  (signals.go) bundles Boost/Penalty/Recency/CwdBoost plus the
  Cwd/CwdWeight fields for the engine blend; the zero value
  degrades to all-no-ops (appctx.Cache pattern) AND deactivates the
  blend outright (index.Blend.active). frecency.json
  deliberately has NO schema in schemas/ (like history.json).
  Exhaustively unit-tested, headless, table-driven: fake clocks,
  counting/blocking lstat fakes, scripted ProcTree fakes, real
  tempdir files (os.Chtimes) for the atime/mtime max path.
- `internal/priors` -- the PURE half of the pick-memory ranking
  priors (config search.priors, ON by default -- disabled is a debug
  escape hatch; consumed by
  internal/index's Blend.Prior seam, wired by internal/app
  priors.go), pure stdlib and headless-tested on the
  internal/frecency conventions (RWMutex, injectable clock,
  nil-receiver/zero-value = total no-op, immutable swapped state).
  Three tables per generation (Tables, built by BuildTables and
  swapped whole via Store.SetTables): exact-query pick memory
  (normalized query -> path -> frecency-style decayed pick weight,
  14d half-life; term = 6*w/(1+w), the dominant within-class signal),
  per-extension and per-dir-prefix (first 3 DIRECTORY components,
  both separators split) smoothed pick rates ((picks+1)/(imps+20)
  applied as a clamped log-odds nudge vs the 1/20 unseen baseline,
  +-0.3 max per table -- the penalty/recency scale). Data sources,
  read-only + tolerant (missing = empty + nil error, malformed lines/
  entries skipped, oversized files ignored): the telemetry.jsonl(.1)
  JSONL FORMAT as a data contract (ReadTelemetryFile parses only
  v/ts/query/shown file paths/picked kind+path; non-file picks =
  impressions only; unknown fields -- the plugin-row titles included
  -- are ignored; deliberately NO internal/telemetry import) and
  frecency.json (ReadFrecencyWeights, the {v:1,entries:{path:{c,t}}}
  shape re-declared) whose decayed-count distributions BOOTSTRAP the
  two rate tables while the log holds < 20 file picks -- the
  exact-query table never bootstraps. Memory hard-capped: 2048
  queries x 4 rows under a 512 KiB approximate byte budget (lowest
  decayed best-weight queries evicted first), 512/2048 rate keys
  (most-seen kept). Store.PriorFunc(query) resolves ONCE per query
  (table-generation snapshot, no locks or allocation on the
  per-candidate path) and returns nil when nothing applies.
- `internal/arbiter` -- the PURE half of learned composition
  arbitration (config search.arbiter, ON by default -- disabled is a
  debug escape hatch / kill switch; applied at
  the index Blend.Model seam AND the app layer's plugin-emission
  path, both wired by internal/app arbiter.go), pure stdlib and
  headless-tested on the internal/priors conventions (RWMutex
  Store, nil-receiver/zero-value = total no-op, immutable swapped
  Model generations, tolerant log reading). ONE feature definition
  (features.go): Row is the unified projection of a telemetry
  shown-row (training) and a live row (serving) -- file rows carry
  the ResultSignals components + depth/ext, plugin rows carry
  id/score/priority/within-source rank -- visited sparsely
  (visitFeatures) by both the serve-path dot products and the
  trainer's dense vectors (parity pinned); FeatureDim = 69: bias +
  kind + file class one-hots/jump/align/boost/recency/cwd/penalty/
  isDir + depth buckets (4) + ext FNV buckets (16) + the four known
  builtin source one-hots (apps-search/windows/firefox-tabs/
  firefox-frequent) + other-plugin FNV buckets (8) + score/priority/
  source-rank + the query-shape and time-of-day features CROSSED
  with the row kind (qlen buckets x2, has-space x2, has-separator
  x2, tod buckets x2 -- row-independent features cancel out of every
  pairwise comparison, so "which source does this query shape mean"
  must enter as kind crosses). read.go re-declares the telemetry
  JSONL line shape with explicit tags (the priors no-import stance:
  internal/telemetry is an append-only writer with a one-way
  MarshalJSON and no reader; the on-disk format is the contract) --
  missing/oversized files = (nil, nil), malformed/wrong-version/
  unknown-kind lines skipped, SourceRank counted per plugin id,
  Priority derived (apps-search = 1, the one production prioritized
  source; serve reads the emission's real value), Hour from the
  pick ts. train.go: Train = pairwise logistic SGD (picked row must
  outscore every other shown row of its impression; fixed seed
  20260720, 12 epochs, lr 0.2, L2 1e-4 -- deterministic given the
  log, pinned) + the ACTIVATION GATE: >= MinPicks (200) JOINED
  picks, finite weights, and on the time split (oldest 80% train /
  newest 20% holdout) the model's holdout pairwise accuracy must
  STRICTLY beat the delivered order's accuracy on the same pairs --
  pass = retrain on ALL picks and ship, refuse = TrainOutcome with
  nil Model + a human-readable Reason either way (RetrainEvery = 50
  is the app layer's new-picks retrain cadence). Model.Score = the
  full-vector score (cross-source comparisons, same-impression rows
  only); Model.FileDelta = the file-VARYING block only (per-query
  constants would eat headroom without reordering), clamped to
  +-FileDeltaClamp (2.0 blend units -- strictly under the blend's
  one-class-band equivalence, the 3.0 tier-jump threshold, and the
  exact-prior's 6.0 saturation; class inversion is additionally
  impossible structurally, effClass being the primary sort key).
- `internal/telemetry` -- the ALWAYS-ON local ranking log (config
  search.telemetry carries only maxSizeKB -- deliberately no off
  switch, the log is private by staying on the machine; wired by
  internal/app's
  telemetry.go, fed by internal/index's signalstrace.go seam), pure
  (stdlib only) and exhaustively unit-tested. One JSON line per PICK
  at <configDir>/telemetry.jsonl: Record {v 1, ts RFC3339 (both
  stamped by the store when unset, injectable clock), query (always
  recorded in full), blendActive, joined (impression found in the
  app's ring), refined (reserved, always false), shown, picked}.
  ShownRow marshals KIND-SHAPED (custom MarshalJSON): file rows carry
  path + the full feature vector explicitly -- class/effClass/align/
  boost/recency/cwd/penalty/isDir/depth/ext, zeros included so class
  0 = exact is never ambiguous with omitted -- while plugin rows
  carry exactly rank/kind/plugin/score/title (full-fidelity capture;
  the title is the rendered row title the frontend reports). Store: append-only JSONL (deliberate divergence from
  history.json's whole-file rewrite -- immutable append-heavy
  records), O_APPEND single-write() lines so appends never interleave
  mid-record, mutex-guarded, 0600 (re-tightened best-effort per
  open), MkdirAll parent, nil-receiver-safe no-ops; rotation = an
  append that would cross MaxSizeKB (New repairs <= 0 to 65536)
  renames the live file over telemetry.jsonl.1 first, so the disk cap
  is two generations, an empty live file never rotates, and a torn
  final line is reader-skipped (loss-tolerant log, not a ledger; no
  Load step, only offline tooling reads). The wire half:
  PickReport{query, shown []ShownRef{kind, path|plugin+score+title},
  picked PickedRef{rank, action, revealed}} -- row IDENTITIES plus
  the plugin-row titles (the one display field only the frontend
  knows), so a frontend can never forge feature values --
  and ValidatePickReport (bounded sizes 256 rows/4096-byte strings
  incl. titles -- wire-abuse defense, never redaction --
  per-kind field consistency incl. abs paths + plugin-id shape +
  score 0..100, in-range rank, open/reveal + revealed-flag
  consistency for file picks, charset-gated action kind for plugin
  picks). Deliberately NO schema in schemas/ (internal single-party
  format, the history.json / frecency.json precedent).
- `internal/sysstats` -- the system-stats sampler behind the
  frontend's stats row, pure and headless-tested (fixture proc/sys
  trees, injectable clock + gpuExec seams). `Snapshot` is the wire
  contract (json tags enabled/cpuPct/cpuOk/gpuPct/gpuOk/memUsed/
  memTotal/memOk/swapUsed/swapTotal/swapOk/netRxBps/netTxBps/netOk;
  bytes for mem/swap, bytes/sec for net, 0..100 pcts; *Ok=false =
  "render a dash"; Enabled is the APP layer's field -- the sampler
  always leaves it false, internal/app stamps it true on every
  GetStats return and emitStats payload, so Enabled false = feature
  off = the frontend hides the row). `New(Options{ProcRoot "/proc",
  SysRoot "/sys", GOOS
  runtime.GOOS, Interval 1500ms, GPUInterval 5s, GPUTimeout 1s,
  LookPath exec.LookPath, OnUpdate, Logf; unexported test seams
  gpuExec + now + darwin})` probes sources ONCE, cheaply (no
  subprocess spawns), by a GOOS switch: windows/unknown = zero
  sources + one "placeholders" log;
  linux = the three proc files assumed present, GPU = first readable
  glob hit of SysRoot/class/drm/card*/device/gpu_busy_percent
  (amdgpu) else LookPath("nvidia-smi") else none (intel: deliberately
  absent, no cheap sysfs busy%), all summarized in ONE "stats:
  sources: cpu=... gpu=..." line; darwin = the darwinReaders seam
  (Options.darwin, else newDarwinReaders -- binding only, zero IO at
  probe time) + its own accurate source line ("cpu=host_statistics
  mem=vm_statistics64+hw.memsize swap=vm.swapusage net=sysctl(iflist2)
  gpu=ioaccelerator" -- gpu=none when the gpu reader member is nil),
  while a readers value with nothing bound (the !darwin
  stub) degrades to the placeholders path. DARWIN LAYOUT (the
  fixture tests must keep running on BOTH CI jobs, so the split is
  VALUE-driven, build tags confined to the thin readers): darwin.go
  is UNTAGGED pure logic -- the darwinReaders struct (cpuTicks/
  memTotal/vmStat/swapRaw/ifRIB/ifNames/gpuStats; nil member = that
  metric
  degrades alone), cpuCountersFromTicks (busy=user+system+nice,
  total=busy+idle; uint32 tick wraps take the linux skip-one-update
  path), memFromVMStat = Activity Monitor's "Memory Used"
  ((internal - purgeable clamped at 0) + wired + compressor pages *
  pageSize -- NOT total-free, which macOS's tiny free_count makes
  meaningless), decodeXswUsage (vm.swapusage xsw_usage: LE uint64s
  at 0/8/16, min len 24; total 0 = the valid empty-dynamic-swap
  zero, rendered 0M), decodeIfList2 (bounds-checked NET_RT_IFLIST2 walker, the
  fanotify_parse pattern: 4-byte prologue, advance by ifm_msglen,
  RTM_IFINFO2=0x12 records read ifm_index@12 + if_data64 64-bit
  ibytes/obytes@96/104; malformed lengths error, zero usable records
  error -- never a silent zero), the SEPARATE darwin iface filter
  (skip lo/gif/stf/awdl/llw/utun/ap/bridge/anpi/pktap/feth/vmnet;
  keep en* -- Wi-Fi IS en0 on Macs -- and bond*), the IOAccelerator
  utilization selection (gpuUtilKeys "Device Utilization %" preferred
  then "Renderer Utilization %" per accelerator -- the two
  widely-attested PerformanceStatistics keys -- and gpuPctFromStats =
  busiest accelerator, clamped 0..100, ok=false when nothing
  published), and the sampleCPUDarwin/sampleMemDarwin/
  sampleNetDarwin/sampleGPUDarwin bodies;
  readers_darwin.go (the package's ONE cgo file, darwin-only) binds
  host_statistics/host_statistics64+host_page_size (mach ports
  deallocated per call) + unix.SysctlUint64("hw.memsize") +
  unix.SysctlRaw("vm.swapusage") + syscall.RouteRIB(NET_RT_IFLIST2)
  (deprecated-but-kept: x/net/route exposes NO darwin byte counters
  -- verified) + net.Interfaces + the IOAccelerator
  PerformanceStatistics reader (cs_gpu_perf: IOKit port 0 = the
  default port on every macOS version, the matching dict consumed by
  IOServiceGetMatchingServices so it is never CFReleased, every
  created object released, re-matched per read -- no cached service
  handles; `-framework IOKit -framework CoreFoundation` LDFLAGS, the
  package's only framework link); readers_other.go (!darwin) binds
  nothing. sampleCPU/sampleMem/sampleNet/sampleGPU dispatch on
  s.dwn != nil
  and share the extracted updateCPURate/updateNetRate with linux
  (byte-identical linux behavior); sampleGPUDarwin rides the fast
  loop like the linux amdgpu sysfs read (an in-process registry
  call, no subprocess -- the nvidia slow-goroutine pattern is
  deliberately not used), and a nil gpu reader is the silent dash
  while a failed read or a registry publishing no utilization key
  (VM paravirtual GPUs) = GPUOK false + one log line = the honest
  dash. THE invariant: nothing outside the
  sampler goroutines ever does IO -- Snapshot() is a mutex-guarded
  copy, SetVisible(v) is a flag flip (+ non-blocking 1-buffered kick
  send on true), and while hidden the loops sample NOTHING, so the
  start-hidden app reads zero bytes until first summon. Start(ctx):
  zero sources = log + return, else ONE fast goroutine (select ctx /
  kick / Interval ticker / one-shot follow-up timer) + ONE slow
  nvidia goroutine only for the nvidia source (GPUInterval ticker,
  exec via gpuExec seam under a GPUTimeout CommandContext with 250ms
  WaitDelay -- a hung nvidia-smi is killed -- parse leading int,
  store value+timestamp; the fast loop folds it in and expires it to
  GPUOK=false past 3*GPUInterval; no summon kick here by design, an
  exec has no business on the summon path). A kick = immediate
  baseline sample (point-in-time mem/swap/amdgpu/ioaccelerator
  published; previous
  RATE values kept, never blanked) then a one-shot follow-up at
  Interval/5 (~300ms) so cpu/net rates turn fresh right away. Rates
  (cpu pct, net Bps) come from counter deltas ONLY when the stored
  counters are <= 3*Interval old (rateWindow) -- older = re-store +
  keep previous values -- and negative/zero deltas (wrap) skip the
  update; cpu busy = total - idle - iowait over the first 8 "cpu "
  aggregate fields (guest/guest_nice excluded, already inside
  user/nice), pct clamped 0..100; mem used = MemTotal - MemAvailable
  (missing MemAvailable = MemOK false; kB * 1024 = bytes); swap =
  SwapTotal/SwapFree, total 0 valid (SwapOK true, rendered 0M); net =
  sum of
  rx/tx bytes over real interfaces -- "lo" exact plus the virtual
  prefixes veth/docker/br-/virbr/vnet/tap/tun/wg/zt/dummy/ifb/kube/
  cni/flannel/cali skipped, eth/en/wl/ww/bond kept. Per-metric
  failures = that metric OK=false + one log per distinct message
  (bounded map, 64), everything else unaffected; OnUpdate fires on
  the sampler goroutine after each published sample while visible
  (nil tolerated). Exhaustively table-tested (parsers incl. malformed
  input + wrap + the iface filter, probe variants, direct-sample rate
  math on a fake clock, full lifecycle over a real loop, nvidia fake
  incl. ctx-deadline + real /bin/sh subprocess kill) plus
  BenchmarkSample: one full fast-path sample against the real /proc
  (skips where unreadable). darwin_test.go is UNTAGGED (synthetic
  xsw_usage/RIB buffers, scripted fakeDarwin readers with call
  counts, GOOS "darwin" + injected seam lifecycle incl. the
  hidden-reads-nothing proof -- runs on linux CI AND the mac job;
  the nil-seam stub expectation is runtime.GOOS-gated);
  readers_darwin_test.go (darwin-only) real-calls every production
  reader on the mac runner (tick monotonicity, memsize/vm_stat
  sanity, real-RIB decode against the documented offsets, the swap
  pipeline gate -- decode + sampleMem SwapOK=true whatever the total,
  the SWP field-report regression pin -- the GPU reader's clean
  semantics -- the registry match never errors, 0..100 when a
  utilization key is published, graceful ok=false on VM runners
  whose paravirtual GPU publishes nothing -- and a two-real-samples
  end-to-end with live cpu/net + the GPU either live in 0..100 or
  the logged honest dash).
  Consumed by internal/app's stats.go.
- `internal/theme` -- design-token resolution. WARNING: the 22
  `TokenNames` (bg, bg-elevated, fg, fg-dim, accent, accent-fg,
  selection-bg, selection-fg, border, highlight, warning, badge-bg,
  badge-fg, scrollbar, font-family, font-size, font-size-small,
  radius, gap, padding, bg-opacity, blur) are a STABLE PUBLIC
  CONTRACT -- the frontend exposes each as `--sb-<token>`, the README
  documents the table, and the plugin workstream styles plugin
  accents/badges against them (accent/accent-fg primary,
  badge-bg/badge-fg reserved for result badges); never rename or
  remove one. Builtins dark.json (the original palette) + light.json
  (extends dark) are embedded via go:embed. `Resolve(name,
  configDir)`: builtin lookup first (not shadowable), else
  `<configDir>/themes/<name>.json`; merges over the extends chain
  (builtin-or-user, depth cap 4, cycle detection), gap-fills from
  dark so the result always covers every token; validates strictly
  (unknown keys -> error naming them; values whitelisted to hex /
  rgb()/rgba()/hsl()/hsla() / px|em|rem|% lengths / bare numbers,
  font-family to a tight charset; url(, expression(, @import, `;`,
  `{`, `}` hard-rejected). ANY error returns the dark builtin
  ALONGSIDE the error (caller logs; never crash). sync_test.go is the
  drift guard: it parses frontend/src/style.css's :root --sb-* block
  and requires it token-for-token identical to dark.json -- edit both
  together or the build fails.
- `internal/plugin` -- the plugin system, pure and headless-testable
  (wired into the app by internal/app's plugins.go). INVERTED over the
  shared engine (engine.go): builtin providers are candidate SOURCES
  (interface candidateSource = provider + candidates(ctx,req)
  []match.Candidate + limit() + preRanked(); payload = the wire
  Result MINUS score/ranges) and sourceResults -> match.Rank ->
  mintResults is the ONLY path stamping Score/MatchRanges (rogue
  payload scores are overwritten; non-Result payloads dropped);
  external plugins (resultProvider; production always
  *externalProvider) are sanitized then engine-passed by rankExternal:
  claimed queries (req.Targeted or Trigger.Claims = prefix/regex path
  matched) ride TierTriggered with self-score as the hint and
  response order kept on ties, all_queries results are text-gated
  against Title+Keywords (misses dropped with a throttled reason) --
  dispatch_test fakes implement bare resultProvider and bypass, the
  routing test (engine_test.go TestEveryRegisteredSourceRoutesThrough
  Engine) pins that every PRODUCTION registration is one of the two
  shapes. Old per-provider score ladders/wordStart copies are GONE;
  each source declares ordered match Texts instead (apps [name],
  windows [title, app], sites [host-sans-www, title, url], tabs
  [title, host-sans-www, url]) and the engine's canonical bands
  apply. Options gains FuzzyDisabled (config search.fuzzyDisabled,
  threaded into every Rank) and Rewrites (builtin_rewrites.go:
  "rewrites" preRanked source at the triggered tier -- RE2 rules
  compiled at New via compileRewrites, full-match ^(?:pat)$ unless
  user-anchored, invalid = one Errors() line + skipped; on match ONE
  result per rule in config order, replacement/title expanded via
  ExpandString ($1/${name}/$$), open_url ONLY -- non-http(s)
  expansions logged + dropped; nothing registers when no rule
  compiles). schema.go:
  versioned JSON wire protocol
  (Request/Response/Result/Action, v=1; Result also carries Keywords
  <=8x64 runes -- extra engine match texts -- and MatchRanges <=32
  half-open RUNE pairs on Title, normalizeRanges clamps/sorts/merges
  against the post-truncation title; Result carries the INTERNAL-ONLY
  IconKey json:"iconKey" -- the "app:<ref>" icon-resolution key the
  frontend hands to ResolveIcons for a real icon image, stamped by
  the builtin app sources and CLEARED by the sanitizer on every
  external result (image icons are a trusted-source capability;
  InstalledApp mirrors the appctx Icon ref json:"icon" for the
  purpose); Action carries the
  INTERNAL-ONLY DesktopID json:"desktop_id" -- the .desktop entry
  behind a builtin run_command launch, consumed by the app's
  credentialed launch path -- and the INTERNAL-ONLY Tab json:"tab"
  -- the ffext c<conn>:<tab>:<window> routing token behind a builtin
  activate_tab switch, Value doubling as the fallback URL) and
  `SanitizeResponse`, which
  clamps/validates everything an external plugin returns: 20-result
  cap, rune caps (title 200/subtitle 300/badge 24/field 40+200, max 8
  fields), control chars -> spaces everywhere, icon = builtin name or
  <=32-byte glyph, accent_color regex, score default 50 clamp 0..100,
  action validation (open_path abs path, open_url http(s)+host,
  copy_text <=8 KiB, run_command 1..16 argv <=1024 B each and the
  whole RESULT is dropped unless the manifest sets allow_run_command;
  internal-only set_query/run_builtin/activate_window/activate_tab
  always stripped
  and a stray Action.Window, Action.Tab OR Action.DesktopID on
  external types
  cleared; anything
  removed gets a human-readable reason for logging). trigger.go:
  `Trigger` Compile/Match/Boost -- prefix (case-insensitive,
  rune-folded) / regex (ci RE2 on the RAW query) / all_queries paths
  (first match wins the stripped value), min_query_len in runes of
  the STRIPPED query gating all paths (defaults 2 when all_queries),
  optional focused-app gate (name/exe ci RE2, both-empty rejected at
  Compile, fail-closed) + focused_boost clamped 0..100. manifest.go:
  `LoadDir(<configDir>/plugins)` -- one error per broken manifest
  (path-prefixed), missing dir = no plugins no error, duplicate id ->
  first alphabetical dir wins, defaults (v=1, name=id, timeout_ms
  1500 clamp 100..10000, bangs=[id]), bangs lowercased+deduped,
  context subset of {focused,running,installed}, empty bangs + nil
  trigger rejected as unreachable, trigger compiled on load.
  bangs.go: `BangSet` -- config-driven sigils (must be one non-letter/
  digit/space rune; invalid ones recorded via Errors(), all-invalid ->
  defaults ! / @), Register (dup = error, first wins), Parse (sigil +
  [a-zA-Z0-9_-]* name lowercased + end-or-space + raw rest), Resolve
  (exact > alias > unique prefix, canonical bang returned), sorted
  Candidates(partial), Primary() = first configured sigil.
  command.go/http.go: the transports behind the tiny `transport` seam
  -- command = one shell-free subprocess per query (request JSON to
  stdin then closed, cwd = Manifest.Dir, argv[0] with a separator
  resolved against it, stdout capped 1 MiB, stderr capped 8 KiB and
  quoted in errors, ctx timeout hard-kills with 250ms WaitDelay);
  http = POST to the manifest url (ONE shared keep-alive client per
  Registry, max 3 http(s)-only redirect hops, 2xx required, body
  capped 1 MiB). Both error on invalid JSON and v != 1. registry.go:
  `New(Options) *Registry` wraps manifests in providers (settings
  default "{}", request context filtered to the manifest-declared
  parts, `SanitizeResponse` applied HERE so trusted builtins bypass
  it), registers builtins FIRST (a manifest can never shadow a
  builtin bang or id; dup bang/id = recorded error, first wins),
  honors the global kill switch + per-id disable entries, and
  collects every setup problem for `Errors()`. `Dispatch(ctx, query,
  gen, appCtx, emit)` returns `TargetInfo` synchronously and fans out
  one goroutine per matching provider: ctx-abortable debounce
  (clamped 0..2s here -- DebounceMS arrives unclamped), per-plugin
  timeout ctx (manifest timeout_ms; builtins 1.5s), panic recovery,
  per-provider 5s-throttled logging (throttle.go), focused boost
  added and clamped at 100, emit only with results and only while ctx
  is live -- emit runs on provider goroutines and MUST be
  goroutine-safe. SOURCE PRIORITY (placement metadata, NEVER a
  score): `prioritized` (engine.go, the watch backendInfo
  optional-extension pattern) is `priority(best match.Tier) int` --
  decided PER EMISSION from the strongest tier the engine minted
  (sourceResults returns it beside the rows; TierNone for external/
  empty answers); Emission gains Priority (json priority,omitempty)
  stamped in dispatchOne and CheatSheet via providerPriority (type
  assertion, absent = 0 whatever the tier). Only apps-search
  implements it (sourcePriorityApps = 1 when best <= strongTier =
  TierWordStart -- a STRONG match (triggered/exact/prefix/word-start)
  earns the above-file-results placement, a weak best (substring/
  fuzzy) emits at 0 and renders below the files: the macOS "test"
  field report, where scattered-subsequence app hits outranked a
  directory literally named "test"), and a PROMOTED emission is cut
  to its strong rows inside sourceResults (weak rows must never ride
  the promoted zone; they render below the files whenever no strong
  match exists, the whole section then being priority 0); the
  targeted apps provider stays 0
  (bang queries have no files to outrank), and external plugins can
  NEVER set it -- the wire Response has no priority field and
  *externalProvider does not implement the extension (pinned by
  TestSourcePriorityMetadata + TestExternalEmissionPriorityAlwaysZero
  + TestPriorityNeverChangesMintedScores: the mint is byte-identical,
  bands untouched; TestAppsSearchWeakMatchesStayBelowFiles +
  TestAppsSearchPromotedSectionStrongRowsOnly pin the tier gate).
  APP USAGE TIE-BREAK: Options.AppUsage (the app layer's frecency
  store behind a live accessor; nil = cold) feeds appCandidates'
  Candidate.TieBreak (decayed launch count x1000, usageTieBreak), so
  equal-tier equal-score app rows order by real usage before the
  name -- within a match class only, the tier stays the primary sort
  key (in the fuzzy band the alignment score still ranks first;
  usage breaks exact score ties). Keys: AppUsageKey(desktopID, argv)
  = "app:"+desktopID when a *.desktop id is stamped, else "app:"+
  argv joined with spaces (the darwin `open -a <bundle>` shape) --
  derivable identically from the snapshot (lookup) and the echoed
  action (record); AppPickKey(pluginID, action) gates recording to
  run_command launches from the two builtin app sources.
  Routing: resolved bang (exact/alias/unique-prefix)
  + space => ONLY that provider, all trigger gating bypassed;
  bare/partial/ambiguous or resolved-without-space sigil => ONLY the
  builtin suggestions provider; bang-shaped text with zero candidates
  => normal trigger fan-out on the raw query. `CheatSheet()` returns
  the suggestions provider's answer for a bare primary sigil as ONE
  synchronous Emission (Gen 0, no goroutines/fan-out; suggestions
  provider disabled = zero Emission) -- the app binds it for the
  frontend's empty-query cheat sheet. `Close()` drops idle
  HTTP connections; reload = build a new Registry, swap atomically,
  Close the old. Builtins (in-process, no sanitizer; targeted-only
  except apps-search, windows and the two Firefox providers):
  builtin_bangs.go "bangs"/Commands -- bang completions (resolved
  bang first, primary-sigil titles, typed-sigil set_query preserving
  the query rest, cap 12); builtin_app.go "app"/App Commands --
  !rescan/!reload/!config/!version/!quit, one run_builtin result each
  (version subtitle from Options.Version); builtin_apps.go
  "apps"/Launch -- !app/!launch over the Options.InstalledApps
  snapshot (empty query = all 15 listed, usage first then
  alphabetical, cap 15, run_command argv via `parseDesktopExec`:
  quotes, backslash escapes, %-field codes stripped; the shared
  candidate builder is `appCandidates`, whose
  actions carry DesktopID = the InstalledApp.ID ONLY when it is a
  bare *.desktop name (launch.ValidDesktopID -- the darwin scan's
  ".app" bundle ids used to fail the app layer's run_command
  re-validation and error every macOS launch) so linux launches keep
  activation credentials, whose TieBreak carries the AppUsage decayed
  launch count (see the registry bullet), and whose Results carry the
  internal-only IconKey "app:<Icon ref>" when the installed app has
  one -- the frontend's real-icon hook;
  AppUsageKey/AppPickKey/usageTieBreak live here too);
  builtin_apps_search.go "apps-search"/Apps -- installed apps in
  NORMAL results: no bangs, a real all_queries Trigger (match
  override on builtinBase, effective min 2 runes), the shared
  engine's canonical bands over the app name (words = letter/digit
  runs, so spaces, hyphens, dots split), cap 6, same run_command
  launch, and THE one prioritized source (priority(best) = 1 only at
  word-start tier or better -> its Emission renders above the file
  results with only its strong rows; weak bests emit at 0, below the
  files); bang routing keeps it
  exclusive with the targeted !app
  path, and a nil/empty snapshot emits nothing;
  builtin_openwindows.go "windows"/Open Windows -- also in the normal
  fan-out (no bangs; own all-queries match, min 2 runes of
  the trimmed query) over the Options.OpenWindows snapshot
  (plugin-local WindowInfo, ID as STRING to survive JSON), registered
  ONLY when that seam is non-nil (the app layer's session gate);
  ranking title word-start 85 > app prefix 80 > title substring 65 >
  app substring 60, ties alphabetical, cap 8, rows carry the
  internal-only activate_window action (icon "app", subtitle = app
  name);
  builtin_firefox.go "firefox-frequent"/Frequent Sites -- NO bangs,
  all-queries semantics (>= 2 trimmed runes, the shared
  allQueriesMatch helper), registered ONLY when Options.FrequentSites
  (the app-layer getter yielding []SiteInfo, a plugin-local mirror of
  internal/firefox.Site) is non-nil; scores
  host prefix 95 (leading "www." ignored) > title word-start 80 >
  host substring 70 > title-or-URL substring 60, ties by visit count
  then title, cap Options.FrequentSitesMax (<=0 -> 6); result =
  title-or-host / URL subtitle / icon "globe" / open_url action;
  builtin_tabs.go "firefox-tabs"/Open Tabs -- same NO-bangs
  all-queries semantics, registered ONLY when Options.OpenTabs (the
  getter yielding []TabInfo, mirror of internal/firefox.Tab) is
  non-nil; scores title word-start 85 > host prefix 80 ("www."
  ignored) > title substring 65 > URL substring 55 (the TITLE outranks
  the host here, unlike frequent-sites), ties by lastAccessed DESC
  then title, cap Options.OpenTabsMax (<=0 -> 6); result =
  title-or-host / URL subtitle / icon "link" (globe is taken) /
  "pinned" badge on pinned tabs / the action: a TabInfo.Token-carrying
  row (the app's ffext live snapshot supplied it) gets the
  internal-only activate_tab {Tab: token, Value: URL} SWITCH, a
  token-less (sessionstore) row keeps the byte-identical open_url --
  which re-OPENS the page (the README tab-switching section owns the
  user-facing story).
  Exhaustively
  unit-tested, table-driven, plus an end-to-end manifest ->
  registry -> /bin/sh transport dispatch test.
- `internal/launch` -- the pure decision half of "focus and raise on
  launch" (README "Focus and raise on launch" holds the user-facing
  capability matrix), exhaustively unit-tested headless; the
  OS/display glue lives behind internal/platform/native seams wired
  by internal/app launch.go. launch.go: `Target`/`ClassifyTarget`
  (URL = scheme+host parse, else file path; URI = file:// form;
  IsDir steers handler resolution to inode/directory), `Handler`
  (DesktopID/Exec/WMClass/Exe/DBusActivatable/StartupNotify/
  Terminal), `Credential` + kinds (none / x11-sn / wayland-gdk /
  wayland-xdg), `ShouldMint` (unresolved handlers always mint;
  resolved ones only with StartupNotify or DBusActivatable -- GLib's
  dangling-busy-cursor gating), `CredentialEnv` (DESKTOP_STARTUP_ID
  + XDG_ACTIVATION_TOKEN, BOTH carrying the same id -- launchees
  pick whichever their toolkit understands, and Firefox >= 108
  forwards it through its remoting), `LogLine` (the one-per-launch
  log format), `ValidDesktopID` (bare *.desktop name). exec.go:
  `ExpandExec` -- .desktop Exec tokenization (parseDesktopExec-
  compatible quoting) with target substitution: %f/%F = raw path
  (URL verbatim for URL targets, documented divergence), %u/%U =
  URI, %% literal, %i/%c/%k + deprecated codes drop, unknown codes
  keep their percent, NO target code = target appended last
  (xdg-open-style divergence from GLib's silent no-file launch), and
  an Exec whose program token does not survive a target-less
  expansion is unlaunchable (nil -- the target must never become
  argv[0]). dbus.go: `ApplicationDBusCall` derives the
  org.freedesktop.Application call (bus name = id sans .desktop,
  validated; path = "."->"/" + "-"->"_"; Open with URIs / Activate;
  platform-data = desktop-startup-id + activation-token) and
  `DBusActivate` performs it over a private never-autolaunched
  session-bus conn (ctx-bounded; the method call itself
  D-Bus-activates the service -- that IS the launch); tested against
  a throwaway dbus-daemon like internal/portal. watcher.go: the
  X-side raise watcher -- `XWindow`/`XState` (stacking order,
  bottom-to-top), `NewIdentity` (pid + startup id + lowercased
  WM_CLASS hints from StartupWMClass/exe base/argv0 base),
  `RunWatcher` polls (default 6s deadline / 200ms interval,
  ctx-bounded): an ACTIVE window matching the identity ends the
  watch silently (self-raised; never double-activate), a NEW window
  (not in the Before snapshot) matching pid/startup-id/class is
  activated once (topmost match wins), and at the deadline the
  most-recently-used EXISTING window matching the class hints
  (highest _NET_WM_USER_TIME, ties toward the top of the stack) is
  raised -- the editor-tab-into-running-instance fix -- else one
  quiet give-up log. sn.go: `SNRemoveMessage` (the libsn `remove:
  ID="..."` wire string, backslash-escaped, NUL-terminated) +
  `SNChunks` (20-byte ClientMessage chunks, last zero-padded) behind
  native.RemoveStartupSequence.
- `internal/preview` -- the preview-pane engine, pure (no Wails
  imports) and headless-tested. preview.go holds the wire contract:
  Target {kind "file"|"plugin"|"none", path, isDir, title, subtitle,
  pluginName} and Payload {gen, kind
  "meta"|"text"|"image"|"dir"|"web"|"ai"|"error", title, path, meta,
  text, image, dir, web, ai, err, durMs}. dispatch.go:
  `New(parentCtx, Options{TextMaxKB, ImageMaxEdge, DirMaxEntries,
  Emit, KagiAPIKey, KagiBaseURL, KagiMaxResults, OpenAIAPIKey,
  OpenAIBaseURL, OpenAIModel,
  OpenAIMaxOutputTokens, AICachePath, Logf})` -> Dispatcher (the
  base URLs go through normalizeBaseURL: empty = the client default,
  ONE trailing "/" trimmed, anything not http(s)-with-a-host leaves
  that provider UNAVAILABLE -- webErr/aiErr carry the terse
  invalid-baseUrl message the fetch path emits, and the URL value is
  never logged or emitted because it may carry userinfo);
  `Preview(target, gen)` is synchronous bookkeeping only (mutex'd
  cancel of the previous request + gen store; kind none/"" =
  cancel-only) and spawns ONE goroutine per request; file targets
  emit a FAST meta card first, then the rich payload (dir listing /
  capped text / thumbnail / a final meta card with a "binary" note)
  under per-request hard timeouts (2s meta/dir/text, 4s image, via
  runUnder racing the provider against the ctx); symlinks are
  described (readlink) and never followed; every emit is suppressed
  once the request ctx is cancelled; provider funcs are Dispatcher
  seam fields for tests (webFn/aiFn stay nil while the matching key
  is unconfigured -- WebConfigured()/AIConfigured() report it).
  `FetchWeb(query, gen)` / `FetchAI(query, gen)` are the explicit
  web/AI triggers sharing Preview's SAME cancel+generation space (a
  fetch supersedes an in-flight file preview and vice versa, via
  arm()): exactly ONE payload per accepted fetch -- kind "web"
  {query, results, cached} / "ai" {query, answer, model, cached} /
  "error" (blank query = "empty query"; no key = an error naming the
  config key + env fallback; invalid baseUrl = "kagi: invalid baseUrl
  (preview.kagi.baseUrl)" / "openai: invalid baseUrl
  (preview.openai.baseUrl / OPENAI_BASE_URL)"; provider failure; 10s
  web / 90s ai hard
  timeouts spelled out by fetchErrMsg). kagi.go: KagiClient
  (NewKagiClient(key, maxResults); BaseURL/HTTPClient/Now exported
  seams -- BaseURL doubles as the production preview.kagi.baseUrl
  override) -- Kagi Search API v1 verified 2026-07-18: GET
  {base}/api/v1/search?q=&limit=, header `Authorization: Bot <key>`,
  response data.search rows {url,title,snippet} (the deprecated v0
  flat data array with t==0 rows is still accepted on parse);
  Search(ctx, q) -> (results, cachedBool, err) with an exact-query
  TTL cache (15min, 100 entries, oldest-inserted evicted; hits =
  zero network + no token spend) and a client-side token bucket
  (burst 3, refill 1/s; empty = "kagi: rate limited, retry shortly"
  WITHOUT dialing); non-2xx = "kagi: HTTP <code>" + at most a
  200-char parsed error message -- never the raw body, never the key.
  openai.go: OpenAIClient (NewOpenAIClient(key, model,
  maxOutputTokens); same exported seams) -- OpenAI Responses API
  verified 2026-07-18: POST {base}/v1/responses `Authorization:
  Bearer <key>` {"model","input","max_output_tokens"}; Ask(ctx,
  prompt) -> (answer, resolvedModel, err) concatenating output[]
  "message" items' "output_text" parts (top-level output_text is
  SDK-only per the docs; read as a defensive fallback), status
  "incomplete" appends a "[truncated by max_output_tokens]" marker
  line ("[truncated: content_filter]" for that reason; marker-only
  answers are legal -- reasoning models can spend every token before
  emitting text), API-error JSON {"error":{"message"}} -> terse
  capped error. aicache.go: AICache -- the persistent AI answer LRU
  on internal/history's atomic pattern (lazy one-shot Load: missing =
  empty+nil, corrupt = empty + error, logged once via the Logf seam;
  temp-file+rename 0600 writes, MkdirAll parent; in-memory updates
  even when the write fails; "" path = memory-only): {"v":1,
  "entries":[{k,model,prompt,answer,at}]} at Options.AICachePath, k =
  sha256 hex of model+NUL+FULL prompt (the stored prompt is capped
  2KB, answer 32KB), Get(model,prompt) refreshes recency (At),
  Put evicts past 128 entries by oldest At; hits emit Cached:true
  with zero network. cache.go: bytes-bounded LRU of rich payloads
  (16 MiB / 64 entries; key = path + mtime + size + provider kind);
  hits skip the meta emission. text.go: IsBinary (NUL or >30% bad
  bytes), ReadCapped (maxKB, ToValidUTF8-sanitized), LangHint (~35
  extensions + Dockerfile/Makefile name matches -> highlight.js
  names). image.go: Thumbnail -- extension gate
  (png/jpg/jpeg/gif/webp/bmp), 32 MiB source + 40-megapixel
  DecodeConfig gates, decode raced against ctx, x/image/draw
  ApproxBiLinear downscale to maxEdge, JPEG q80 for JPEG sources else
  PNG, base64 data URI. dir.go: ListCapped (dirs first,
  case-insensitive, capped, entry.Info sizes, never recurses).
  meta.go: MetaFor (humanized size, mtime, mode, kind guess, path +
  extra rows). Wired by internal/app's preview.go: Options.Preview
  (config section) gates startPreview, which resolves each API key
  ONCE -- config value, else the env var through the getenv seam
  (KAGI_API_KEY / OPENAI_API_KEY), exactly the resolution
  GetPreviewConfig reports -- resolves the OpenAI base URL the same
  way (preview.openai.baseUrl else OPENAI_BASE_URL; the Kagi base is
  config-only, no env) -- and passes <configDir>/aicache.json
  (config.Dir() failure = one log line + memory-only cache); the
  keys and base URLs flow only into preview.Options, never into logs
  or payloads;
  bound methods QueryPreview(target, gen) / GetPreviewConfig()
  (enabled + kagi/openai configured + resultsWidth = the flag-off bar
  width, Options.ResultsWidth wired from config window.width in
  main.go with a DefaultWindowWidth fallback when unset; keys never
  exposed) /
  FetchWebPreview / FetchAIPreview (gen store + dispatcher FetchWeb/
  FetchAI; nil dispatcher = no-op, so the frontend's Ctrl+K / Ctrl+I
  strip is the ONLY call path and nothing automatic ever dials);
  emissions
  ride the "preview:result" event behind the previewGen atomic gate
  (the QueryPlugins pattern); Shutdown cancels the dispatcher's
  parent ctx. previewsize.go `PreviewWindowSize()` (translucent.go
  pattern: fresh config.Load, any error = disabled) tells main.go the
  window size BEFORE wails.Run; flag off = the configured base size
  (window.width/height, defaults 780x550 -- the WindowSize read),
  flag on = preview.windowWidth/Height, threaded into
  Options.WindowWidth/WindowHeight for the positioning math
  (App.windowSize()).
- `internal/progress` -- the startup progress printer behind the
  initial index build's "indexing..." line, pure and
  headless-tested. `New(w, tty, logf)` -> `Printer`: on a TTY the
  line redraws IN PLACE (plain "\r" + space padding, no ANSI escapes;
  self-stamped like a default log line) and the Printer implements
  io.Writer so the app can log.SetOutput(printer) -- an intercepted
  log write erases the line, writes the log bytes, and redraws when
  the write ends in '\n', so ordinary logging never tears the display
  (the Printer writes to the raw stream itself, never through log --
  no recursion); off a TTY `Indexing` appends plain lines through
  logf (usually log.Printf) at most one per 5s, nil logf = dropped.
  `Indexing(entries)` renders "index: indexing... N entries, X ram"
  with the process RAM figure resampled at most once per second;
  `Done()` erases and resets all render/throttle state (safe when
  nothing rendered); `TTY()`; `IsTerminal(*os.File)`; mem.go `RAM()`
  (platform CURRENT footprint via rss_{linux,darwin,windows}.go --
  linux /proc/self/statm, windows WorkingSetSize, darwin mach
  task_info TASK_VM_INFO phys_footprint (Activity Monitor's figure)
  through the package's ONE cgo file footprint_darwin.go (the
  sysstats readers_darwin.go pattern; darwin builds need cgo, which
  Wails already requires) with getrusage ru_maxrss -- the PEAK, in
  bytes on darwin -- as the mach-failure fallback; runtime Sys
  fallback) / `RAMString()` / `FormatBytes` (decimal MB/GB, one
  decimal). All methods goroutine-safe. Consumed by internal/app's
  progress.go (the `newProgress` seam).
- `internal/icons` -- result-row icon resolution to data URIs behind
  the app's bound ResolveIcons, pure and headless-tested (every input
  dir and external command sits behind Options seams). Key protocol
  (the frontend wire contract): "dir", "file:<basename>",
  "app:<ref>". NewService does NO IO; the first Resolve pays
  initialization (mime-db load + gsettings/settings.ini theme
  detection), and everything is served through a positive + negative
  (name|size)->URI LRU (512 entries each) under one mutex.
  Linux/freedesktop half (#37): theme.go/lookup.go/mimedb.go -- the
  detected GTK theme + Inherits chain + Adwaita/hicolor, exact size
  match then closest, then unthemed/pixmap fallbacks; absolute
  .png/.svg refs served directly; 1 MiB MaxFileBytes cap. Darwin half
  (bundle.go + plist.go + icns.go): an "app:" ref that is an ABSOLUTE
  path ending ".app" (case-insensitive -- the ref SHAPE selects the
  branch, so fixture bundles test it on any OS) resolves
  Contents/Info.plist -> CFBundleIconFile (".icns" appended when
  extension-less; separators/".."/non-.icns rejected) ->
  Contents/Resources/<file> -> icnsBestPNG. plist.go is a
  hand-rolled bounded bplist00 reader (trailer + offset table + dict/
  ASCII/UTF-16 strings/int extended counts ONLY -- the mozLz4
  precedent, no plist dep; caps 65536 objects / 4096-rune strings)
  plus an encoding/xml fallback matching the same root-dict-only
  semantics (nested decoys never match). icns.go walks the container
  (4CC + BE length entries) and passes through the best PNG-magic
  payload -- smallest nominal size covering the want, else largest
  below, else unknown-size band; NO image decoding ever -- skipping
  legacy RLE/JPEG-2000 payloads and entries over 512KB
  (maxIcnsEntryBytes; plist capped 4 MiB, icns container 32 MiB via
  stat-first readCapped). CFBundleIconName-only (Assets.car) apps
  are a DELIBERATE miss (negative-cached -> glyph fallback; est.
  5-15% of /Applications, measured by the darwin-only
  real_darwin_test.go which runs un-gated on the mac job and fails
  only when a POPULATED /Applications resolves nothing). Consumed by
  internal/app icons.go (the newIcons seam).
- `internal/appctx` -- app-context collection for the plugin system,
  pure and headless-tested: the data types (AppInfo / InstalledApp
  (incl. Icon -- the platform icon ref: .desktop Icon= on linux, the
  .app bundle path on darwin, empty on windows) /
  WindowInfo (ID uint32/Title/App/PID) /
  Snapshot -- deliberately NOT internal/plugin's wire types, the app
  layer converts), the `Source` seam (FocusedApp/RunningApps/
  InstalledApps/OpenWindows) implemented by
  internal/platform/native, and `Cache` (mutex-guarded, injectable
  clock): `CaptureFocused` = synchronous focused-app read at
  hotkey-press BEFORE the window steals focus;
  `RefreshRunningAsync` / `RefreshInstalledAsync` /
  `RefreshWindowsAsync` = single-flight
  background refreshes that never block callers and keep old data on
  failure; `EnsureFreshInstalled(ttl)` re-kicks only when the last
  SUCCESSFUL installed refresh is older than ttl; `Snapshot()` =
  immutable copies. A zero-value or nil-Source Cache no-ops
  everything (degraded). desktop.go = XDG .desktop scanning with
  injectable dirs (`DesktopDirs(getenv)`: $XDG_DATA_HOME else
  ~/.local/share, then $XDG_DATA_DIRS else
  /usr/local/share:/usr/share, each + /applications, deduped;
  `ScanDesktopDirs`: flat per-dir scan of *.desktop files, [Desktop
  Entry] needs Type=Application + non-empty Name/Exec,
  NoDisplay/Hidden/Terminal skipped, Exec kept RAW for the plugin
  layer's parser, ID = file name, earlier dirs shadow later ones BY
  PRESENCE (a Hidden local copy disables a system app), localized
  Name[xx] ignored, sorted by Name). proc.go = `ProcInfo(procRoot,
  pid)` readlink exe + trimmed comm, each empty on error (cross-user
  /proc exe readlink fails; expected). proctree.go = `ProcTree`
  (NewProcTree(root)), the production process-tree SNAPSHOT behind
  the frecency cwd derivation (structurally satisfies
  frecency.ProcTree; this package deliberately imports frecency only
  in tests): Children from ONE memoized scan of every <root>/N/stat
  ppid (parse after the LAST ')' -- comm may hold spaces/parens;
  capped 8192, child lists sorted), Cwd = readlink <root>/N/cwd,
  Foreground = bounded BFS for the first positive stat tpgid (the
  terminal's foreground process group; a plain GUI tree has none).
  The app builds a FRESH one per capture (plat.procTree factory), so
  the memoized scan can never go stale; fixture-dir tested,
  including the DeriveCwd end-to-end pair.
- `internal/firefox` -- the Firefox data layer (frequent sites + open
  tabs), pure and headless-tested (fixture profiles.ini trees, fixture
  places.sqlite databases AND fixture recovery.jsonlz4 snapshots BUILT
  IN THE TESTS -- the latter via a test-only literals-only mozLz4
  compressor; injectable now/clock/fetch/mtime seams).
  profiles.go: `BaseDirs(goos, home, getenv)` = the probe order
  (linux: classic ~/.mozilla/firefox, snap
  ~/snap/firefox/common/.mozilla/firefox -- Ubuntu 22.04's default --
  flatpak ~/.var/app/org.mozilla.firefox/.mozilla/firefox; windows
  %APPDATA%\Mozilla\Firefox; darwin best-effort) +
  `FindProfile(bases)`: per base, profiles.ini resolves to ONE
  profile ([Install*] Default= wins, then [ProfileN] Default=1, then
  a lone [ProfileN]; IsRelative=1 joins against the base, missing
  IsRelative inferred from the path; the resolved dir must exist);
  multiple bases = newest places.sqlite mtime wins, earlier base
  breaks ties; ok=false = caller degrades. places.go:
  `FrequentSites(ctx, profileDir, QueryOptions{MinMonth, MinWeek,
  Now, Limit})` NEVER opens the live db (Firefox holds it locked,
  WAL): copies places.sqlite + places.sqlite-wal to a fresh temp dir
  (chunked, ctx-abortable), opens the COPY read-only via pure-Go
  modernc.org/sqlite (driver "sqlite" -- windows/amd64 must keep
  cross-compiling, so never swap in a cgo driver), one grouped query
  (visit_date is MICROSECONDS since epoch; hidden=0, http(s)-only,
  visit_type NOT IN (4,8) i.e. EMBED/FRAMED_LINK excluded; HAVING
  c30 >= MinMonth AND c7 >= MinWeek, ORDER BY c30 DESC, LIMIT
  default 200), host parsed in Go (net/url Hostname; empty host =
  row dropped), temp dir removed on the way out. cache.go: `Cache`
  (NewCache(ctx, CacheOptions)) = the appctx.Cache pattern for
  sites: `Sites()` returns an immutable copy immediately and
  single-flight-kicks ONE background refresh when stale (success
  schedules the next attempt a TTL away, default 10m; failure keeps
  old data, logs once per DISTINCT message, retries no sooner than
  1m so a broken profile is not re-copied per keystroke); every
  refresh goroutine is bounded by the constructor ctx (cancelled =
  no new kicks, in-flight fetch aborts quietly). mozlz4.go:
  `DecodeMozLz4(data, maxSize)` -- Firefox's .jsonlz4 container
  (8-byte "mozLz40\0" magic + LE uint32 uncompressed size + raw LZ4
  BLOCK format, no frame) with a hand-written ~80-line block decoder:
  token nibbles, 0xFF-chained length extensions, byte-by-byte FORWARD
  match copies (offset < length self-replication is legal), every
  read bounds-checked, the block must produce EXACTLY the declared
  size, declared sizes over the cap (default 64 MiB) rejected as
  corruption. sessionstore.go: `ReadOpenTabs(profileDir)` reads
  sessionstore-backups/recovery.jsonlz4 (rewritten ~15s by a RUNNING
  Firefox; private windows never persisted) -> []Tab{URL, Title,
  Host, Pinned, LastAccessed(ms)}: hidden tabs skipped, entries[index-1]
  is the current page (1-based index clamped into range, entry-less
  tabs skipped), http(s)-with-host only, raw cap 500; a MISSING file
  = (nil, nil) -- browser closed, deliberately NO
  sessionstore.jsonlz4 fallback (those tabs are not open) -- while
  corrupt/unreadable files are errors; `RecoveryMTime` = the cheap
  staleness probe. tabcache.go: `TabCache` (NewTabCache(ctx,
  TabCacheOptions)) = the Cache pattern with an mtime gate: `Tabs()`
  serves the snapshot immediately; a due probe (>= 1s apart) stats
  the file and re-reads ONLY when the mtime changed or the last read
  is older than the TTL (default 15s, matching Firefox's write
  cadence -- no config knob); success MAY legitimately store an
  empty list (closed browser), failure keeps old data with the same
  once-per-distinct-message logging and 1m retry gap, ctx bounds
  every goroutine. Consumed by
  internal/app's firefox.go + the plugin registry's firefox-frequent
  and firefox-tabs builtins -- where the open-tabs getter now serves
  the internal/ffext live bridge snapshot FIRST (connected + fresh
  within the same 15s bound) and this sessionstore layer is the
  always-there fallback.
- `internal/watch` -- keeps the index live after the initial walk;
  three cooperating tiers whose CONTRACT is identical final index
  state, differing only in latency (pinned by TestTierEquivalence*).
  Event model (2026-07 redesign): an event is only a DIRTY PATH -- op
  codes are advisory (consulted once at intake to drop Write/Chmod)
  and lstat at apply time decides: gone -> `Manager.Remove` (subtree
  tombstone) + watches under the path dropped; file -> `Manager.Add`
  (a dir->file flip first tombstones the old subtree -- AddEntry only
  flips the bit); dir -> Add + `reconcileDir` = shallow readdir diff
  vs `Manager.ChildrenOf`, recursing (scanNewDir) ONLY into
  index-unknown children, kind flips tombstoned+re-added, missing
  children removed -- so application is order-independent by
  construction (fanotify-style merged events plug in) and the sweeper
  feeds the same reconcile with paths that never had events. `Watcher`
  (watch.go = types/lifecycle/state helpers, events.go = the run loop
  + reconcile engine, hotset.go = the hot-set bookkeeping split out
  for the 750-line cap): a bounded HOT SET of fsnotify watches --
  fsnotify uniform on ALL platforms, never recursive.
  `Options.MaxWatches` (config watcher.maxWatches -> app.Options
  .WatchMaxWatches): 0 = auto via TWO per-OS seams (`readMaxWatches`
  raw limit + `autoBudget` formula, production bindings in
  budget_{linux,darwin,other}.go; both formulas live untagged in
  watch.go so every job tests both): linux = autoBudgetInotify
  (min(max_user_watches/2, 65536), floor 1024), darwin =
  autoBudgetDarwinFD over readFDLimit (fdlimit_darwin.go, read-only
  Getrlimit -- NEVER add a Setrlimit: the Go runtime already raises
  the soft limit at init and restores the original in exec'd
  children; min(RLIMIT_NOFILE/16, 8192), floor 256, /16 because
  kqueue opens one fd per watched dir PLUS one per direct child
  file -- the unbudgeted model pinned a field machine at its fd
  ceiling), elsewhere/read failure = unlimited watch-everything;
  negative = unlimited. `FormatBudget` renders math.MaxInt as
  "unlimited" in BOTH budget log lines (events.go fill summary +
  the app summary), never the raw digits.
  `Options.WatchEx` (config watcher.watchExcludes; a SECOND
  index.Excluder distinct from the walk one): matching dirs AND
  their whole subtrees (watchExcluded walks ancestors -- the walk
  excluder gets subtree coverage from pruning, watch-excluded trees
  stay indexed so it must be reproduced) are skipped at every
  watch-issuing point (fill, event/sweep promotion, cold refill,
  resync want-set, root pinning) and leave Stats.IndexedDirs, but
  stay fully indexed + swept -- staleness bound = the sweep
  interval; nil = one nil-check on the hot path. Fill
  priority (addInitialWatches + budget-aware syncWatches refill):
  roots first (pinned, always watched, never evicted), then dirs
  under the `homeDir` seam (os.UserHomeDir) to 75% of budget, then
  the rest; fills use cold adds (at budget: NO syscalls issued --
  beyond-budget dirs stay cold for sweeps, no failing-syscall storm).
  Recency is a container/list LRU: touches = addWatch/refreshWatch on
  a watched dir, reconcile touching a watched parent (map hit only --
  file events never promote cold parents), and `promote(dir)` = watch
  with eviction (reconcileDir's refreshWatch promotes sweep-found
  dirs); at budget a new hot dir evicts the least-recently-touched
  (Stats.Evictions -- NOT degradation; DroppedWatches stays strictly
  "the OS refused"). Events are debounced (debounce.go: dirty-path
  set, quiet ~250ms / oldest ~1s / 4096 cap; injectable). Excluded
  paths filtered with the SAME `index.Excluder` as the walks. The
  notifier seam (notify.go; optional `backendInfo` extension = kind()
  name + wideCoverage) keeps unit tests scripted; integration
  tests run real inotify/kqueue. The production per-dir `fsnotifier`
  implements backendInfo too: kind() = (`PerDirBackendName()`, false)
  -- the HONEST per-OS label ("inotify" linux, "kqueue" darwin+BSDs,
  "windows" windows; also the New-time Stats default), exported for
  app_test's per-GOOS assertions. BACKEND SELECTION: New binds
  `newBackendNotifier(Options.Backend, normalized roots)` (notify.go;
  config watcher.backend -> app.Options.WatchBackend): "inotify" =
  plain fsnotify on every OS, no whole-filesystem probe; "fanotify"
  and "fsevents" = STRICT `newStrictFanotifyNotifier` /
  `newStrictFSEventsNotifier` (per-OS: fanotify_linux.go +
  fsevents_darwin.go carry their own-OS strict + auto selections,
  fanotify_other.go is now `!linux && !darwin`, fsevents_other.go =
  `!darwin`; off its OS each strict mode is the loud
  always-unavailable noop) -- constructor failure = one LOUD
  'backend "..." required by config but unavailable ... live
  watching DISABLED' line + the no-op `noopNotifier` (notify.go:
  accepts everything, delivers nothing, kind ("none", wide) so
  Watched/IndexedDirs stay 0 and addInitialWatches logs no
  coverage-active line for it; sweeps converge), NEVER a per-dir
  fallback; anything else = `newAutoNotifier` -- linux tries
  fanotify, darwin tries fsevents, windows/BSDs go straight to
  per-dir fsnotify; ANY constructor error = one log line +
  per-directory fallback. The `newFanotifyFn` / `newFSEventsFn`
  package vars are the constructor seams the selections probe
  (scripted in tests, no privileges needed). FSEVENTS BACKEND
  (fsevents_darwin.{go,h,c} cgo over CoreServices +
  fsevents_events.go, the UNTAGGED pure half unit-tested on linux
  CI too): ONE FSEventStreamCreate over the roots'
  EvalSymlinks-RESOLVED spellings (FileEvents|NoDefer, sinceNow,
  latency 0.3s, callbacks on a private serial dispatch queue via
  FSEventStreamSetDispatchQueue -- no run loop), a cgo.Handle
  trampoline (launchmint pattern) feeds handleBatch -> `fseDecide`
  per record: overflow flags (MustScanSubDirs/UserDropped/
  KernelDropped/IdsWrapped) -> the fsnotify overflow sentinel
  (degrade + sweep) AND MustScanSubDirs still emits its subtree
  root; content/metadata-only flags dropped (the Write/Chmod
  analogue; flags==0 KEPT, fail open); `fsePathTranslator` maps
  resolved prefixes back to configured spellings (/tmp ->
  /private/tmp forking guard); paths outside the roots dropped
  (stream-on-"/" sees everything). Close ordering is load-bearing:
  closed flag -> FSEventStreamStop/Invalidate/Release -> dispatch
  queue DRAIN (dispatch_sync_f) -> only then cgo.Handle delete +
  channel closes. fsevents_darwin_test.go runs REAL FSEvents
  un-gated on the mac job (delivery incl. symlink translation,
  watcher-level convergence, scripted selections, handleBatch
  overflow paths -- the integration twin CI's unprivileged fanotify
  cannot have). fanotifyNotifier: ONE
  FAN_CLASS_NOTIF|FAN_REPORT_DFID_NAME|FAN_CLOEXEC|FAN_NONBLOCK
  group; FAN_MARK_FILESYSTEM marks (mask CREATE|DELETE|MOVED_FROM|
  MOVED_TO|ONDIR; FAN_RENAME deliberately unused) on every root's
  filesystem -- ANY root-mark failure (EPERM without CAP_SYS_ADMIN,
  ENODEV null fsid, EXDEV) fails the WHOLE constructor so the
  fallback takes over cleanly (no mixed-backend watcher in v1) --
  then best-effort marks per extra real mountpoint under the roots
  (index.RealMountpoints; a refused mount logs once and is left to
  sweeps: coverage holds, latency differs). Events: kernel reports
  (parent-dir file handle, name); the read loop routes the handle by
  fsid to that superblock's O_PATH mount fd (a handle resolves ONLY
  against its own fs), open_by_handle_at + readlink /proc/self/fd ->
  parent path (needs CAP_DAC_READ_SEARCH; ESTALE = parent gone =
  drop), joins the name, filters to the configured roots (whole-sb
  marks see outside paths; the index scope never widens), resolving
  each (fsid, handle) once per read batch (deliberately NO
  cross-batch cache in v1: a persistent LRU needs rename/delete
  invalidation to stay truthful), emits advisory
  fsnotify.Create -- reconcile-by-lstat absorbs merged masks. Full
  events channel (1024) drops + synthesizes ErrEventOverflow;
  parsing lives in fanotify_parse_linux.go (bounds-checked
  DFID_NAME record walker, unit-tested on synthetic buffers); ALL
  syscalls sit behind seam fields (init/mark/read/resolve/fsid/
  mounts) so routing/dedup/overflow/shutdown logic tests run
  unprivileged, plus a capability-gated integration test (t.Skip
  without CAP_SYS_ADMIN; skipped in CI -- the documented coverage
  limitation). `MarkMount(path)` extends coverage to
  sweeper-discovered mounts (unmarking on unmount is NOT
  implemented; the stale mark pins a little kernel memory until the
  group closes). Under wideCoverage the Watcher sets `wide`: hot-set
  fill, bookkeeping, and every per-directory watch call become
  no-ops (Watched/IndexedDirs stay 0). Degradation (never crash,
  never spin): refused watch = counted+logged once; event-queue
  overflow = lost events -> Sweeper.Request when wired, else
  Rescanner fallback; OnDegraded edge-triggered once -> app's
  "watch:degraded".
  Stats{Backend "inotify"|"kqueue"|"windows" (per-dir, per-OS honest)
  |"fanotify"|"fsevents" (wide)|"none" (strict mode refused: no
  live watching, sweeps only), Budget, WatchedDirs,
  IndexedDirs, DroppedWatches, Evictions, Overflows, Degraded};
  `InitialRegistration()` closes when the first fill finished (the
  app waits on it before its summary log). DEFERRED START
  (StartDeferred/Release, the app's register-before-index ordering):
  StartDeferred = Start with the fill and ALL application HELD --
  the notifier is live immediately (wide marks cover everything, the
  per-directory model watches just the configured roots), the run
  loop's hold phase (collectUntilRelease in events.go) drains events
  into the debouncer's dirty set WITHOUT applying (deduped, bounded
  by the unexported holdCap, default 65536; new paths beyond it are
  dropped + latched), and Release (idempotent; wire the
  Sweeper/Rescanner first) lets the loop run the normal fill (against
  the CURRENT index -- the app releases after the fresh-store swap)
  then flush the held set through the ordinary reconcile;
  reportHoldLoss converges any hold loss (cap drops count+log+degrade
  as an overflow; overflows that fired while requesters were unwired
  re-kick) via one sweep request. Stop works held or released
  (the hold phase exits on ctx cancel and the fill/flush no-op);
  deferred_test.go pins mid-build-events-reach-final-index,
  roots-watched-immediately, cap-loss-degrades-and-sweeps,
  overflow-resweep-at-release, stop-without-release, and
  Release-as-no-op-on-plain-Start. `Sweeper` (sweep.go): the
  always-on convergence tier -- NewSweeper(m, w != nil, SweepOptions
  {Interval 20m default, MinGap 1m, InitialWatermark (zero = first
  pass re-lists EVERY dir; the app passes build-completion time),
  StatsPerSec 50000 sleep-throttle, unexported `mounts` seam
  (default index.RealMountpoints over the roots)}). One pass:
  mount-table snapshot
  under the roots diffed vs the previous pass (symmetric difference
  force-reconciled -- mount-onto-existing-dir moves no mtime,
  unmounts restore content silently; an APPEARED mountpoint gets
  Watcher.markMount first, so a fanotify backend marks the new
  filesystem before its content is indexed), then the roots (no index entry
  of their own: routed to reconcileDir directly, a full reconcile
  would invent one), then every live indexed dir via
  Manager.LiveDirsPage(4096): lstat each; gone or mtime >= watermark
  - 2s slack -> reconcile (Relisted), else skip (Swept). The
  watermark advances to the pass's start ONLY on completion --
  cancelled passes redo the window; mtime-BACKDATED mutations (tar
  --preserve) are the documented miss, converging via full re-list /
  rescan / !rescan. SweepStats{Completed, Cancelled, Running,
  LastStart, LastDuration, Swept, Relisted}. `Rescanner` (rescan.go):
  serialized full rebuilds -- `Manager.BuildFromDisk` (fresh-store
  swap; queries never block) then budget-aware `syncWatches` --
  triggered by an optional interval ticker (config
  `rescanIntervalMinutes`) and one-shot requests, coalesced through a
  1-slot channel, spaced by MinGap (default 30s). Stop cancels
  promptly at ANY point on all three loops (fast quit): in-flight
  rebuild aborts mid-walk, syncWatches stops between dirs, sweep
  passes abort between dirs and inside throttle sleeps, MinGap waits
  cut short, queued requests dropped. All three loops share the
  lifecycle.go Start/Stop plumbing: idempotent Stop, safe
  before/during Start, no goroutine leaks. App wiring: startWatch
  builds the watch-only excluder (bad watcher.watchExcludes pattern =
  log + nil), passes app.Options {WatchMaxWatches, WatchEx} into
  watch.Options, builds watcher + rescanner + sweeper (SweepOptions
  .Interval = app.Options.SweepInterval, 0 -> the app-side 20m
  default) -- EXCEPT under Options.SweepDisabled (config
  watcher.sweepDisabled): the Sweeper is never built and ONE loud
  warning says unwatched dirs now converge only at full rescans
  (overflow recovery then falls back to the Rescanner request path)
  -- starts them in that order, waits
  for InitialRegistration, then logs ONE summary ("watch: backend %s:
  %d/%d dirs live-watched (budget %d); sweep interval %s; full rescan
  interval %s" -- sweep interval reads "disabled" when off); Shutdown
  stops rescanner, then sweeper (nil-tolerated), then watcher
  (the sweeper reconciles through the watcher). measure_test.go is
  the env-gated watcher measurement harness (the internal/index
  gated-bench pattern: BENCHMARK phase, b.N ignored, skip unless
  COMPETENT_SEARCH_WATCH_MEASURE=1, knobs _DIRS/_STORM/_ROOT/_OUT)
  backing the PR-body registration/storm/idle numbers.
- `internal/portal` -- XDG Desktop Portal GlobalShortcuts client over
  godbus/dbus/v5 (direct dep), the Wayland-native global-hotkey path.
  PURE D-Bus client, deliberately NO app wiring yet. `Dial` (private
  session-bus conn; the caller owns Close -- dropping the conn ends
  the portal session), `Available` (fast probe:
  org.freedesktop.portal.Desktop has an owner + GlobalShortcuts
  `version` property >= 1; distinct wrapped ErrNoPortal vs
  ErrNoGlobalShortcuts), `TriggerString` (platform.Hotkey ->
  shortcuts-spec syntax: CTRL/SHIFT/ALT/LOGO modifiers + xkbcommon
  keysym names, alt+space -> "ALT+space", enter -> "Return";
  unmappable = error), `Register(ctx, conn, Options{ShortcutID,
  Description, PreferredTrigger, OnActivated})` -> `*Session`.
  Register follows the portal Request convention: subscribe on the
  PREDICTED /request/SENDER/TOKEN path (crypto/rand tokens) BEFORE
  each call, falling back to the returned handle; then CreateSession
  (session_handle typed "s" -- documented erratum) -> ListShortcuts ->
  BindShortcuts ONLY when the id is not already bound (a session may
  attempt binding exactly ONCE; the portal remembers approvals across
  sessions) -> Activated dispatch filtered to this session handle +
  shortcut id (Deactivated ignored). Response code 1 = ErrDenied,
  2 = portal error; create/list wait 25s, bind 5min (interactive
  approval dialog), all ctx-abortable. Signal channels are BUFFERED
  (godbus silently drops on a full channel). Session exposes
  BoundDescription + Handle() for logging; Close() = best-effort
  org.freedesktop.portal.Session.Close + match removal, idempotent,
  never closes the conn. Tested headlessly against an in-package fake
  portal service on a throwaway `dbus-daemon --session` per test
  (spawned and killed strictly by PID; t.Skip when the binary is
  absent -- present in CI's ubuntu-24.04). Consumed by internal/app's
  hotkey.go (startPortalShortcut: Dial -> Available -> TriggerString
  -> Register with ShortcutID "toggle", which must stay stable across
  runs -- the portal keys remembered approvals on it).
- `internal/tray` -- the tray icon: org.kde.StatusNotifierItem +
  com.canonical.dbusmenu implemented DIRECTLY over godbus (no cgo, no
  GTK/libappindicator -- nothing fights Wails for a main loop), pure
  and headless-tested like internal/portal. `New(Options{ID, Title,
  Tooltip getter, Menu []MenuItem{Label,Separator,OnClick},
  OnActivate, Logf})` + `Start(ctx)`: Dial opens a PRIVATE session-bus
  conn via dbus.SessionBusPrivateNoAutoStartup (NEVER autolaunches a
  dbus-daemon; no bus = one quiet log line, Start returns nil,
  degraded); export of /StatusNotifierItem (methods
  Activate/SecondaryActivate/XAyatanaSecondaryActivate -> OnActivate,
  ContextMenu/Scroll no-ops) + /MenuBar + org.freedesktop.DBus
  .Properties on both (the GNOME extension reads EVERYTHING via
  GetAll and needs Id+Menu before it shows anything) + Introspectable
  on /, /StatusNotifierItem and /MenuBar (the extension's brute-force
  item scan walks the introspection tree from "/"); RequestName
  org.kde.StatusNotifierItem-<pid>-1 (KDE convention, best-effort);
  registration calls StatusNotifierWatcher.RegisterStatusNotifierItem
  with the OBJECT PATH "/StatusNotifierItem" -- the v42 extension
  (Ubuntu 22.04) resolves a leading "/" against the sender directly,
  while a bus-name argument takes an async name resolution that can
  fail; a NameOwnerChanged watch (buffered chan, portal precedent)
  re-registers whenever org.kde.StatusNotifierWatcher gains an owner
  (GNOME Shell restart, extension reload, host appearing after a
  degraded start -- "no StatusNotifierItem host" is one log line, not
  an error). SNI props: Category ApplicationStatus, Status Active,
  ItemIsMenu false, Menu /MenuBar, IconPixmap ONLY (no IconName: the
  extension prefers a set name, mangles it with a "-panel" suffix and
  warns per failed theme lookup -- the pixmap renders
  deterministically); the icon is a magnifier DRAWN IN CODE (icon.go,
  analytic coverage rasterizer, stdlib math only, no assets) at
  22/24/48 px in ARGB32 network byte order (bytes A,R,G,B, straight
  alpha -- v42 argbToRgba parses exactly that; rgba.go's exported
  `MagnifierRGBA(size)` is the one PREMULTIPLIED-RGBA variant of the
  same rasterizer, consumed by the darwin Dock icon in
  internal/platform/native panel_darwin.go); ToolTip
  (sa(iiay)ss) carries Title + the summon-shortcut text, re-read from
  the Tooltip getter at every (re-)registration and announced via
  NewToolTip on change (GNOME's extension ignores tooltips; KDE
  shows them). dbusmenu: static tree, root 0 children ids 1..n,
  revision pinned 1, Version 3 (libdbusmenu's value), GetLayout
  honoring recursionDepth + propertyNames filter (the extension calls
  GetLayout(0,-1,["type","children-display"]) then
  GetGroupProperties(ids,[]) for the rest), GetProperty, Event
  ("clicked" -> OnClick; opened/closed/hovered ignored; unknown id =
  dbus error), EventGroup (unknown ids reported back), AboutToShow
  false, AboutToShowGroup. Close() cancels the watch goroutine,
  closes the conn (which unregisters the item), idempotent + nil-safe
  + bounded. Tested against a fake org.kde.StatusNotifierWatcher on a
  throwaway dbus-daemon (spawn/kill by captured PID, t.Skip without
  the binary): registration argument, GetAll host-side reads, full
  dbusmenu surface, watcher-restart + late-host re-registration,
  tooltip refresh, close/cancel goroutine hygiene. Test gotcha:
  dbus.Store REUSES a non-nil dest slice's backing array and MERGES
  into existing maps -- always decode into fresh variables.
- `internal/gsettings` -- the GNOME custom-keybinding fallback for
  Wayland GNOME sessions whose portal lacks GlobalShortcuts (GNOME <
  48, e.g. Ubuntu 24.04/GNOME 46): pure logic over an injectable
  `Runner` seam (production `Run` execs the gsettings CLI, no shell,
  3s/call timeout, stderr folded into errors; unit tests script argv
  -> output). `ConvertHotkey` maps platform.Hotkey to GTK accelerator
  syntax (<Control><Alt>space; keys per gdk_keyval_from_name: space,
  lowercase letters/digits, F1, Return, Escape, Tab, Up/Down/...);
  accelerator normalization treats <Primary>/<Ctrl>/<Ctl> as control,
  ignores modifier order and case (conflict detection).
  `EnsureBinding(ctx, run, hk, command)` (=
  `EnsureBindingWith(..., BindingOptions{})`; the options variant's
  ForceBinding -- wired ONLY from the app's config live-apply path --
  rewrites an existing entry's accelerator to the requested hotkey
  through the same conflict-checked candidate ladder, filling
  Rebound/PreviousBinding on success, or RebindSkipped (a notice,
  never an error -- the working binding is kept) when every candidate
  is taken) -> `Applied{Binding,
  Requested, FellBack, Changed, Existing, Rebound, PreviousBinding,
  RebindSkipped, InList, DiskBinding,
  DiskCommand, Verified, VerifyNote}`: reads the media-keys
  custom-keybindings list; if the app's entry (fixed path ...
  /custom-keybindings/competent-search-thing/) exists it is STICKY --
  the binding is never rewritten without ForceBinding (user edits in
  GNOME Settings
  survive; Existing=true) and the stored command SELF-HEALS: it is
  rewritten (command key only; Repaired=true + PreviousCommand for
  the app's loud old->new repair log) when it can no longer launch
  the running binary -- empty/unparseable (commandExecutable, the
  GLib-shell inverse of ToggleCommand), a non-absolute executable, a
  dead path, or a live path that is a different file (os.Stat +
  os.SameFile vs the new command's exe) -- AND when it still launches
  it but through a Cellar-versioned spelling while the new command's
  is not (platform.ParseBrewCellar on both exes; the migration that
  keeps the binding alive across brew upgrades -- the
  brew-upgrade-broke-the-shortcut field fix), while any other
  still-working spelling (stable, custom symlink, and
  versioned->versioned when no stable spelling was derivable) is kept
  verbatim (zero writes,
  read-back verifies the on-disk command); a fresh entry gets the first free
  candidate of [configured, <Control><Alt>space, <Super>space]
  (normalization-deduped) checked against every accelerator in the
  wm/mutter/mutter.wayland/shell/media-keys schemas
  (`list-recursively`, arrays-of-strings only) plus every OTHER
  custom entry's binding (capped 64) -- because mutter silently
  refuses conflicting grabs and GNOME 46 defaults take BOTH Alt+Space
  (activate-window-menu) and Super+Space (switch-input-source); all
  candidates taken = sentinel ErrAllTaken, nothing written. Fresh
  writes go entry keys (name/command/binding) FIRST, list append
  LAST -- LOAD-BEARING ORDER: gsd (verified identical in
  gsd-media-keys-manager.c 42.1 and 46.0) reads the entry the moment
  the list changes, DROPS one whose command+binding are still empty
  ("Key binding ... is incomplete"), and a command written after
  that drop is silently lost (update_custom_binding_command only
  mutates existing keys), so list-last is what guarantees GNOME 42
  sees a complete entry; never "simplify" back to list-first. Both
  paths end with a read-back (3 fresh gets: list membership, binding,
  command) filling the InList/Disk*/Verified/VerifyNote fields --
  verification read failures degrade to Verified=false + note, never
  an error. Writes are GVariant text (single-quoted,
  parsed+serialized by tiny in-package helpers, incl. the "@as []"
  empty form); the scan tolerates missing schemas/entries but
  list/entry read and all write failures are fatal.
  `ToggleCommand(exe)` builds the GLib-shell-safe "<exe> toggle"
  command. `DaemonRunning(ctx)` (daemon.go) probes the session bus
  (godbus) for org.gnome.SettingsDaemon.MediaKeys -- the gsd process
  that turns the entry into a compositor grab; error = no bus =
  caller skips the check. Exhaustively unit-tested against scripted
  runners (exact argv sequences incl. write order and read-backs,
  idempotent second run = zero sets, verification mismatch paths)
  plus a LookPath-guarded smoke test of the real CLI and a
  throwaway-dbus-daemon test of the probe.
- `internal/platform` -- the PURE half of the platform layer, fully
  unit-tested headlessly: `ParseHotkey` ("alt+space", "ctrl+shift+k";
  modifiers ctrl/control, shift, alt/option, super/win/cmd/meta; keys
  space/tab/enter/return/esc/escape/a-z/0-9/f1-f12/arrows; unknown
  token -> error naming it) into an OS-neutral `Hotkey{Mods,Key}`;
  `StableExecutable(exe, args0)` (stablepath.go: the stable spelling
  of the running binary's path for anything that outlives the process
  -- exec.LookPath(base) hit kept UNRESOLVED, else the structural
  Homebrew candidates (brewpath.go: `ParseBrewCellar` splits an
  absolute <prefix>/Cellar/<formula>/<version>/<rest> path at its
  LAST separator-bounded "Cellar" component, prefix read from the
  path itself -- no hardcoded prefix list; candidates = linked
  <prefix>/<rest> then opt <prefix>/opt/<formula>/<rest>, and they
  precede args0 because in the gsd-boot context args0 IS the
  versioned Cellar path), else abs/Abs-resolved args0, else exe,
  every candidate same-binary-guarded via os.Stat+os.SameFile so a
  foreign same-named binary never wins; tested with real tempdir
  trees, symlinks and t.Setenv(PATH), no seams);
  `ResolvedExecutable(path)` (resolvedpath.go: the COUNTERPART --
  Abs + EvalSymlinks to the real regular file, ok=false on any
  failure; consumed ONLY by the app's setcap grant hint, because
  setcap refuses symlinks while StableExecutable deliberately
  prefers them; same real-tempdir test style);
  geometry (`Rect`, `Display{Rect,Work,Primary}`, `PickDisplay`,
  `BarPosition` = centered, top at H/3 - winH/3, clamped;
  `DisplayForWindow` by window center; `WailsPosition` translating
  absolute coords to Wails' current-monitor-relative
  WindowSetPosition; `Display.UsableRect` = Work when it has area
  else Rect (linux Xinerama fills Work with the full geometry), and
  `ClampSize(area, w, h, minW, minH)` = the ONE clamp-to-screen rule
  every window-sizing path shares -- area-capped per axis, floors
  win over a pathological tiny area, zero-area axes unclamped); open/reveal argv construction (`OpenCommands` /
  `RevealCommands`: linux xdg-open / dbus-send --print-reply
  FileManager1.ShowItems with xdg-open-parent fallback, darwin open /
  open -R, windows rundll32 FileProtocolHandler / explorer /select,)
  and `Launcher` (injectable `Run`/`Start` seams + `Logf`, BOTH
  env-carrying now: Start(argv, extraEnv) returns (pid, wait, err)
  and appends extraEnv to the inherited child environment -- nil =
  byte-identical old behavior, never os.Setenv -- while
  OpenEnv/RevealEnv/Launch thread the launch-credential env through
  (Open/Reveal stay as nil-env wrappers); RevealCommands takes the
  startupID injected into the ShowItems startup-id argument ("" =
  the old call); `Launch(argv, extraEnv)` runs ONE resolved handler
  command line under the same observed-grace semantics and returns
  the child pid for the raise watcher; every
  spawn logs its exact argv; Open/Reveal observe the child for a 1.5s
  grace window -- a non-zero exit inside it returns an error with
  captured stderr (unlinked-temp-file capture, never a pipe a
  grandchild could block or SIGPIPE on), logs, and falls through to
  the next candidate; a child still running at expiry is success,
  reaper-logged if it fails later; `Run` stays fire-and-forget for
  plugin run_command but logs spawns and reaper-logs failures); session
  detection (session.go: `DetectSession(getenv)` -- XDG_SESSION_TYPE
  "wayland"/"x11" wins, else WAYLAND_DISPLAY, else DISPLAY, else
  unknown; Desktop = raw XDG_CURRENT_DESKTOP;
  `Session.IsGNOME` = any colon-separated segment equals "gnome"
  case-insensitively); and the fps meter's data shapes (power.go:
  `PowerInfo` {MaxFPS, LowPowerMode, ThermalState} -- the darwin
  display/power probe result -- `ThermalStateString`, and
  `UncapStatus` + String(), the WebKit near-60 uncap outcomes kept
  in lockstep with platform_darwin.h's CS_UNCAP_* codes).
- `internal/platform/native` -- the thin OS glue, DELIBERATELY NO test
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
- `wails.json` -- Wails CLI project config (app name, frontend
  install/build commands) read by `wails dev`/`wails build` only; the
  no-CLI go-toolchain path does not use it.
- `frontend/` -- vanilla TypeScript + Vite. No framework. Tiny vitest
  + jsdom suite (`npm test` = `vitest run`; vitest.config.ts +
  src/test-setup.ts, which loads the REAL index.html body into jsdom
  before render.ts's module-load template grabs, so the DOM-order
  tests fail if the zones/templates change shape; src/priority.test.ts
  pins priority-above-files rendering, the flat traversal order, and
  the reconcileSelection rules, src/stats.test.ts pins the stats
  formatters + renderStats' dash-vs-value rules + the stats-row WIDTH
  CONTRACT (formatter-maxima sweeps and the style.css ch-reservation
  structure -- jsdom computes no layout, so those two sides ARE the
  mechanical gate), src/hover.test.ts drives the REAL main.ts
  over faked Wails bindings to pin the hover-vs-selection model
  (hover changes nothing, Enter runs the keyboard-selected row, click
  runs the clicked row), and src/ffext-logic.test.ts drives
  ../../webextension/logic.mjs (typed via its sibling logic.d.mts)
  with scripted browser/timer fakes -- listTabs/activate shapes, the
  tab-then-window call order, stale-tab rejection, reconnect backoff,
  push debounce -- all run in the CI linux job's frontend step).
  `index.html`
  (query row with inline SVG magnifier + hidden bang chip; #results
  split into #priority-results (plugin sections with priority > 0,
  ABOVE the files) / #file-results / static #empty ("No matches") /
  #plugin-results zones; status bar + degraded chip + backend chip;
  the #stats row
  BELOW the status bar -- the bottom-most chrome, five STATIC
  label/value span pairs (CPU GPU RAM SWP NET, value ids
  stat-cpu/-gpu/-ram/-swap/-net), starts hidden, JS only ever writes
  INSIDE the value spans (plain text, except the NET value's two
  tinted arrow spans stats.ts builds -- see the stats.ts bullet);
  #preview-pane
  (spinner + #preview-body + command strip with the web/AI buttons
  and the pane flash) as one more #bar child, display:none unless
  body.with-preview; #config-pane (header with #config-title +
  #config-filter, #config-notices, then #config-main = the ToC
  sidebar nav #config-toc beside the scrollable #config-body --
  sidebar before body, so Tab runs filter -> ToC -> controls --
  and #config-strip with
  flash + dirty note + Open config.json / Close / Save buttons) as
  the last #bar child, display:none unless body.with-config -- all
  editor rows/controls/ToC entries are built dynamically by
  config.ts, so no
  templates; <template>s for
  folder/file icons AND plugin section/row skeletons) + `src/main.ts`
  (search as-you-type: 15ms debounce + sequence-number stale-response
  drop; every generation also fire-and-forgets QueryPlugins(query,
  seq) -- INCLUDING the empty query, which is the Go-side cancel
  signal -- and updates the bang chip from the returned TargetInfo;
  an EMPTY query additionally fetches CheatSheet() and renders it as
  the single plugin section (dropped if the generation moved on or
  anything was typed), so the bar lists the available commands before
  you type -- with NO auto-selected row: both auto-select-first
  fallbacks are gated on a non-blank query so Enter on an empty bar
  stays a no-op, and moveSelection enters the unselected list
  explicitly (Down -> first row, Up -> last); wire() kicks the
  pipeline once at startup so the sheet is already rendered before
  the first summon (an app:shown emitted while EventsOn registration
  is still in flight is missed -- observed on cold WebKit starts) and
  fetches GetHistory into histEntries (refetched after each
  successful AddHistory). QUERY HISTORY modality (histCursor: -1 =
  not browsing, 0 = newest): Up recalls older entries when the input
  is blank OR histCursor >= 0 (the input is still exactly a recall's
  text -- every 'input' event and setQueryLocal reset histCursor to
  -1, so typing or picking a completion exits browse mode and the
  arrows navigate the result list again); Down while browsing moves
  forward, and forward past the newest entry clears the bar back to
  the empty state (cheat sheet); recall = replace the input, caret
  to end, re-run the pipeline (the recalled query renders its
  results live, Enter activates as usual; programmatic value writes
  fire no 'input' event, so the cursor survives). History COMMITS
  (AddHistory(state.query), fire-and-forget, then refetch) only when
  an activation actually executed: a file row's Open/Reveal resolved
  without error, or RunPluginAction resolved without error --
  set_query and blank queries never commit; the SAME two success
  sites also fire reportPick (RecordPick, fire-and-forget, errors to
  console.warn only -- the ranking log must never break an
  activation) with
  a report SNAPSHOTTED at activation time via pickReport (the flat
  state.items as {kind, path | plugin+score+title} identity rows,
  the picked rank, the action kind + revealed flag), appended to the
  always-on local ranking log Go-side;
  "plugin:results" emissions are dropped unless gen === seq, else
  upsert that plugin's section (keyed by id; priority = e.priority ??
  0) and renderPluginArea re-renders BOTH plugin zones
  (render.ts splitByPriority: priority > 0 -> #priority-results above
  the file rows -- the apps section -- everything else below;
  compareSections = priority desc, max score desc, plugin id);
  selection is one flat list in DOM order -- priority rows, then file
  rows, then below-zone plugin rows: ArrowUp/Down wrap, Home/End.
  TWO DISTINCT POINTER STATES (the hover-steals-selection field
  report): the ACTIVE selection (state.selected) moves ONLY through
  keyboard navigation and the auto-select/reconcile paths and is the
  single source of truth for Enter, the pick report, AND the preview
  pane, while mouse HOVER is a purely decorative CSS :hover wash
  (style.css .result:not(.selected):hover -- render.ts registers NO
  hover listener at all, RowHandlers has no onHover), so sweeping the
  cursor can never change what Enter runs, mark the generation
  navigated, or retarget the preview; a CLICK is the explicit mouse
  choice and activates the clicked row (src/hover.test.ts pins all
  three). Row handlers resolve their index at EVENT time
  (rows.indexOf(row) -- render-time captured indices went stale when
  a late priority emission PREPENDED rows above the files), and every
  re-render reconciles the selection through selection.ts
  reconcileSelection: userNavigated (set by arrows/Home/End,
  cleared per generation in runSearch) preserves the selected item BY
  IDENTITY at its shifted index, while an un-navigated bar re-runs
  auto-select on row 0 so a late apps section takes the selection
  Spotlight-style (never at a blank query -- the cheat sheet stays
  unselected, and its section is always priority 0/below);
  selection scrollIntoView fires ONLY for keyboard/auto-
  select navigation (applySelection/select carry a scroll flag;
  the plugin-area re-render selects without scrolling, so
  it never moves the viewport), and wheel input on #results is
  handled manually OFF-MAC ONLY (wheel.ts shouldInterceptWheel,
  vitest-pinned: navigator.platform "Mac*" = NO listener at all --
  a non-passive always-preventDefault wheel listener forces WebKit's
  synchronous main-thread scroll path there, pinning scroll motion
  to the Low-Power-Mode-halvable rendering-update clock, while
  native async overflow scrolling runs compositor-side at display
  rate with momentum; linux/windows register the listener exactly
  as before): a non-passive listener preventDefault()s and
  applies deltaMode-normalized deltas (40px/line, clientHeight/page;
  WebKitGTK sends pixels) straight to scrollTop -- WebKitGTK's
  default-on smooth-scroll animator otherwise eats fast detents and
  Wails exposes no setting for it; ctrl+wheel stays native on both
  paths; file
  rows Enter=Open / Ctrl/Cmd+Enter=Reveal; plugin rows run
  their action on Enter/click (Ctrl+Enter identical): set_query stays
  frontend-local (replace input, caret to end, re-run the pipeline),
  everything else goes to RunPluginAction -- Go owns bar-hide per
  action type; copy_text and run_builtin "version" stay open and flash
  "Copied" ~1.2s in the status bar, run_builtin "config" stays open
  WITHOUT the flash (Go summons the editor instead of hiding, so the
  visible flag must survive), action errors -- plugin actions
  AND file-row open/reveal failures -- flash ~2s; #empty
  shows only when a non-blank query has neither files nor sections;
  Tab/Shift+Tab are preventDefaulted no-ops reserved for future use
  (the default focus traversal would leave the input -- the bar's
  only focusable element -- and the webview, tripping the blur-hide);
  Esc + window blur -> Hide -- BOTH gated on !configModeActive()
  (config.ts): in config mode the document keydown handler
  early-returns entirely (arrows/Enter/Tab must behave like form
  keys; config.ts's own window handler owns Esc + Ctrl/Cmd+S) and the
  blur auto-hide is suppressed (users alt-tab away mid-edit) --
  those two plus the app:shown focus skip below are the ONLY three
  main.ts config gates; runtime events: "app:shown" -> CLEAR
  the input (the bar always summons empty; the pre-hide text is
  deliberately dropped) + reset histCursor + focus + refresh (renders
  the cheat sheet; plugins re-query through the same path) + a
  refreshStats re-render (GetStats is the instant cached snapshot;
  the summon's fresh samples follow as events) -- the reset runs
  even when config.ts is RESTORING the editor (hide-while-editing;
  see its app:shown handler) so the search layer underneath stays
  fresh for the eventual Esc-out, but the inputEl.focus() steal is
  skipped while configModeActive() (the restored editor re-asserts
  its own focused control),
  "index:progress" -> status text, "watch:degraded" -> warning chip,
  "watch:backend" -> the PERSISTENT #backend-chip when full=false
  ("Partial file watching" for inotify / "File watching off" for
  none, the Go hint on hover via title; full=true keeps it hidden;
  independent of the degraded chip -- both can show, one shared
  --sb-warning chip rule in style.css),
  "stats:update" -> applyStats. STATS ROW wiring (all in main.ts):
  applyStats hides the whole #stats row when snapshot.enabled is
  false (stats.disabled) and otherwise unhides + delegates to
  stats.ts renderStats; wire() calls refreshStats once at startup
  (same missed-app:shown reasoning as the cheat-sheet prefetch --
  pre-first-summon the enabled snapshot renders all dashes) and
  events keep it live while visible)
  + `src/stats.ts` (the stats row formatters + renderStats(snap,
  nodes): formatPct "12%" (rounded); formatBytesPair "6.2/15.9G" --
  BOTH values in the unit the TOTAL picks, GiB else MiB below 1 GiB,
  shared decimal rule one-decimal-below-10-else-none; formatRate
  humanizes bytes/sec B/K/M/G (binary) with the same rule, net
  renders as "<down>rx <up>tx" arrow pairs whose arrows renderNet
  builds as .net-down/.net-up SPANS (element+text-node building, the
  render.ts convention -- style.css tints the two directions; the
  rates stay plain text); any *Ok=false -> em-dash
  placeholder, while swapOk=true with swapTotal 0 (no swap configured
  / empty dynamic macOS swap) renders the live "0M" -- a real zero is
  a value, only a dead source dashes (the macOS SWP field report);
  the WIDTH CONTRACT constants (PCT_MAX_CHARS 4 "100%",
  BYTES_PAIR_MAX_CHARS 10 "1024/1024M", RATE_MAX_CHARS 5,
  NET_MAX_CHARS 13 = 2 rates + arrows + space) are the formatters'
  maximum emit widths over documented input domains (pcts 0..100,
  byte pairs used <= total <= 9999 GiB, rates < 9999 GiB/s);
  style.css reserves each value slot at MAX + 1ch and stats.test.ts
  pins BOTH sides -- bump a constant and the CSS reservation
  together;
  glyphs (em dash, arrows) are \uXXXX escapes -- ASCII-only source;
  src/stats.test.ts pins the formatters + the swap dash-vs-0M rules)
  + `src/fpsmeter.ts` (the dev-only fps meter; wire() ends with
  initFPSMeter(app), which asks the bound FPSEnabled ONCE and
  registers NOTHING when it answers false -- zero cost off. On: a
  rAF loop collects frame deltas (deltas > 250ms = gaps -- hidden
  window, summon resume, debugger -- excluded; visibilitychange to
  hidden resets the baseline so no hidden-state work happens),
  summarizes every ~5s of ACCUMULATED visible time (first report at
  ~2.5s for CI) through the PURE exported summarize (avg/max fps,
  >20ms long-frame pct, inferredHz = inverted 10th-percentile delta
  snapped within 10% to the common panel rates -- JS cannot read the
  refresh rate; the Go context line supplies the hardware truth),
  and fire-and-forgets RecordFPSSample (console.warn on error, the
  reportPick pattern); src/fpsmeter.test.ts pins summarize incl. the
  30fps-throttle and gap-discard shapes) + `src/wheel.ts`
  (shouldInterceptWheel(platform), the pure mac gate for main.ts's
  manual wheel interception -- see the wheel passage above)
  + `src/render.ts` (pure text-node DOM builders, no innerHTML
  anywhere: appendHighlighted renders the Go-minted matchRanges
  (half-open RUNE pairs; the walk counts code points because JS
  strings are UTF-16) as .hl spans -- LETTER COLOR ONLY via
  --sb-highlight, on file-row names AND plugin titles, no frontend
  re-matching (the old indexOf highlight is gone; renderResults no
  longer takes the query) -- file rows with the highlighted match + dim parent dir (a
  non-empty result hint replaces the parent-dir text -- the
  outside-indexed-roots note); plugin
  sections -- unselectable header, rows with icon/title/dim
  subtitle/badge/"label: value" fields; the builtin icon-name -> glyph
  map (calculator globe clock star info warning link terminal text
  hash bolt app puzzle; unknown/absent -> puzzle, non-name values
  render as literal glyphs); REAL app icons: a row whose result
  carries the internal-only iconKey renders its glyph, requestIcon
  batches the pending keys per render tick (queueMicrotask) through
  the bound ResolveIcons(keys, 64), and setIconImage swaps the glyph
  span's content for an <img class="plugin-icon-img"> whose src is
  the Go-minted data URI -- a DOM-node build with a property-assigned
  src, NOT a second innerHTML sink; answers (misses included) land in
  a module-level cache (cleared past 512 keys), stale rows are
  skipped via isConnected, and a missing binding/miss leaves the
  glyph standing; accent_color is ONLY ever applied by
  setting the `--plugin-accent` custom property on the row -- never
  inline color styles) + `src/theme.ts` (initTheme called first in
  wire(): fetches GetTheme and sets each token as `--sb-<k>` on
  <html>, injects GetCustomCSS as the text of the single managed
  `<style id="sb-custom-css">`, refetches on "theme:changed") +
  `src/style.css` (Spotlight-ish bar, dark by default; dir ellipsizes
  before the name; thin scrollbar; ALL colors/sizes/effects flow
  through var(--sb-*) -- the :root block holds the dark fallbacks and
  MUST stay identical to internal/theme/builtin/dark.json, enforced
  by internal/theme/sync_test.go; the decorative hover wash
  .result:not(.selected):hover (a 40% color-mix of --sb-selection-bg,
  clearly weaker than the full .selected style -- the :not() guard
  keeps the selected row's look authoritative under the pointer); the
  #stats row block (a single nowrap flex-0-0-auto line so it can
  never squeeze the results area: a five-track grid
  (repeat(5, auto) + space-between) whose value spans carry RESERVED
  min-widths -- 5ch pct / 11ch byte-pair / 14ch net = the stats.ts
  *_MAX_CHARS + 1ch slack, stats.test.ts-pinned -- plus tabular-nums,
  so a value changing rendered width never shifts its neighbors;
  ~0.85x small font, fg-dim on a --sb-border top border; the subtle
  metric hues are color-mix derivations of EXISTING tokens --
  labels = accent 45% into fg-dim under the 0.7 opacity, .net-down =
  accent 60%, .net-up = warning 60% -- deliberately NO new theme
  token, so the sync_test.go :root contract is untouched and both
  builtin themes stay legible -- and an explicit
  #stats[hidden]{display:none} because the author-level display:grid
  would defeat the UA sheet's [hidden] rule); appended namespaced
  plugin block
  (.plugin-*, .bang-chip, .status-flash) where every accent rule
  consumes var(--plugin-accent, var(--accent, #89b4fa)) and a :root
  bridge defines --accent: var(--sb-accent, #89b4fa), so the theming
  design tokens apply when present and the standalone default
  otherwise, merge order irrelevant; plus the appended .preview-* /
  body.with-preview block: with-preview turns #bar into a grid --
  the left column (query row/results/status/stats row exactly as
  before; four explicit rows, the pane spans 1 / 5 and a hidden
  #stats collapses its row to zero) keeps the FLAG-OFF bar width via
  minmax(0, min(var(--preview-results-col, 680px), 100%)) -- the
  min() cap makes the column give way instead of overflowing when
  the clamp-to-screen rule or a drag shrinks the window below the
  configured column width, the pane taking whatever remains -- the
  custom property preview.ts
  sets on <body> from GetPreviewConfig.resultsWidth (= config
  window.width; the 680px fallback is the pre-knob constant), pane
  in the rest behind
  a border-left divider, minmax(0,..)
  tracks so pane content scrolls instead of growing the window --
  and without the class every preview rule is inert, so flag-off
  layout is behavior-identical to the classic bar; CI screenshots run
  preview-off and must stay that way, the 780x550 default-geometry
  window regex in
  screenshots.ts depends on it) + `src/preview.ts` (ALL pane logic;
  initPreview is called once from wire() with the GetPreviewConfig
  answer and wires elements + listeners UNCONDITIONALLY -- each
  handler gates on the live `enabled` flag -- then hands the answer
  to the exported `applyPreviewConfig(cfg)`, which config.ts
  re-invokes with a fresh GetPreviewConfig after every GUI save and
  on every "config:changed" (the backend applies preview config
  live, so the pane mounts/unmounts/resizes without a relaunch):
  enabled toggles body.with-preview + renderIdle on mount, updates
  --preview-results-col, and setTrigger reflects each provider's key
  state on its strip button in BOTH directions; while disabled every
  hook/handler no-ops. The subscription
  "preview:result" (drop unless enabled AND payload.gen === its own
  previewGen
  counter, cancel the 150ms-delayed spinner on the first accepted
  payload, REPLACE the pane content per emission -- a fast meta card
  precedes the rich payload, cache hits skip it) and its
  OWN window keydown handler for Ctrl/Cmd+K (web) / Ctrl/Cmd+I (AI)
  -- main.ts's document handler is untouched, Tab and Ctrl+Enter
  stay reserved. previewOnSelectionChange (called from select(), the
  single selection choke point) paints an instant zero-IO header and
  debounces QueryPreview 90ms so held arrows stay free, dedupes
  same-row re-selects by target key, and maps rows to targets: file
  -> {kind:"file", path, isDir}, plugin -> {kind:"plugin", title,
  subtitle, pluginName}, null -> idle card + a debounced
  {kind:"none"} cancel; previewOnQueryChange feeds the strip labels
  ('Search web for "<q>"') and idles the pane on a cleared query.
  The strip buttons + hotkeys are the ONLY FetchWebPreview /
  FetchAIPreview call sites (never automatic; unconfigured providers
  render disabled with a hint naming the config key). Renderers are
  text-node-only: meta dl, text (header + highlighted <pre><code> +
  truncation footer), image (<img src=dataUri> + WxH/size caption),
  dir (rows cloning the folder/file icon templates + "N more..."),
  web (rows whose click runs RunPluginAction("preview", open_url) --
  Go validates, opens, hides the bar), ai (answer + model/cached
  badges + a Copy button through copy_text, <= 8 KiB Go-side, with a
  "Copied"/error flash in the pane strip), error card) +
  `src/config.ts` + `src/config.css` + `src/toc.ts` (the CONFIG
  EDITOR MODE --
  initConfig wires from wire(), everything else is lazy on the first
  "config:open": fetch + cache GetConfigSchema (embedded, immutable)
  and JSON.parse GetConfigForEdit's configJson into a WORKING COPY,
  set body.with-config (config.css hides every normal bar region via
  two-id selectors that out-rank the with-preview grid rules
  regardless of bundle order; #config-pane fills the bar as flex
  child OR spanning grid item), render the whole settings UI from
  the schema's top-level properties IN SCHEMA ORDER and focus the
  filter. LAYOUT (VS Code settings pattern): #config-main = the ToC
  sidebar #config-toc (~176px, own scroll) beside the controls
  column #config-body; the ToC is generated by the SAME walk that
  renders the controls (makeSection is the one registration point,
  so sidebar and column can never disagree): one entry per top-level
  section in schema order + indented sub-entries for nested object
  sections (search.frecency/priors/telemetry/arbiter,
  firefox.frequentSites/openTabs, preview.kagi/openai), while
  top-level LEAF settings group under a synthetic "General" section
  (leading run; a leaf after the first real section -- rewrites --
  gets its own group named after itself, schema order never
  reshuffled). Entries are buttons (ids config-toc-<dotted>; Tab
  order filter -> ToC -> controls, Enter/Space jumps): click =
  INSTANT scroll of #config-body to the section (never smooth --
  WebKitGTK's animator, the main.ts wheel-handler enemy), and the
  scroll listener highlights the entry whose section sits at the
  viewport top via toc.ts activeSectionIndex -- a PURE function over
  (offsets, scrollTop, viewport, contentHeight) with a
  bottom-of-scroll rule so a short trailing section can win;
  vitest-pinned (toc.test.ts), rAF-coalesced, measured through
  getBoundingClientRect. The renderer is a generic schema walk
  (resolve() follows
  "#/$defs/" refs; classify() picks the control): object-with-
  properties = nested section (dotted-path header + title +
  description note), boolean = checkbox, string enum = select,
  integer/number = number input carrying schema min/max as UX ONLY
  (Go owns validation; unparseable input marks the row invalid and
  blocks save by name), string = text input -- except descriptions
  starting "SECRET:" = password input + show/hide toggle, never
  echoed elsewhere; array-of-string = one-per-line textarea
  (trimmed, blanks dropped); object whose patternProperties values
  are all strings = key/value row editor with add/remove
  (bangs.aliases); EVERYTHING else (plugins.entries, rewrites, any
  future shape) = raw-JSON textarea that must JSON.parse before save
  ("invalid JSON" marks + blocks). HIDING is schema-annotation
  driven: a node carrying "x-editor-hidden": true -- checked on the
  property node AND its resolved $ref target (editorHidden) -- is
  skipped, leaf or whole subtree, at every depth; rootsVersion,
  "$schema", window.width/height, and preview.windowWidth/
  windowHeight carry the annotation in schemas/config.schema.json --
  the window sizes are set by DRAGGING the bar's edges (resize.ts),
  so editor rows would fight the drag (no
  hard-coded key list; vendor keys are invisible to the lockstep
  schema tests, which compare property-NAME lists only; toc.test.ts
  pins all six annotations). The filter
  hides rows by dotted path
  + description, then sections left empty -- and mirrors into the
  ToC: zero-match entries dim (.config-toc-dim), matching entries
  show a visible-row count badge (parent counts include their
  sub-sections); clicking a dimmed entry whose section the filter
  hid clears the filter first, then jumps. Controls write through
  setVal -> setPath into the working copy (dirty note + accent Save
  button); Save (button/Ctrl+S) = SaveConfig(JSON.stringify(doc)) ->
  error strips verbatim on failure, else "Saved" flash + notices
  (Applied live list, per-knob "<knob> takes effect at next launch"
  for nextLaunch/pending -- NEVER "restart" wording -- applyErrors
  as warnings), re-fetch (Normalize's repaired truth; fresh doc
  clears the summary slate first) + re-render preserving scroll/
  filter, and refreshPreviewConfig (GUI saves fire no config:changed
  -- self-write suppression -- so the applyPreviewConfig refresh
  runs here). GetConfigForEdit's unknownKeys render a persistent
  warning strip (dropped-if-saved; points at Open config.json,
  which calls OpenConfigFile and keeps the editor open).
  "config:changed" (external edit): editor closed = preview refresh
  only; open + clean = silent re-fetch/re-render + transient
  "changed on disk -- reloaded" flash + the event's own summary;
  open + dirty = keep the edits, show a "changed on disk" strip
  with a Reload button (also while HIDDEN with the editor latched:
  the strip is waiting on re-show, edits never clobbered); event
  error = error strip, doc kept. MODE
  EXITS vs HIDE/SHOW RESTORE: Esc/Close are the ONLY mode exits --
  clean = leave + focus #query (previous bar state byte-identical);
  dirty = first press flashes "unsaved changes --
  press Esc again to discard", second within 2s discards. Hiding
  the bar WHILE the editor is up (hotkey toggle, IPC hide, tray,
  darwin Space switch) leaves `active` latched and the next
  "app:shown" RESTORES the editor exactly -- mode, #config-body
  scrollTop, focused control (a focusin listener on #config-pane
  tracks the last focused id; re-asserted plus scroll plus ToC
  highlight one nextFrame later -- rAF with a setTimeout fallback
  for jsdom), and unsaved dirty edits -- in memory for the app run
  (config.test.ts pins the round-trip). After an Esc/Close exit the
  next summon is a fresh search bar, with an unsaved working copy
  still PRESERVED and restored on the next config:open this run
  (dirty note + "restored unsaved edits" flash; a clean editor
  re-fetches fresh). Own window keydown handler (Esc + Ctrl/Cmd+S,
  mode-gated);
  configModeActive() is the export main.ts gates on. All DOM is
  text-node-only; config.css consumes existing --sb-* tokens with
  literal dark fallbacks -- NO new --sb-* token, no :root block) +
  `src/resize.ts` (DRAG-EDGE WINDOW RESIZING, wired by wire()'s
  initResize: deliberately ELEMENT-FREE -- document-level pointer
  listeners classify positions against ~6px left/right/bottom bands
  + L-shaped bottom corners (edgeZone, pure + vitest-pinned in
  resize.test.ts; the top edge is excluded, it hosts the query row
  and the anchor), so NO overlay strips exist to intercept wheel/
  hover/selection/preview/sidebar events (WebKitGTK dispatches no
  DOM pointer events for native scrollbar interaction, so the
  results scrollbar at the right edge stays usable -- and the flip
  side is a documented yield: where a scrollbar hugs the right edge
  (overflowing results, the config editor body) it owns that strip
  and right-edge drags yield to it; left/bottom always work); hover
  swaps
  documentElement.style.cursor (ew/ns/nesw/nwse-resize), a
  capture-phase pointerdown inside a band preventDefault+
  stopPropagation-claims the drag + pointer capture (guarded --
  jsdom has none), pointermove computes ABSOLUTE targets from the
  drag-start size (dragTarget: horizontal = 2x the travel, about
  center; vertical = 1:1 downward; no accumulation across dropped
  frames) rAF-coalesced into ResizeDrag(w, h), and pointerup/cancel
  commits ONCE via ResizeCommit (moveless edge clicks commit
  nothing); drags stay active in config-editor mode -- they
  manipulate the window, not the search UI) +
  `src/highlight.ts` (hljs lib/core + explicitly registered grammars
  covering every LangHint name in the hljs distribution plus
  shell/plaintext -- never import the full highlight.js bundle;
  highlightInto: hinted registered language first, highlightAuto only
  for unhinted content <= 64KB, plain text beyond or on any error;
  `setHighlighted` is the frontend's ONE sanctioned innerHTML-style
  sink -- createContextualFragment into the single <code> node, fed
  EXCLUSIVELY hljs output, which HTML-escapes all content text by
  documented contract; the invariant comment on it is load-bearing,
  never route other strings through it) + `src/hljs-theme.css` (hljs
  token classes -> var(--sb-*) with literal dark fallbacks, scoped
  under .preview-code, imported from highlight.ts; NO new --sb-*
  token -- the :root block is a sync_test.go contract) +
  `src/wails.d.ts` (ambient
  types for the Wails-injected `window.go` / `window.runtime` incl.
  EventsOn, the event payload shapes (incl. StatsSnapshot -- the
  GetStats return AND "stats:update" payload, field names lockstep
  with internal/sysstats.Snapshot json tags), and the plugin wire
  contract TargetInfo/PluginAction (incl. activate_window + its
  window field, activate_tab + its tab token field, and the internal
  desktop_id -- all echoed back
  unchanged)/PluginResult/PluginEmission plus the preview contract
  Preview{Target,Payload,ConfigInfo,MetaRow,Text,Image,Dir,DirEntry,
  Web,WebResult,AI}, ResolveIcons, and the four preview bound
  methods, plus the config-editor contract ConfigForEdit/ConfigSaveResult/
  ConfigChangedEvent (Go nil slices arrive as null -- the applied/
  pending fields are `string[] | null`) and the four config bound
  methods GetConfigSchema/GetConfigForEdit/SaveConfig/OpenConfigFile
  plus the drag-resize pair ResizeDrag/ResizeCommit,
  and the telemetry report contract
  Telemetry{PickReport,ShownRef,PickedRef} and RecordPick -- keep in
  sync with internal/app + internal/plugin + internal/preview +
  internal/sysstats + internal/telemetry payload
  structs; field names lockstep with configui.go/configapply.go json
  tags).
- `webextension/` -- the shipped Firefox companion extension behind
  switch-to-tab (MV2, persistent background page -- an MV3 event page
  idles out and the host can never wake it, since native-messaging
  connections are always extension-initiated; permissions exactly
  [nativeMessaging, tabs]; pinned gecko id = ffext.ExtensionID).
  logic.mjs is ALL the logic, pure and importable (constants +
  tabRow/listTabs/handleMessage -- activate is tabs.update THEN
  windows.update, any rejection = {ok:false,error} -- +
  nextReconnectDelay + createController with injectable
  setTimeout/clearTimeout: one native port, capped-backoff reconnect,
  500ms-coalesced tabsChanged pushes on the five tabs.on* events);
  background.js/background.html are the thin module-script entry;
  logic.d.mts types the vitest import. NOT built or bundled by
  anything -- Firefox loads the directory (about:debugging) or a
  web-ext-signed .xpi of it; internal/ffext/sync_test.go +
  frontend/src/ffext-logic.test.ts are its CI gates (both hard).
- `examples/plugins/` -- three shipped example plugins, INERT until a
  user copies one into `<configDir>/plugins/` (each has a README with
  install/usage): `calc` (python3 command plugin: trigger prefix "=",
  bangs calc/c, ast-whitelisted arithmetic with bounded exponents,
  Hex/Binary fields for integers, copy_text, icon "calculator");
  `color-http` (the HTTP-transport sample: package `colorhttp`
  implements the documented wire format WITHOUT importing internal
  packages -- POST-only 405, malformed body 400, any path -- and is
  unit-tested to the coverage gate; `server/` is a thin package main,
  DELIBERATELY NO test file like internal/platform/native; manifest
  prefix "#", bang color, swatch fields R/G/B + H/S/L, accent = the
  color); `ps` (python3 bang-targeted-only plugin: NO trigger key,
  bangs ps, context ["running"], filters the running-app snapshot,
  copy_text PID). internal/plugin/integration_test.go drives the REAL
  shipped manifests + scripts end-to-end (LoadDir -> New -> Dispatch
  -> emission): calc/ps via real python3 (t.Skip when absent; CI has
  it), color via httptest around colorhttp.Handler, plus an echo
  script proving undeclared context stays off the wire and a
  min-timeout kill of a sleeping script. Keep the scripts, manifests,
  and those tests in sync.
- `schemas/` -- formal JSON Schemas (draft 2020-12, $id = raw master
  URLs) for every JSON format: config.schema.json (config.json),
  plugin-manifest.schema.json, theme.schema.json (theme files),
  plugin-request/plugin-response.schema.json (the v1 wire protocol).
  Deliberately STRICTER than the loaders (additionalProperties false;
  the response schema rejects what the sanitizer would clamp) --
  authoring aids, not the runtime validators. Kept in lockstep by
  internal/plugin/schemas_test.go, internal/config/schema_test.go and
  internal/theme/schema_test.go (test-only dep
  santhosh-tekuri/jsonschema/v6): they compile all five, validate the
  shipped example manifests + builtin themes + config.Default() +
  canned wire payloads, assert negative cases, and reflection-guard
  every struct json tag against the schema properties (and the theme
  token set against TokenNames), so a struct/schema drift fails CI.
  The example manifests and builtin themes carry "$schema" keys
  (loaders ignore unknown top-level keys). `schemas/embed.go` makes
  the directory a Go package too: it go:embeds config.schema.json as
  `schemas.ConfigSchemaJSON` for the app's GetConfigSchema bound
  method (the config editor validates client-side against it) --
  deliberately data-only, no functions or statements, so it stays out
  of the coverage math.

## Build / test

- NEVER run bare `go` commands (no `go build`, `go test`, `go vet`,
  `go mod tidy`). The ONLY build/test entry point is `go-toolchain`
  at the repo root.
- Build the frontend FIRST -- `frontend/dist` is embedded and gitignored:

      cd frontend && npm install && npm run build && cd ..
      GOFLAGS=-tags=webkit2_41,desktop,production go-toolchain --cgo

- `--cgo` is required (Wails Linux webview uses cgo for gtk3/webkit).
- `GOFLAGS=-tags=webkit2_41` is required on webkit2gtk-4.1-only distros
  (Ubuntu 24.04+); go-toolchain passes GOFLAGS through to the go tool.
- `desktop,production` are Wails v2's manual-build tags. WITHOUT them
  the binary still compiles and tests still pass, but running it exits
  immediately with "Wails applications will not build without the
  correct build tags" (the tagless wails/v2/internal/app is a stub).
  Keep them in GOFLAGS everywhere a runnable binary matters (CI needs
  one for the screenshot step).
- Linux build deps:
  `apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev`.
  The internal/portal and internal/tray bus tests also want `dbus`
  (dbus-daemon; they t.Skip without it, but skipped is not tested --
  CI has it).
- go-toolchain AUTO-REWRITES files (gofmt, go.mod/go.sum tidy, lint
  fixes). Always `git add` and commit whatever it changes; never revert
  its edits. On CI the same checks run read-only and a non-canonical
  tree is a hard failure.
- go-toolchain enforces >= 80% test coverage over packages that have
  test files, and FAILS any module that has coverable statements but no
  test results at all. That is why the App object lives in
  `internal/app` (tested) and `main.go` has no test file (packages
  without test files are not profiled). Keep `main.go` minimal; put
  testable logic in `internal/*`.
- Never call Wails `runtime.*` functions in unit tests -- without a real
  Wails context they abort the process (log.Fatalf). Guard runtime
  calls behind nil-context checks (see `App.Hide`).
- Benchmarks: run automatically after every build; also
  `go-toolchain bench run|save|show|compare` (`--benchtime`, `--count`).

## Conventions

- ASCII only in every file (code, docs, YAML): plain `--`, `...`, `"`.
  No em-dashes, no smart quotes, no unicode glyphs.
- Strict frontend file-type separation: TS/JS only in `.ts`/`.js`, CSS
  only in `.css`, HTML only in `.html`. No inline `<style>`/`<script>`
  bodies.
- Plugin styling: a result's accent_color reaches CSS EXCLUSIVELY via
  the `--plugin-accent` custom property set per row in render.ts;
  every consumer uses var(--plugin-accent, var(--accent, #89b4fa)),
  and a :root bridge defines `--accent: var(--sb-accent, #89b4fa)` so
  the theming tokens apply when present. Never apply plugin data
  as literal inline color/background styles, and never widen the
  whitelisted styling knobs without updating the sanitizer + README.
- No innerHTML anywhere in the frontend, with EXACTLY ONE sanctioned
  exception: highlight.ts's `setHighlighted`, which parses
  highlight.js output (hljs HTML-escapes all content text by
  documented contract) into the preview pane's single <code> node via
  createContextualFragment. Every other render path builds text
  nodes; never add a second markup sink.
- Changing any JSON-carrying struct (config, manifest/trigger, wire
  Request/Response, themeFile) or its validator means updating the
  matching schema in `schemas/` in the same commit -- the lockstep
  schema tests enforce it.
- One branch per session (`claude/searchbar-v1` for the v1 build,
  `claude/plugins-v1` for the plugin system), squash-merged; add
  follow-up commits rather than rebasing.
- Commit go-toolchain's auto-rewrites as part of your work.

## CI notes

- `.github/workflows/ci.yml` runs on every push (`on: push:`, no
  filters). Jobs: `linux` (the original single build job + cache
  hand-offs of the linux and windows app binaries), `darwin`
  (macos-latest: darwin/arm64 cgo build + the full unit-test suite,
  tags `desktop,production`, no deb; then
  `.github/scripts/darwin-smoke.ts` (typescript action, `file:`
  input) boots the built binary on the runner's real WindowServer
  session and asserts boot + JSON-shaped IPC round-trips within hard
  deadlines + the legacy-rejection check (a8-legacy-rejected: a bare
  v1 line must earn the JSON invalid-request error or a silent
  close, never the old raw "ok") + the config-command ack
  (a9-config-ipc: {"cmd":"config"} must earn
  {"ok":true,"accepted":"config"}, sent hidden after a7 and clear of
  the toggle pair, then an explicit hide restores the hidden state
  -- an IPC-ack check, not a UI check) + the fps meter gate
  (a4-fps-meter: every scenario boots with COMPETENT_SEARCH_FPS=1;
  after a3 shows the bar, a parseable "fps: N avg, ..." summary AND
  the "fps: meter on; display NHz max, ..." context line -- the
  darwin power-probe cgo's first-run proof -- must land in the app
  log within 12s; the MECHANISM is the gate, absolute rates are
  informational on the AC-powered VM) + a real on-screen window via a compiled
  CGWindowList Swift probe, including while a big index build is
  PROVABLY in flight (the hard B-midindex-window check: progress
  line present, completion line absent, before b1 runs); scenario B
  then WAITS OUT the big build (B-index-done, 180s bound; the B hard
  cap is 360s to fit it) and pins the macOS watcher field fixes:
  B-backend (the "watch: backend ..." log line must name fsevents --
  auto-selection + honest label in one grep) and B-fd-headroom (lsof
  row count vs kern.maxfilesperproc, threshold min(5000, limit/2) --
  a regressed unbounded kqueue path sits AT the fd ceiling and fails
  loudly) -- the step
  is a HARD GATE (no continue-on-error): every SMOKE id is pass/fail
  and any FAIL fails the darwin job and with it all-builds;
  screenshots are best-effort "evidence:" captures (never a SMOKE
  id), copied to smoke-shots/ and uploaded via actions/
  upload-artifact@v4 as `darwin-smoke-<sha>` (`if-no-files-found:
  ignore` -- the linux screenshots pattern), and EVERY capture is
  ordered AFTER the focus/visibility-sensitive hard gates: the job's
  FIRST screencapture can pop the macOS 26 TCC screen-recording
  consent dialog, a key-stealing system modal whose focus loss
  blur-auto-hides the bar (correct product behavior) -- it starved
  a4's rAF accumulation and inverted a5's toggle into a re-show on
  run 29728632030, so scenario A's one shot (01-summoned-macos.png,
  the a6-reshown bar = the summoned state) lands after a6, where the
  remaining gates (a7, a9, all of B) are proven dialog-tolerant --
  including the
  translucentEvidence run between scenarios A and B: one extra boot
  with window.translucent=true (startApp's extraCfg param) captured
  as 03-translucent-macos.png, EVERY failure swallowed as an
  "evidence: ... unavailable" line, no SMOKE ids -- and the full
  app-log
  dumps print only on failure (green runs get the
  hotkey:/index:/watch:/panic summary lines); hands off the
  darwin/arm64 app binary the same way) and `publish` (needs: [linux,
  darwin];
  publishes ONE buildhost release per push -- see "Binary publishing"
  below). There is deliberately NO aggregator job: the org-required
  `all-builds` context is a COMMIT STATUS posted by the
  required-builds-manager app, which tallies the repo's real build jobs
  itself (it excludes any job literally named all-builds from its own
  math -- its status text reads "N/M builds ..."; `publish` counts as a
  real job in that tally), so a green merge needs `linux`, `darwin` AND
  `publish` green, and renaming/adding jobs is safe. The aggregator job
  #23 briefly added was redundant and was removed in the 2026-07-17
  ci.yml cleanup (#25).
- The `linux` job: checkout -> apt install gtk/webkit/x11 dev packages plus
  xvfb/xdotool/imagemagick/x11-utils/openbox -> `npm ci && npm run
  build && npm test` in `frontend/` (npm test = the vitest gate:
  DOM ordering + the webextension logic suite) -> `wow-look-at-my/go-toolchain@v1`
  with `targets: linux/amd64,windows/amd64`, `cgo: 'true'`,
  `timeout: '20'`, `autorelease: 'false'`, and env
  `GOFLAGS: "-tags=webkit2_41,desktop,production"` -> two
  `wow-look-at-my/actions@cache-upload#latest` hand-offs
  (`app-linux-amd64`, `app-windows-amd64` -- job hand-offs ride the
  org's cache trio, never GitHub artifacts) -> deb build + publish
  (next bullet) -> screenshot capture -> `actions/upload-artifact@v4`
  (screenshots).
- Deb packaging: buildhost's own `fmt=deb`/APT-repo debs carry NO
  `Depends` (hardcoded control in buildhost internal/repackage/deb.go),
  so on a machine without the WebKitGTK/GTK runtime libs the app dies
  at the dynamic loader (`libwebkit2gtk-4.1.so.0: cannot open shared
  object file` -- real user report, 2026-07-16). CI therefore builds a
  proper .deb itself (dpkg-deb; `Depends: libwebkit2gtk-4.1-0,
  libgtk-3-0, libglib2.0-0, libgdk-pixbuf-2.0-0, libsoup-3.0-0,
  libjavascriptcoregtk-4.1-0, libc6 (>= 2.34)` = the binary's direct
  NEEDED libs; names resolve on Ubuntu 22.04 AND 24.04 -- noble's t64
  packages Provide the unsuffixed names; deb Version =
  `0.<run_number>+g<sha7>`) and publishes it to the separate buildhost
  project `competent-search-thing/deb` (kind=archive, raw download =
  byte-identical passthrough) via the first-party
  `wow-look-at-my/buildhost/.github/actions/buildhost-{create-release,
  upload-artifact,publish-release}@master` actions (OIDC, same
  `id-token: write` the workflow already grants). If the app ever
  gains new direct library deps (check `objdump -p` NEEDED), update
  that Depends line + README's dep table together. The install path
  was verified in clean Ubuntu 24.04/22.04 chroots that never had the
  build deps -- keep it that way when changing packaging: an
  in-build-container run proves nothing about user machines.
- Targets: in the `linux` job, linux/amd64 is the only cgo
  (gtk/webkit) target; windows/amd64 cross-compiles pure-Go from the
  Linux runner (Wails uses WebView2 on windows, and Go auto-disables
  cgo for non-host targets) but is never RUN in CI. darwin needs cgo
  against the Apple SDK, so darwin targets must never be added to the
  LINUX job's targets list -- darwin is built by the dedicated
  `darwin` job on a mac runner.
- Binary publishing: the dedicated `publish` job (needs: [linux,
  darwin], ubuntu-latest, no checkout) restores the `app-linux-amd64`
  + `app-windows-amd64` + `app-darwin-arm64` cache hand-offs (the
  org-standard `wow-look-at-my/actions@cache-upload#latest` /
  `@cache-download#latest` trio -- NOT GitHub artifacts, which this
  org does not use for job hand-offs; distinct hand-off names keep
  the `cache-xfer-<name>-<run_id>-<run_attempt>` keys collision-free,
  single-file hand-offs restore as `<dest>/<basename>`, and a
  "re-run failed jobs" restores the prior attempt via the
  restore-keys prefix) and publishes ONE buildhost release per
  push to project `competent-search-thing` carrying linux/amd64,
  windows/amd64 and darwin/arm64, via the same first-party
  buildhost-{create-release,upload-artifact,publish-release}@master
  actions the deb uses (the actions mint their own OIDC token, audience
  https://pazer.build, from the workflow's `id-token: write` grant; no
  version input = auto-increment; git_branch defaults to
  github.ref_name). Branch pushes create branch releases
  (`?branch=<name>` downloads, slashes URL-encoded); the bare "latest"
  URL only ever follows the default branch (master) -- a buildhost
  guarantee, do not re-verify it per project. This replaced
  go-toolchain's `autorelease` (removed in the 2026-07-17 cleanup #25;
  restored as the explicit job the same day), and BOTH build jobs pin
  `autorelease: 'false'` on their go-toolchain steps -- the action
  DEFAULTS it to 'true', so #25's input-line removal had silently
  re-enabled in-job publishing (extra app + server releases per push,
  fixed 2026-07-19). The old autorelease also
  pushed the color-http example server binary to project
  `competent-search-thing/server`; that is deliberately NOT restored
  (dev sample, not a user-facing deliverable). Deb publishing (previous
  bullet) is unchanged, and README "Install" documents the real install
  paths (deb / linux raw binary / macOS / windows).
- `frontend/package-lock.json` is committed (required by `npm ci`).

## CI screenshots

- After the build, `.github/scripts/screenshots.ts` (run via
  `wow-look-at-my/actions@typescript#latest`, `file:` input) boots the
  freshly built binary under Xvfb and captures three PNGs PER BUILTIN
  THEME into `screenshots/<theme>/` (dark, light) at the workspace
  root: `01-summoned.png` (empty bar), `02-results.png` (query "rep"
  with highlighted matches), `03-selection.png` (selection moved down
  two rows). Each theme gets a FRESH app process reading a temp
  config.json with that `theme` set (no hot-reload reliance);
  Xvfb/openbox stay up across themes. Everything uploads as the
  `screenshots-<sha>` artifact (`if: always()`; upload-artifact walks
  `screenshots/` recursively, so no per-theme path config) for visual
  comparison between runs. The step FAILS the job -- and with it the
  required `all-builds` status -- when the window never maps, a capture
  is blank/tiny, the hotkey grab is refused, or Escape does not hide
  the bar; treat that as a real UI regression, not flakiness to mute.
  Blank detection is PER-THEME (dark mean band 500..60000, light
  30000..64000 -- the light UI averages ~61k/65535, above dark's old
  ceiling -- plus per-shot size floors); the bounds were derived from
  real local captures recorded in the script comment. If the UI or a
  builtin theme changes deliberately, RE-DERIVE them from a local run
  (CLAUDE.md "To capture locally" below); never guess.
- Mechanics (mirrors what was verified manually): deterministic
  ~200-file fixture tree + `config.json` in a temp dir
  (`COMPETENT_SEARCH_CONFIG_DIR`), `Xvfb :99` at 1280x800x24, openbox
  with the stock `A-space` keybind stripped from
  `/etc/xdg/openbox/rc.xml` -- stock openbox grabs Alt+Space for its
  client menu, which wins the race and makes the app's XGrabKey fail
  with BadAccess -- then the REAL `xdotool key --clearmodifiers
  alt+space` summon, `xdotool type`, arrow keys, `import -window`,
  Escape-hides assertion. The app window is found by name + 780x550
  geometry in `xwininfo -root -tree` (xdotool search --onlyvisible
  --class does not match it; the geometry is the DEFAULT
  window.width/height -- the script's temp config.json sets no size,
  so it must track the internal/config defaults). One full retry with
  `WEBKIT_DISABLE_DMABUF_RENDERER=1` (not needed under Xvfb so far).
  The binary is `build/competent-search-thing_linux_amd64` in CI
  (go-toolchain matrix naming) or `build/competent-search-thing`
  locally; the script tries both and runs a THROWAWAY COPY, never the
  build/ artifact itself. The script launches the binary with ZERO
  CLI arguments -- internal/cli's bare-invocation path must keep
  booting the GUI or every capture breaks. It leaves
  COMPETENT_SEARCH_SOCKET unset, so the per-theme app processes share
  the default socket path: that works because each theme's process is
  stopped (SIGTERM then SIGKILL) before the next starts and
  ipc.Listen recovers the stale socket file; a still-running previous
  instance would make the next launch exit "already running" and fail
  the capture loudly.
- To capture locally: `apt-get install -y xvfb xdotool imagemagick
  x11-utils openbox`, build with the full GOFLAGS above, then follow
  the same sequence (the script is directly readable as the runbook).
- `docs/screenshot.png` is the committed reference image used by
  README.md (the 02-results state, captured from the real app under
  Xvfb). If the UI changes deliberately, recapture and replace it.
- `docs/screenshot-macos.png` is the committed macOS reference image
  used by README.md's macOS install section (the darwin GUI smoke's
  01-summoned capture, taken from the `darwin-smoke-<sha>` artifact).
  If the macOS UI changes deliberately, recapture and replace it.
