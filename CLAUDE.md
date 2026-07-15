# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

## Architecture map

- `main.go` -- Wails glue only: embeds `frontend/dist` (go:embed),
  configures the window (frameless, always-on-top, start-hidden,
  hide-on-close, fixed 680x460), binds the App object. Deliberately has
  NO test file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/Startup/Shutdown/QueryPlugins/
  RunPluginAction). Bound methods appear in JS as
  `window.go.app.App.<Method>`. Holds the `index.Manager`; `Startup`
  saves the runtime ctx, registers the global hotkey (once; parse or
  register failure = log once, run on without it), brings the plugin
  layer up once (plugins.go: an appctx.Cache over the plat.appSource
  seam + RefreshInstalledAsync, then the registry via the
  `newRegistry` builder seam, whose production value `buildRegistry`
  re-reads config.json, LoadDirs <configDir>/plugins, passes Version
  and the installedApps getter, and logs every registry Errors()
  entry once with a "plugin:" prefix -- missing plugins dir =
  builtins only, no noise), and kicks the initial disk walk in a
  goroutine; when the walk finishes, `startWatch` brings up a
  `watch.Watcher` + `watch.Rescanner` pair; `Shutdown` (wired to
  Wails OnShutdown) releases the hotkey, cancels the in-flight plugin
  generation + Close()s the registry, and stops rescanner+watcher
  cleanly, also flagging a still-running initial build to skip
  starting them. The hotkey callback `toggle` (rate-limited 250ms
  against key autorepeat) hides the bar when visible; when hidden it
  FIRST captures app context (`captureAppContext`: CaptureFocused +
  RefreshRunningAsync + EnsureFreshInstalled(5m) -- the bar window
  steals focus, so this precedes showing), then
  `showOnCursorDisplay`: platform.CursorDisplays -> PickDisplay ->
  BarPosition (absolute coords), then darwin = native.MoveWindow,
  linux/windows = translate via DisplayForWindow + WailsPosition (Wails
  WindowSetPosition is RELATIVE to the window's current monitor -- and
  to the WORK AREA origin on Windows -- while WindowGetPosition is
  absolute; verified in the v2.13.0 sources), any failure -> WindowCenter;
  then WindowShow + "app:shown". `QueryPlugins(query string, gen
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
  {watched,dropped,overflows}, "app:shown", "plugin:results"
  (payload plugin.Emission {plugin,name,gen,results}). ALL Wails
  runtime calls and platform hooks sit behind seam structs
  (`runtimeSeams` incl. clipboardSetText/quit and `platformSeams`
  incl. run/appSource, in window.go; defaults in New, plus the
  `newRegistry` seam); unit tests MUST replace them (see newTestApp,
  which also nils appSource and stubs newRegistry so no config or
  X11 IO happens) -- real runtime funcs abort the process without a
  Wails context. Open/Reveal call the platform launcher and hide the
  bar on success. `app.Result` is a type alias of `index.Result`
  (the JSON tags path/name/isDir live in internal/index). The app
  `Version` constant lives in plugins.go. Unit-tested.
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
  rescanIntervalMinutes, maxResults, plugins {disabled, entries
  {<id>: {disabled, settings}}}, bangs {sigils, aliases}). Lives under
  os.UserConfigDir(); the `COMPETENT_SEARCH_CONFIG_DIR` env var
  overrides the directory (tests rely on this); `Dir()` exposes that
  directory (the plugins/ dir lives inside it, next to config.json).
  `Load` never crashes: missing file -> defaults written, corrupt file
  -> defaults + error returned for logging. `Normalize` repairs zero
  values (nil plugin entries/bang aliases -> empty maps, empty sigils
  -> the ! / @ defaults); entry settings are opaque json.RawMessage
  forwarded verbatim to that plugin.
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
  back-to-back walks. Both loops share the lifecycle.go Start/Stop
  plumbing: idempotent Stop, safe before/during Start, no goroutine
  leaks.
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
  (injectable `Run` seam; default starts detached and reaps).
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
  windows/darwin files compile only on their OSes -- CI is linux/amd64
  -- so keep them boring and conventional.
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
  inline color styles) + `src/style.css` (dark Spotlight-ish bar; dir
  ellipsizes before the name; thin scrollbar; appended namespaced
  plugin block (.plugin-*, .bang-chip, .status-flash) where every
  accent rule consumes var(--plugin-accent, var(--accent, #89b4fa)) so
  the theming branch can define --accent later) + `src/wails.d.ts`
  (ambient types for the Wails-injected `window.go` / `window.runtime`
  incl. EventsOn, the event payload shapes, and the plugin wire
  contract TargetInfo/PluginAction/PluginResult/PluginEmission -- keep
  in sync with internal/app + internal/plugin payload structs).

## Build / test

- NEVER run bare `go` commands (no `go build`, `go test`, `go vet`,
  `go mod tidy`). The ONLY build/test entry point is `go-toolchain`
  at the repo root.
- Build the frontend FIRST -- `frontend/dist` is embedded and gitignored:

      cd frontend && npm install && npm run build && cd ..
      GOFLAGS=-tags=webkit2_41 go-toolchain --cgo

- `--cgo` is required (Wails Linux webview uses cgo for gtk3/webkit).
- `GOFLAGS=-tags=webkit2_41` is required on webkit2gtk-4.1-only distros
  (Ubuntu 24.04+); go-toolchain passes GOFLAGS through to the go tool.
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
- One branch per session (`claude/searchbar-v1` for the v1 build),
  squash-merged; add follow-up commits rather than rebasing.
- Commit go-toolchain's auto-rewrites as part of your work.

## CI notes

- `.github/workflows/ci.yml` runs on every push (`on: push:`, no
  filters). The single job is named exactly `all-builds` -- the org
  ruleset requires a green `all-builds` status on the head SHA before
  a PR can merge to master. Do not rename it.
- The job: checkout -> apt install gtk/webkit/x11 dev packages ->
  `npm ci && npm run build` in `frontend/` -> `wow-look-at-my/go-toolchain@v1`
  with `targets: linux/amd64`, `cgo: 'true'`, `autorelease: 'false'`,
  `timeout: '20'`, and env `GOFLAGS: "-tags=webkit2_41"`.
- `targets: linux/amd64` because the default target matrix
  (linux,darwin,windows x amd64,arm64) cannot cross-compile a cgo/webkit
  app from a Linux runner.
- `autorelease: 'false'` because buildhost publishing needs the
  `actions: read` permission this workflow does not grant.
- `frontend/package-lock.json` is committed (required by `npm ci`).
