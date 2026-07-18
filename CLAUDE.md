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
  silently flips it to OnDemand -- and with the flag off both fields
  stay nil, byte-identical to the pre-flag call (CI screenshots run
  flag-off). Zero-arg invocation boots the GUI exactly as before the
  CLI existed (CI screenshots rely on that). Deliberately has NO test
  file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/GetTheme/GetCustomCSS/Startup/DomReady/
  Shutdown/QueryPlugins/RunPluginAction/CheatSheet/GetHistory/
  AddHistory/GetStats). Bound methods
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
  error), Open config -> openConfigFile (the !config behavior minus
  the bar-hide), Quit -> runBuiltin("quit"); the tooltip getter wraps
  hotkeyDescription(), so no shortcut is promised until one is
  proven), starts the system-stats sampler once (stats.go in this
  package: the `newStats` builder seam -- production buildStats does a
  fresh config.Load (translucent.go pattern), stats.disabled = one
  "stats: disabled in config" log + nil, else sysstats.New wired with
  OnUpdate = emitStats (the guarded "stats:update" emit) and
  log.Printf -- and a non-nil sampler is Start()ed under a dedicated
  ctx cancelled in Shutdown; the sampler idles until the bar first
  shows, so startup cost is zero and newTestApp-stubbed apps spawn
  nothing), wires the
  single-instance IPC handlers when Options.IPC is set (Toggle =
  toggle, Show = showIfHidden, Hide = Hide; Options.ShowOnStartup
  latches a pending show), brings the plugin
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
  builtins only, no noise), starts theme hot reload (theme.go: a
  dedicated fsnotify watcher on the config dir + its themes/ subdir,
  events debounced 300ms into "theme:changed"; any failure = log +
  run on without live reload), and kicks the initial disk walk in a
  goroutine (under a cancellable context); when the walk finishes,
  `startWatch` brings up the `watch.Watcher` + `watch.Rescanner` +
  `watch.Sweeper` trio honoring the Options watcher knobs
  (WatchMaxWatches, WatchExcludes -> a second watch-only Excluder,
  WatchBackend -> watch.Options.Backend, SweepInterval,
  SweepDisabled = no Sweeper + one loud warning; see the
  internal/watch bullet), then announces the effective backend ONCE:
  `watchBackendFor(st.Backend)` builds the "watch:backend" payload
  {backend "fanotify"|"inotify"|"none", full bool (fanotify only),
  hint string (empty when full; the pinned hintPartialWatch /
  hintWatchOff texts otherwise, hintWatchFailed when the watcher
  itself failed to start -> backend forced to "none")}, and when NOT
  full `logFanotifyGrant()` first logs -- once per App, linux only
  (plat.goos), the grant line BEFORE the emit (tests synchronize on
  the recorded event, then read the log) -- "watch: enable
  full-filesystem watching with: sudo setcap
  cap_sys_admin,cap_dac_read_search+ep <path>" with the path through
  platform.StableExecutable(exe, args0), mirroring hotkey.go;
  `Shutdown` (wired to Wails OnShutdown) closes the IPC server first
  (when present), releases the hotkey (native stop func, cancel of
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
  sweeper nil-tolerated when disabled) plus the theme watcher
  cleanly -- every step bounded, so quit never waits out a disk
  walk. Summons that arrive before
  the frontend can render are deferred: `DomReady` (wired to Wails
  OnDomReady) executes at most ONE pending show (ShowOnStartup or an
  early hotkey/IPC toggle/show; Hide cancels the pending flag), and
  after DomReady summons act immediately. `showIfHidden` is the IPC
  show handler: visible = plain re-WindowShow (no capture, no
  reposition), hidden = the same capture+position+show path toggle
  uses. GetTheme re-loads config.json
  (ONLY the theme field is consumed live) and returns theme.Resolve's
  token map -- errors are logged once per distinct message and fall
  back to dark; GetCustomCSS returns <configDir>/themes/custom.css
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
  EnsureFreshInstalled(5m) -- the bar window
  steals focus, so this precedes showing), then
  `showOnCursorDisplay`: platform.CursorDisplays -> PickDisplay ->
  BarPosition (absolute coords), then darwin = native.MoveWindow,
  linux/windows = translate via DisplayForWindow + WailsPosition (Wails
  WindowSetPosition is RELATIVE to the window's current monitor -- and
  to the WORK AREA origin on Windows -- while WindowGetPosition is
  absolute; verified in the v2.13.0 sources), any failure -> WindowCenter;
  then WindowShow + "app:shown". EXCEPTION: on a detected Wayland
  session (platform.DetectSession via the detectSession seam, cached
  once per process) the whole cursor-display flow is skipped --
  Wails is a native Wayland client there and gtk_window_move /
  keep-above are silent no-ops, the compositor owns placement -- so
  the show path is WindowCenter (best-effort) + WindowShow, with a
  once-per-run "placement is decided by the compositor" log; the
  X11/unknown path is untouched (CI's Xvfb has DISPLAY set and no
  XDG_SESSION_TYPE, which detects as x11). `QueryPlugins(query string, gen
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
  actually ran. `GetStats() sysstats.Snapshot` (stats.go) returns the
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
  (newRegistry, swap under mutex, Close the old) / config (open
  config.json) / version (copy `Version`, stays open) / quit
  (runtime Quit); activate_window (parseWindowID: non-empty base-10
  uint32) -> the activateWindow seam (production
  native.ActivateWindow); everything else hides the bar on success. Events
  emitted (all guarded so a nil ctx no-ops): "index:progress"
  {indexed,done,seconds}, "watch:degraded"
  {watched,dropped,overflows}, "watch:backend" {backend,full,hint}
  (once, from startWatch; see above), "app:shown", "theme:changed" (no
  payload; frontend refetches GetTheme/GetCustomCSS),
  "plugin:results" (payload plugin.Emission
  {plugin,name,gen,results}), "stats:update" (payload
  sysstats.Snapshot {enabled,cpuPct,cpuOk,gpuPct,gpuOk,memUsed,
  memTotal,memOk,swapUsed,swapTotal,swapOk,netRxBps,netTxBps,netOk};
  enabled always true on the event -- it only ever fires from a live
  sampler). ALL Wails
  runtime calls and platform hooks sit behind seam structs
  (`runtimeSeams` incl. clipboardSetText/quit and `platformSeams`
  incl. run/activateWindow/appSource plus getenv/executable/args0/detectSession/
  startPortal/ensureGnomeBinding AND the launch seams --
  open/reveal/run take extraEnv now (reveal also startupID),
  launchExec, resolveHandler, handlerByID, mintCredential,
  prepareLaunch, dbusLaunch, watchState, snRemove -- in window.go;
  defaults in New, plus
  the `newRegistry`, `newTray` and `newStats` seams); unit tests MUST
  replace them (see
  newTestApp, which also nils appSource, stubs newRegistry, newTray
  AND newStats so no config, X11, session-bus or /proc//sys IO
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
  ErrAlreadyRunning = Send "show" to the running instance + stdout
  notice + exit 0; any other listen error = log + run the GUI with a
  NIL server (degraded, no IPC -- the app must still work). toggle /
  show send their command to the running instance; when none runs
  they start the GUI in this process with ShowOnStartup=true (on an
  ErrAlreadyRunning race they fall back to Send "show"); an "err not
  ready" reply counts as success (the instance is booting and shows
  itself). hide never starts the app: not running = plain notice on
  stderr + exit 1 (cobra error/usage output suppressed). Convention:
  ONE self-registering subcommand per file (init -> registerCommand);
  newRoot() consumes the builder registry so Execute -- and every
  test -- gets a fresh command tree. RunOptions{Server,
  ShowOnStartup} is the runGUI contract; the App takes ownership of
  the server (Shutdown closes it). Unit-tested headlessly: fake
  runGUI, real ipc servers on temp sockets, COMPETENT_SEARCH_SOCKET
  (t.Setenv) isolation.
- `internal/ipc` -- the single-instance unix-socket IPC layer, pure
  and headless-tested. SocketPath: $COMPETENT_SEARCH_SOCKET override,
  else $XDG_RUNTIME_DIR/competent-search-thing.sock, else a per-uid
  name under os.TempDir(). Line protocol, ONE request per conn (2s
  conn deadline, 4 KiB line cap): toggle/show/hide/version/ping ->
  "ok" | the bare version string | "err <reason>" ("err not ready"
  until SetHandlers wires the app -- nil handler members stay not
  ready; version/ping always answer). Listen recovers stale sockets:
  EADDRINUSE -> 500ms probe dial; an answer = ErrAlreadyRunning, a
  dead socket = os.Remove + retry ONCE; after listening the file is
  chmodded 0600 (filesystem perms are the only auth). Close is
  idempotent + nil-safe: stops the accept loop, unlinks the socket,
  waits for in-flight conns. Send wraps every dial failure in
  ErrNotRunning (test with IsNotRunning) so callers can branch
  "nothing to talk to" vs a broken exchange. Handlers run on conn
  goroutines and must be goroutine-safe.
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
  files, shorter then lexicographic paths. QueryWith dispatches by
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
  `QueryWith(q, limit, QueryOptions{FuzzyDisabled})` is the toggle
  path (config search.fuzzyDisabled -> main.go ->
  Manager.SetFuzzyDisabled): disabled dispatches to queryNamesSub,
  the pre-fuzzy scan, behavior-identical to the old engine. A query
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
  = base name, pattern with separator = full path), symlinks indexed
  but never descended, permission errors counted not fatal, throttled
  progress callbacks. `Manager`: owns the RWMutex contract (queries
  RLock, mutations Lock); `BuildFromDisk` walks into a fresh store and
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
  convention; main.go wires it to Manager.SetFuzzyDisabled},
  watcher {maxWatches 0 = auto-budget / negative = unlimited,
  sweepMinutes 0 = the 20m default, sweepDisabled (zero value = sweeps
  ON, the tray.disabled convention), watchExcludes
  (json omitempty; excluder-syntax patterns never LIVE-WATCHED but
  still indexed + swept), backend (json omitempty; the
  WatcherBackend* constants "auto"/"fanotify"/"inotify" -- fanotify =
  STRICT, no inotify fallback; Normalize trims+lowercases and repairs
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
  dirMaxEntries 200, kagi {apiKey, maxResults 8}, openai {apiKey,
  model "gpt-5-mini", maxOutputTokens 1024}} -- the opt-in preview
  pane (zero value = off); numerics and an empty model are
  Normalize-repaired, the API keys pass through verbatim and are never
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
  patterns without system trees). rootsVersion (0 = legacy, current
  3) drives the one-shot Load migration, each missing step applied in
  order: the v2 step moves configs whose roots are exactly the legacy
  home default (or empty) to the new default roots + appends the
  missing system excludes (user patterns untouched; customized roots
  stamped only); the v3 step appends the MISSING noiseExcludes -- but
  ONLY when the exclude list still contains ALL of baseExcludes
  (default-shaped); a curated-away or explicitly empty list is
  stamped only, with an informational note. Either way version 3 is
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
  opaque json.RawMessage forwarded verbatim to that plugin.
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
  gpuExec + now})` probes sources ONCE, cheaply (no subprocess
  spawns): GOOS != linux = zero sources + one "placeholders" log;
  linux = the three proc files assumed present, GPU = first readable
  glob hit of SysRoot/class/drm/card*/device/gpu_busy_percent
  (amdgpu) else LookPath("nvidia-smi") else none (intel: deliberately
  absent, no cheap sysfs busy%), all summarized in ONE "stats:
  sources: cpu=... gpu=..." line. THE invariant: nothing outside the
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
  baseline sample (point-in-time mem/swap/amdgpu published; previous
  RATE values kept, never blanked) then a one-shot follow-up at
  Interval/5 (~300ms) so cpu/net rates turn fresh right away. Rates
  (cpu pct, net Bps) come from counter deltas ONLY when the stored
  counters are <= 3*Interval old (rateWindow) -- older = re-store +
  keep previous values -- and negative/zero deltas (wrap) skip the
  update; cpu busy = total - idle - iowait over the first 8 "cpu "
  aggregate fields (guest/guest_nice excluded, already inside
  user/nice), pct clamped 0..100; mem used = MemTotal - MemAvailable
  (missing MemAvailable = MemOK false; kB * 1024 = bytes); swap =
  SwapTotal/SwapFree, total 0 valid (SwapOK true, dash); net = sum of
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
  (skips where unreadable). Consumed by internal/app's stats.go.
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
  against the post-truncation title; Action carries the
  INTERNAL-ONLY DesktopID json:"desktop_id" -- the .desktop entry
  behind a builtin run_command launch, consumed by the app's
  credentialed launch path) and `SanitizeResponse`, which
  clamps/validates everything an external plugin returns: 20-result
  cap, rune caps (title 200/subtitle 300/badge 24/field 40+200, max 8
  fields), control chars -> spaces everywhere, icon = builtin name or
  <=32-byte glyph, accent_color regex, score default 50 clamp 0..100,
  action validation (open_path abs path, open_url http(s)+host,
  copy_text <=8 KiB, run_command 1..16 argv <=1024 B each and the
  whole RESULT is dropped unless the manifest sets allow_run_command;
  internal-only set_query/run_builtin/activate_window always stripped
  and a stray Action.Window OR Action.DesktopID on external types
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
  goroutine-safe. Routing: resolved bang (exact/alias/unique-prefix)
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
  snapshot (empty query = first 15 alphabetical, prefix 100 /
  substring 80, cap 15, run_command argv via `parseDesktopExec`:
  quotes, backslash escapes, %-field codes stripped; the shared
  scoring/sort/cap/result-build helper is `collectAppResults`, whose
  actions also carry DesktopID = the InstalledApp.ID so the app can
  launch with activation credentials);
  builtin_apps_search.go "apps-search"/Apps -- installed apps in
  NORMAL results: no bangs, a real all_queries Trigger (match
  override on builtinBase, effective min 2 runes), ranking exact 100
  / prefix 90 / word-start 75 (words = letter/digit runs, so spaces,
  hyphens, dots split) / substring 60, cap 6, same run_command
  launch; bang routing keeps it exclusive with the targeted !app
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
  "pinned" badge on pinned tabs / open_url action -- which re-OPENS
  the page, it cannot focus the existing tab (README honesty note).
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
  Emit, KagiAPIKey, KagiMaxResults, OpenAIAPIKey, OpenAIModel,
  OpenAIMaxOutputTokens, AICachePath, Logf})` -> Dispatcher;
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
  config key + env fallback; provider failure; 10s web / 90s ai hard
  timeouts spelled out by fetchErrMsg). kagi.go: KagiClient
  (NewKagiClient(key, maxResults); BaseURL/HTTPClient/Now exported
  seams) -- Kagi Search API v1 verified 2026-07-18: GET
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
  GetPreviewConfig reports -- and passes <configDir>/aicache.json
  (config.Dir() failure = one log line + memory-only cache); the
  keys flow only into preview.Options, never into logs or payloads;
  bound methods QueryPreview(target, gen) / GetPreviewConfig()
  (enabled + kagi/openai configured, keys never exposed) /
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
- `internal/appctx` -- app-context collection for the plugin system,
  pure and headless-tested: the data types (AppInfo / InstalledApp /
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
  /proc exe readlink fails; expected).
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
  and firefox-tabs builtins.
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
  .WatchMaxWatches): 0 = auto (linux min(max_user_watches/2,
  65536), floor 1024, via the `readMaxWatches` seam; non-linux/read
  failure = unlimited watch-everything), negative = unlimited.
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
  tests run real inotify. BACKEND SELECTION: New binds
  `newBackendNotifier(Options.Backend, normalized roots)` (notify.go;
  config watcher.backend -> app.Options.WatchBackend): "inotify" =
  plain fsnotify, no fanotify probe; "fanotify" = STRICT
  `newStrictFanotifyNotifier` (fanotify_linux.go + the
  fanotify_other.go always-none twin) -- constructor failure = one
  LOUD 'backend "fanotify" required by config but unavailable ...
  live watching DISABLED' line + the no-op `noopNotifier` (notify.go:
  accepts everything, delivers nothing, kind ("none", wide) so
  Watched/IndexedDirs stay 0 and addInitialWatches logs no
  marks-active line for it; sweeps converge), NEVER an inotify
  fallback; anything else = `newAutoNotifier` (fanotify_linux.go; the
  fanotify_other.go twin is plain fsnotify) -- try the fanotify
  whole-filesystem notifier, ANY constructor error = one log line +
  per-directory fsnotify fallback. The `newFanotifyFn` package var is
  the constructor seam both selections probe (scripted in tests, no
  CAP_SYS_ADMIN needed). fanotifyNotifier: ONE
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
  Stats{Backend "inotify"|"fanotify"|"none" (strict mode refused: no
  live watching, sweeps only), Budget, WatchedDirs,
  IndexedDirs, DroppedWatches, Evictions, Overflows, Degraded};
  `InitialRegistration()` closes when the first fill finished (the
  app waits on it before its summary log). `Sweeper` (sweep.go): the
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
  alpha -- v42 argbToRgba parses exactly that); ToolTip
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
  `EnsureBinding(ctx, run, hk, command)` -> `Applied{Binding,
  Requested, FellBack, Changed, Existing, InList, DiskBinding,
  DiskCommand, Verified, VerifyNote}`: reads the media-keys
  custom-keybindings list; if the app's entry (fixed path ...
  /custom-keybindings/competent-search-thing/) exists it is STICKY --
  the binding is never rewritten (user edits in GNOME Settings
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
  geometry (`Rect`, `Display{Rect,Work,Primary}`, `PickDisplay`,
  `BarPosition` = centered, top at H/3 - winH/3, clamped;
  `DisplayForWindow` by window center; `WailsPosition` translating
  absolute coords to Wails' current-monitor-relative
  WindowSetPosition); open/reveal argv construction (`OpenCommands` /
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
  case-insensitively).
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
  darwin = golang.design/x/hotkey (CGEventTap; needs Accessibility,
  register best-effort from a goroutine) + a small Cocoa shim
  (platform_darwin.h/.m: cursor via CGEventCreate, screens via
  NSScreen with bottom-left->top-left conversion, MoveWindow via
  setFrameOrigin on the first NSWindow, all on the main thread).
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
  wait, abandoned callbacks self-clean) and cs_mint(desktop_id) --
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
  ~/Applications *.app scan (Exec = `open -a "<path>"`).
  windows/darwin files compile only on their OSes -- the CI `linux`
  job builds linux/amd64 + a windows/amd64 cross-compile but only
  ever RUNS the linux binary, and the `darwin` job cgo-compiles
  darwin/arm64 + runs the unit-test suite on a mac runner (no GUI
  run) -- so keep them boring and conventional.
- `wails.json` -- Wails CLI project config (app name, frontend
  install/build commands) read by `wails dev`/`wails build` only; the
  no-CLI go-toolchain path does not use it.
- `frontend/` -- vanilla TypeScript + Vite. No framework. `index.html`
  (query row with inline SVG magnifier + hidden bang chip; #results
  split into #file-results / static #empty ("No matches") /
  #plugin-results zones; status bar + degraded chip + backend chip;
  the #stats row
  BELOW the status bar -- the bottom-most chrome, five STATIC
  label/value span pairs (CPU GPU RAM SWP NET, value ids
  stat-cpu/-gpu/-ram/-swap/-net), starts hidden, JS only ever writes
  the value text; #preview-pane
  (spinner + #preview-body + command strip with the web/AI buttons
  and the pane flash) as one more #bar child, display:none unless
  body.with-preview; <template>s for
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
  set_query and blank queries never commit;
  "plugin:results" emissions are dropped unless gen === seq, else
  upsert that plugin's section (keyed by id) and re-render the plugin
  area BELOW the file rows, never displacing them; selection is one
  flat list, file rows then plugin rows: ArrowUp/Down wrap, Home/End,
  hover; file rows Enter=Open / Ctrl/Cmd+Enter=Reveal; plugin rows run
  their action on Enter/click (Ctrl+Enter identical): set_query stays
  frontend-local (replace input, caret to end, re-run the pipeline),
  everything else goes to RunPluginAction -- Go owns bar-hide per
  action type; copy_text and run_builtin "version" stay open and flash
  "Copied" ~1.2s in the status bar, action errors -- plugin actions
  AND file-row open/reveal failures -- flash ~2s; #empty
  shows only when a non-blank query has neither files nor sections;
  Esc + window blur -> Hide; runtime events: "app:shown" -> CLEAR
  the input (the bar always summons empty; the pre-hide text is
  deliberately dropped) + reset histCursor + focus + refresh (renders
  the cheat sheet; plugins re-query through the same path) + a
  refreshStats re-render (GetStats is the instant cached snapshot;
  the summon's fresh samples follow as events),
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
  renders as "<down>rx <up>tx" arrow pairs; any *Ok=false -> em-dash
  placeholder, and swapOk=true with swapTotal 0 (no swap configured)
  is a dash too; glyphs (em dash, arrows) are \uXXXX escapes --
  ASCII-only source)
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
  render as literal glyphs); accent_color is ONLY ever applied by
  setting the `--plugin-accent` custom property on the row -- never
  inline color styles) + `src/theme.ts` (initTheme called first in
  wire(): fetches GetTheme and sets each token as `--sb-<k>` on
  <html>, injects GetCustomCSS as the text of the single managed
  `<style id="sb-custom-css">`, refetches on "theme:changed") +
  `src/style.css` (Spotlight-ish bar, dark by default; dir ellipsizes
  before the name; thin scrollbar; ALL colors/sizes/effects flow
  through var(--sb-*) -- the :root block holds the dark fallbacks and
  MUST stay identical to internal/theme/builtin/dark.json, enforced
  by internal/theme/sync_test.go; the #stats row block (flex 0 0
  auto single nowrap line so it can never squeeze the results area,
  ~0.85x small font, fg-dim on a --sb-border top border, tokens only
  -- and an explicit #stats[hidden]{display:none} because the
  author-level display:flex would defeat the UA sheet's [hidden]
  rule); appended namespaced plugin block
  (.plugin-*, .bang-chip, .status-flash) where every accent rule
  consumes var(--plugin-accent, var(--accent, #89b4fa)) and a :root
  bridge defines --accent: var(--sb-accent, #89b4fa), so the theming
  design tokens apply when present and the standalone default
  otherwise, merge order irrelevant; plus the appended .preview-* /
  body.with-preview block: with-preview turns #bar into a grid --
  680px left column holding query row/results/status/stats row
  exactly as before (four explicit rows; the pane spans 1 / 5 and a
  hidden #stats collapses its row to zero), pane in the rest behind
  a border-left divider, minmax(0,..)
  tracks so pane content scrolls instead of growing the window --
  and without the class every preview rule is inert, so flag-off
  layout is behavior-identical to the classic bar; CI screenshots run
  preview-off and must stay that way, the 780x550 default-geometry
  window regex in
  screenshots.ts depends on it) + `src/preview.ts` (ALL pane logic;
  initPreview is called from wire() with the GetPreviewConfig answer
  and installs NOTHING when enabled is false -- no body class, no
  listeners, hooks no-op; enabled: sets body.with-preview, subscribes
  "preview:result" (drop unless payload.gen === its own previewGen
  counter, cancel the 150ms-delayed spinner on the first accepted
  payload, REPLACE the pane content per emission -- a fast meta card
  precedes the rich payload, cache hits skip it), and registers its
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
  window field and the internal desktop_id the frontend echoes back
  unchanged)/PluginResult/PluginEmission plus the preview contract
  Preview{Target,Payload,ConfigInfo,MetaRow,Text,Image,Dir,DirEntry,
  Web,WebResult,AI} and the four preview bound methods -- keep in
  sync with internal/app + internal/plugin + internal/preview +
  internal/sysstats payload
  structs).
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
  (loaders ignore unknown top-level keys).

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
  tags `desktop,production`, no screenshots/deb; hands off the
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
  xvfb/xdotool/imagemagick/x11-utils/openbox -> `npm ci && npm run build`
  in `frontend/` -> `wow-look-at-my/go-toolchain@v1`
  with `targets: linux/amd64,windows/amd64`, `cgo: 'true'`,
  `timeout: '20'`, and env
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
  restored as the explicit job the same day). The old autorelease also
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
