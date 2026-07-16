# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

## Architecture map

- `main.go` -- glue only: embeds `frontend/dist` (go:embed) and calls
  cli.Execute(app.Version, runGUI); runGUI configures the window
  (frameless, always-on-top, start-hidden, hide-on-close, fixed
  680x460), binds the App object and wires OnStartup / OnDomReady /
  OnShutdown. Zero-arg invocation boots the GUI exactly as before the
  CLI existed (CI screenshots rely on that). Deliberately has NO test
  file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/GetTheme/GetCustomCSS/Startup/DomReady/
  Shutdown/QueryPlugins/RunPluginAction). Bound methods appear in JS as
  `window.go.app.App.<Method>`. Holds the `index.Manager`; `Startup`
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
  said no), the gsettings backend calls
  gsettings.EnsureBinding(hotkeyCtx, run, hk,
  gsettings.ToggleCommand(executable seam)) and logs EXACTLY ONE loud
  summary ("hotkey: GNOME keybinding active: <accel>", with
  "(requested <accel> is taken by GNOME; using fallback)" when it
  fell back, or "hotkey: using existing GNOME keybinding <accel>
  (edit in GNOME Settings > Keyboard)"), and a plan that runs dry
  logs the manual bind-a-key-to-'competent-search-thing toggle'
  instructions. The effective summon description (hk.String(), the
  portal's bound-trigger description, or the installed accelerator)
  is stored on the App (a.hotkeyDesc, read via hotkeyDescription())
  for future UI use), wires the
  single-instance IPC handlers when Options.IPC is set (Toggle =
  toggle, Show = showIfHidden, Hide = Hide; Options.ShowOnStartup
  latches a pending show), brings the plugin
  layer up once (plugins.go: an appctx.Cache over the plat.appSource
  seam + RefreshInstalledAsync, then the registry via the
  `newRegistry` builder seam, whose production value `buildRegistry`
  re-reads config.json, LoadDirs <configDir>/plugins, passes Version
  and the installedApps getter, and logs every registry Errors()
  entry once with a "plugin:" prefix -- missing plugins dir =
  builtins only, no noise), starts theme hot reload (theme.go: a
  dedicated fsnotify watcher on the config dir + its themes/ subdir,
  events debounced 300ms into "theme:changed"; any failure = log +
  run on without live reload), and kicks the initial disk walk in a
  goroutine (under a cancellable context); when the walk finishes,
  `startWatch` brings up a `watch.Watcher` + `watch.Rescanner` pair;
  `Shutdown` (wired to Wails OnShutdown) closes the IPC server first
  (when present), releases the hotkey (native stop func, cancel of
  the async portal/gsettings chain, idempotent+nil-safe close of the
  active portal handle -- a handle the chain stores after Shutdown
  ran is closed by the chain itself), cancels the in-flight plugin
  generation + Close()s the registry, cancels a still-running
  initial build (its walk aborts promptly, logs "index: initial
  build cancelled", discards the partial store, and never starts the
  watch layer), and stops rescanner+watcher plus the theme watcher
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
  autorepeat) hides the bar when visible; when hidden it FIRST
  captures app context (`captureAppContext`: CaptureFocused +
  RefreshRunningAsync + EnsureFreshInstalled(5m) -- the bar window
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
  whose emit path drops stale generations. `RunPluginAction(pluginID
  string, action plugin.Action) error` RE-validates every action the
  frontend echoes back (defense in depth), logs it, then executes:
  copy_text -> ClipboardSetText (bar stays open); open_path (abs
  path only) and open_url (http/https + host only) -> the open seam;
  run_command (1..16 non-empty <=1024-byte argv) -> the run seam
  (launcher, detached); run_builtin -> rescan (Rescanner.Request;
  friendly error while the index is still building) / reload
  (newRegistry, swap under mutex, Close the old) / config (open
  config.json) / version (copy `Version`, stays open) / quit
  (runtime Quit); everything else hides the bar on success. Events
  emitted (all guarded so a nil ctx no-ops): "index:progress"
  {indexed,done,seconds}, "watch:degraded"
  {watched,dropped,overflows}, "app:shown", "theme:changed" (no
  payload; frontend refetches GetTheme/GetCustomCSS),
  "plugin:results" (payload plugin.Emission
  {plugin,name,gen,results}). ALL Wails
  runtime calls and platform hooks sit behind seam structs
  (`runtimeSeams` incl. clipboardSetText/quit and `platformSeams`
  incl. run/appSource plus getenv/executable/detectSession/
  startPortal/ensureGnomeBinding, in window.go; defaults in New, plus
  the `newRegistry` seam); unit tests MUST replace them (see
  newTestApp, which also nils appSource, stubs newRegistry so no
  config or X11 IO happens, pins getenv to "" and detectSession to
  the unknown session -- keeping every test on the native
  hotkey/positioning path unless it overrides detectSession -- and
  makes startPortal/ensureGnomeBinding recording fakes) -- real
  runtime funcs abort the process without a Wails context. Open/Reveal call the platform launcher and hide the
  bar on success. `app.Result` is a type alias of `index.Result`
  (the JSON tags path/name/isDir live in internal/index). The app
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
- `internal/index` -- the index engine. `Store`: compact
  column-oriented data (interned parent-dir table; lowercased +
  original-case name blobs with 0x00 separators and offset tables;
  tombstone removals). `Store.Query`: case-insensitive substring
  search, sharded across NumCPU goroutines with per-shard bounded
  top-K heaps; ranking exact > prefix > substring, dirs before files,
  shorter then lexicographic paths. `Walk`: parallel walker (worker
  pool + LIFO queue) with exclude patterns (`Excluder`: bare pattern
  = base name, pattern with separator = full path), symlinks indexed
  but never descended, permission errors counted not fatal, throttled
  progress callbacks. `Manager`: owns the RWMutex contract (queries
  RLock, mutations Lock); `BuildFromDisk` walks into a fresh store and
  swaps it in, so queries keep working during rebuilds; `Add`/`Remove`
  are the watcher-phase entry points. A bare `Store` is NOT
  thread-safe. Benchmarks build synthetic 100k/1M-entry stores in
  memory (see bench_test.go) and a ~50k-entry disk tree.
- `internal/config` -- config.json load/save (roots, excludes, hotkey,
  rescanIntervalMinutes, maxResults, theme, plugins {disabled, entries
  {<id>: {disabled, settings}}}, bangs {sigils, aliases}). Lives under
  os.UserConfigDir(); the `COMPETENT_SEARCH_CONFIG_DIR` env var
  overrides the directory (tests rely on this); `Dir()` exposes that
  directory (the plugins/ and themes/ dirs live inside it, next to
  config.json). The app's OTHER env knobs live with their owners:
  `COMPETENT_SEARCH_SOCKET` (internal/ipc, the single-instance socket
  path) and `COMPETENT_SEARCH_HOTKEY_BACKEND` (internal/app hotkey.go,
  backend override) -- all three are documented in the README. `Load` never crashes: missing file -> defaults
  written, corrupt file -> defaults + error returned for logging.
  `Normalize` repairs zero values (empty theme -> dark, nil plugin
  entries/bang aliases -> empty maps, empty sigils -> the ! / @
  defaults); entry settings are opaque json.RawMessage forwarded
  verbatim to that plugin.
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
  (wired into the app by internal/app's plugins.go). schema.go:
  versioned JSON wire protocol
  (Request/Response/Result/Action, v=1) and `SanitizeResponse`, which
  clamps/validates everything an external plugin returns: 20-result
  cap, rune caps (title 200/subtitle 300/badge 24/field 40+200, max 8
  fields), control chars -> spaces everywhere, icon = builtin name or
  <=32-byte glyph, accent_color regex, score default 50 clamp 0..100,
  action validation (open_path abs path, open_url http(s)+host,
  copy_text <=8 KiB, run_command 1..16 argv <=1024 B each and the
  whole RESULT is dropped unless the manifest sets allow_run_command;
  internal-only set_query/run_builtin always stripped; anything
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
  => normal trigger fan-out on the raw query. `Close()` drops idle
  HTTP connections; reload = build a new Registry, swap atomically,
  Close the old. Builtins (targeted-only, in-process, no sanitizer):
  builtin_bangs.go "bangs"/Commands -- bang completions (resolved
  bang first, primary-sigil titles, typed-sigil set_query preserving
  the query rest, cap 12); builtin_app.go "app"/App Commands --
  !rescan/!reload/!config/!version/!quit, one run_builtin result each
  (version subtitle from Options.Version); builtin_apps.go
  "apps"/Launch -- !app/!launch over the Options.InstalledApps
  snapshot (empty query = first 15 alphabetical, prefix 100 /
  substring 80, cap 15, run_command argv via `parseDesktopExec`:
  quotes, backslash escapes, %-field codes stripped). Exhaustively
  unit-tested, table-driven, plus an end-to-end manifest ->
  registry -> /bin/sh transport dispatch test.
- `internal/appctx` -- app-context collection for the plugin system,
  pure and headless-tested: the data types (AppInfo / InstalledApp /
  Snapshot -- deliberately NOT internal/plugin's wire types, the app
  layer converts), the `Source` seam implemented by
  internal/platform/native, and `Cache` (mutex-guarded, injectable
  clock): `CaptureFocused` = synchronous focused-app read at
  hotkey-press BEFORE the window steals focus;
  `RefreshRunningAsync` / `RefreshInstalledAsync` = single-flight
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
- `internal/watch` -- keeps the index live after the initial walk.
  `Watcher` (watch.go + events.go): one fsnotify watch per live indexed
  directory plus the roots -- fsnotify is used uniformly on ALL
  platforms and a watch is never recursive anywhere, so directories
  gain/lose watches as they appear/vanish. Events are debounced
  (debounce.go: flush after a quiet window, ~250ms, or when the oldest
  pending event hits ~1s, or at 4096 pending; thresholds injectable via
  `Options` for tests) and applied to the Manager as one ORDERED batch,
  so create-then-delete ends deleted and delete-then-create ends live.
  Event mapping: Create -> Lstat, `Manager.Add` (symlinks indexed as
  non-dirs, never followed), new dirs get a watch + subtree scan via
  `Manager.Add` (dedup-safe); Remove/Rename(old name) ->
  `Manager.Remove` (subtree tombstone) + watches under the path
  dropped; Write/Chmod ignored. Excluded paths are filtered with the
  SAME `index.Excluder` the walks use, before they touch the index.
  The fsnotify interaction sits behind the tiny `notifier` seam
  (notify.go), so unit tests inject scripted Add failures, overflow
  errors, and synthetic event sequences; integration tests run the
  real inotify backend. Degradation model (never crash, never spin): a
  refused watch (inotify max_user_watches) is counted, logged once,
  and skipped; an event-queue overflow means lost events, so the
  watcher asks the Rescanner for a reconcile rescan; `Degraded()` /
  `Stats()` (watched/dropped/overflow counts) expose the state for the
  UI, and `Options.OnDegraded` (edge-triggered, called at most once --
  the flag is sticky) pushes the first transition to the app, which
  forwards it as the "watch:degraded" event. `Rescanner` (rescan.go):
  serialized full rebuilds --
  `Manager.BuildFromDisk` (fresh-store swap; queries never block) then
  `syncWatches` to re-add/drop watches -- triggered by an optional
  interval ticker (config `rescanIntervalMinutes`) and by one-shot
  degradation requests (`Request`), coalesced through a 1-slot channel
  and spaced by `MinGap` (default 30s) so overflow storms cannot cause
  back-to-back walks. Stop cancels promptly at ANY point of the cycle
  (fast quit): an in-flight rebuild aborts mid-walk (partial store
  discarded, previous kept, logged "watch: rescan cancelled"), an
  in-flight `syncWatches` stops between directories (it takes the
  rescan loop's ctx; the swapped-in rebuilt store stays), a MinGap
  wait is cut short, and still-queued requests are dropped. Both
  loops share the lifecycle.go Start/Stop
  plumbing: idempotent Stop, safe before/during Start, no goroutine
  leaks.
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
  Requested, FellBack, Changed, Existing}`: reads the media-keys
  custom-keybindings list; if the app's entry (fixed path ...
  /custom-keybindings/competent-search-thing/) exists it is STICKY --
  the binding is never rewritten (user edits in GNOME Settings
  survive; Existing=true, zero writes) and only a stale command
  (moved binary) is refreshed; a fresh entry gets the first free
  candidate of [configured, <Control><Alt>space, <Super>space]
  (normalization-deduped) checked against every accelerator in the
  wm/mutter/mutter.wayland/shell/media-keys schemas
  (`list-recursively`, arrays-of-strings only) plus every OTHER
  custom entry's binding (capped 64) -- because mutter silently
  refuses conflicting grabs and GNOME 46 defaults take BOTH Alt+Space
  (activate-window-menu) and Super+Space (switch-input-source); all
  candidates taken = sentinel ErrAllTaken, nothing written. Writes
  are GVariant text (single-quoted, parsed+serialized by tiny
  in-package helpers, incl. the "@as []" empty form); the scan
  tolerates missing schemas/entries but list/entry read and all write
  failures are fatal. `ToggleCommand(exe)` builds the GLib-shell-safe
  "<exe> toggle" command. Exhaustively unit-tested against scripted
  runners (exact argv sequences, idempotent second run = zero sets)
  plus a LookPath-guarded smoke test of the real CLI.
- `internal/platform` -- the PURE half of the platform layer, fully
  unit-tested headlessly: `ParseHotkey` ("alt+space", "ctrl+shift+k";
  modifiers ctrl/control, shift, alt/option, super/win/cmd/meta; keys
  space/tab/enter/return/esc/escape/a-z/0-9/f1-f12/arrows; unknown
  token -> error naming it) into an OS-neutral `Hotkey{Mods,Key}`;
  geometry (`Rect`, `Display{Rect,Work,Primary}`, `PickDisplay`,
  `BarPosition` = centered, top at H/3 - winH/3, clamped;
  `DisplayForWindow` by window center; `WailsPosition` translating
  absolute coords to Wails' current-monitor-relative
  WindowSetPosition); open/reveal argv construction (`OpenCommands` /
  `RevealCommands`: linux xdg-open / dbus-send FileManager1.ShowItems
  with xdg-open-parent fallback, darwin open / open -R, windows
  rundll32 FileProtocolHandler / explorer /select,) and `Launcher`
  (injectable `Run` seam; default starts detached and reaps); session
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
  Also per OS: `AppSource() appctx.Source` (appsource_*.go), the
  app-context glue -- linux = EWMH over conn-per-call xgb
  (_NET_ACTIVE_WINDOW / _NET_CLIENT_LIST -> per-window _NET_WM_PID,
  WM_CLASS class for Name, _NET_WM_NAME falling back to WM_NAME for
  Title, exe/comm via appctx.ProcInfo("/proc", pid); RunningApps
  dedupes by pid keeping the first window's title, skips pid==0, caps
  64, sorts by Name; InstalledApps = appctx.ScanDesktopDirs; no X ->
  ok=false); windows = GetForegroundWindow / EnumWindows (package-
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
  windows/darwin files compile only on their OSes -- CI builds
  linux/amd64 + a windows/amd64 cross-compile but only ever RUNS the
  linux binary, and darwin is never compiled at all -- so keep them
  boring and conventional.
- `wails.json` -- Wails CLI project config (app name, frontend
  install/build commands) read by `wails dev`/`wails build` only; the
  no-CLI go-toolchain path does not use it.
- `frontend/` -- vanilla TypeScript + Vite. No framework. `index.html`
  (query row with inline SVG magnifier + hidden bang chip; #results
  split into #file-results / static #empty ("No matches") /
  #plugin-results zones; status bar + degraded chip; <template>s for
  folder/file icons AND plugin section/row skeletons) + `src/main.ts`
  (search as-you-type: 15ms debounce + sequence-number stale-response
  drop; every generation also fire-and-forgets QueryPlugins(query,
  seq) -- INCLUDING the empty query, which is the Go-side cancel
  signal -- and updates the bang chip from the returned TargetInfo;
  "plugin:results" emissions are dropped unless gen === seq, else
  upsert that plugin's section (keyed by id) and re-render the plugin
  area BELOW the file rows, never displacing them; selection is one
  flat list, file rows then plugin rows: ArrowUp/Down wrap, Home/End,
  hover; file rows Enter=Open / Ctrl/Cmd+Enter=Reveal; plugin rows run
  their action on Enter/click (Ctrl+Enter identical): set_query stays
  frontend-local (replace input, caret to end, re-run the pipeline),
  everything else goes to RunPluginAction -- Go owns bar-hide per
  action type; copy_text and run_builtin "version" stay open and flash
  "Copied" ~1.2s in the status bar, action errors flash ~2s; #empty
  shows only when a non-blank query has neither files nor sections;
  Esc + window blur -> Hide; runtime events: "app:shown" ->
  focus+select+refresh (plugins re-query through the same path),
  "index:progress" -> status text, "watch:degraded" -> warning chip)
  + `src/render.ts` (pure text-node DOM builders, no innerHTML
  anywhere: file rows with highlighted match + dim parent dir; plugin
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
  by internal/theme/sync_test.go; appended namespaced plugin block
  (.plugin-*, .bang-chip, .status-flash) where every accent rule
  consumes var(--plugin-accent, var(--accent, #89b4fa)) and a :root
  bridge defines --accent: var(--sb-accent, #89b4fa), so the theming
  design tokens apply when present and the standalone default
  otherwise, merge order irrelevant) + `src/wails.d.ts` (ambient
  types for the Wails-injected `window.go` / `window.runtime` incl.
  EventsOn, the event payload shapes, and the plugin wire contract
  TargetInfo/PluginAction/PluginResult/PluginEmission -- keep in sync
  with internal/app + internal/plugin payload structs).
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
  filters). The single job is named exactly `all-builds` -- the org
  ruleset requires a green `all-builds` status on the head SHA before
  a PR can merge to master. Do not rename it.
- The job: checkout -> apt install gtk/webkit/x11 dev packages plus
  xvfb/xdotool/imagemagick/x11-utils/openbox -> `npm ci && npm run build`
  in `frontend/` -> `echo gomemlimit_gen.go >> .git/info/exclude` (the
  transient guard go-toolchain injects would otherwise stamp every
  published binary vcs.modified/+dirty) -> `wow-look-at-my/go-toolchain@v1`
  with `targets: linux/amd64,windows/amd64`, `cgo: 'true'`,
  `autorelease: 'true'`, `timeout: '20'`, and env
  `GOFLAGS: "-tags=webkit2_41,desktop,production"` -> deb build +
  publish (next bullet) -> screenshot capture ->
  `actions/upload-artifact@v4`.
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
- Targets: linux/amd64 is the only cgo (gtk/webkit) target;
  windows/amd64 cross-compiles pure-Go from the Linux runner (Wails
  uses WebView2 on windows, and Go auto-disables cgo for non-host
  targets) but is never RUN in CI. darwin needs cgo against the Apple
  SDK, so it cannot be built here -- never add darwin targets.
- `autorelease: 'true'` publishes the `build/` binaries (built with the
  full GOFLAGS tags, so they are runnable) to buildhost (pazer.build)
  on EVERY branch push, with the git branch recorded: project
  `competent-search-thing` (the app) plus `competent-search-thing/server`
  (the color-http example server, per the multi-binary naming
  convention). Versions auto-increment; the bare `latest` download URL
  resolves the repo's default branch (master). Requires the
  `actions: read` permission (to fetch the `go-build` run artifact) on
  top of `id-token: write` (OIDC auth to buildhost) -- both are in the
  workflow's permissions block. Install commands: README "Install".
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
  Escape-hides assertion. The app window is found by name + 680x460
  geometry in `xwininfo -root -tree` (xdotool search --onlyvisible
  --class does not match it). One full retry with
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
