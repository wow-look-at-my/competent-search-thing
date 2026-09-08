# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

This file is an INDEX: what exists, the invariants a change must not
break, and where the depth lives. Every request of every session pays
for it verbatim, so keep it under 40,000 characters and keep each entry
to a few lines. When an entry needs more, move the prose to
`docs/<topic>.md` unchanged and leave a pointer -- extract, never
summarize, and never grow an entry to absorb it. The per-package
manuals already live there (docs/app-object.md, docs/frontend.md,
docs/index-engine.md, docs/ci.md, ...), one file per map entry.

## Architecture map

- `main.go` -- glue only: embeds `frontend/dist` (go:embed) and calls
  cli.Execute. runGUI configures the searchbar's window (frameless,
  always-on-top, start-hidden, hide-on-close) while
  RunOptions.ConfigWindow routes to runConfigWindow's ordinary
  settings window. Zero-arg invocation must keep booting the GUI (the
  CI screenshots depend on it). Deliberately has NO test file and
  stays minimal -- go-toolchain does not profile packages without
  tests, so testable logic belongs in internal/*. See docs/main.md.
- `internal/app` -- the Wails-bound App object: every method the
  frontend calls, the Startup/DomReady/Shutdown ordering, the config
  live-apply engine, and the seam structs (`runtimeSeams`,
  `platformSeams`, the `new*` builders) that unit tests MUST replace
  -- calling a real Wails runtime function without a Wails context
  aborts the process, so tests build through `newTestApp`.
  See docs/app-object.md.
- `internal/cli` -- the cobra command line, the real process entry
  point (main.go calls cli.Execute). One self-registering subcommand
  per file; every summon-shaped path treats refused / reset / EOF /
  timeout / garbage UNIFORMLY as "no healthy instance" and becomes
  the instance, so a wedged or version-skewed daemon is replaced
  instead of surfacing a dead end. `config` is the exception that
  starts nothing shared: it listens on the SETTINGS window's own
  socket. See docs/cli.md.
- `internal/ipc` -- the single-instance unix-socket IPC layer, pure
  and headless-tested. JSON is the ONLY wire shape, unknown fields
  are ignored both ways (the tolerance contract that lets commands be
  added safely), the ack is written BEFORE the handler runs, and
  Listen self-heals: it probes an EADDRINUSE holder for liveness
  under a flock and takes over a dead or skewed one -- never by
  pattern kill, always by the pid off the socket's peer credentials.
  See docs/ipc.md.
- `internal/ffext` -- the Firefox companion-extension bridge behind
  switch-to-tab: native-messaging frames on one side, JSON lines on a
  SECOND unix socket on the other. Its constants are LOCKSTEP with
  webextension/logic.mjs (sync_test.go, a hard gate).
  See docs/ffext.md.
- `internal/service` -- the per-user login-service layer (launchd
  LaunchAgent on darwin, systemd user unit on linux), pure logic over
  an injectable Runner. Ensure() is the app's automatic first-run
  registration and NEVER starts, restarts or bootstraps anything: the
  running app may BE the service instance, and a darwin bootstrap of a
  RunAtLoad agent would launch a second copy. See docs/service.md.
- `internal/watchsetup` -- the automatic optimal-watch setup that runs
  BEFORE the GUI, so a fresh Linux install comes up on whole-filesystem
  fanotify instead of the per-directory fallback: probe, then pkexec +
  setcap, then re-exec into the capable binary. A decline is remembered
  per binary identity and never re-asked; `setup-watch` and the config
  editor's button are the forced retry. See docs/watchsetup.md.
- `internal/match` -- THE shared matching engine, pure (stdlib only),
  consumed by internal/index AND internal/plugin: one fold definition,
  one tier ladder, one multi-term semantics, one position-aware scorer
  and one ranking mint. Ranked's fields are unexported with no
  constructor, so ONLY Rank can mint a score. See docs/match.md.
- `internal/index` -- the index engine: a compact column-oriented
  Store (interned dir table, one name blob, tombstone removals),
  sharded case-insensitive queries with per-shard top-K heaps, the
  path/fuzzy/multi-term modes, and the Manager's
  RWMutex contract (queries RLock, mutations Lock; BuildFromDisk
  swaps a fresh store so queries never block). A bare Store is NOT
  thread-safe, and findChild MUTATES, so it stays write-path only.
  The traversal is `github.com/wow-look-at-my/go-fs-tree-fast`
  (package fstree): walk.go here holds the readDirFn seam, alias
  re-exports of ProgressFunc/WalkStats/Excluder, and storeSink.
  See docs/index-engine.md.
- `internal/config` -- config.json load/save under os.UserConfigDir()
  (`COMPETENT_SEARCH_CONFIG_DIR` overrides; `Dir()` exposes it). Load
  NEVER crashes -- a corrupt file yields defaults plus an error to
  log -- Normalize repairs zero values, Save is atomic, and
  rootsVersion (current 9) drives the one-shot migration ladder whose
  every user-visible change lands in MigrationNotes: the indexing
  scope never changes silently. Boolean knobs are all the affirmative
  `enabled` spelling, *bool so absent stays distinguishable from an
  explicit false. See docs/config.md.
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
  config.json's history.persistEnabled=false opts out).
- `internal/frecency` -- the PURE half of result prioritization: the
  decayed open-count store, the path-noise penalty, the atime/mtime
  recency probe and the focused-app cwd derivation. Every signal is a
  rank NUDGE, never an exclusion, and the zero value degrades to a
  total no-op. The blend that consumes them lives in internal/index.
  See docs/frecency.md.
- `internal/priors` -- the PURE half of the pick-memory ranking priors
  (exact-query pick memory plus smoothed per-extension and per-dir
  rates), read from telemetry.jsonl and frecency.json. Memory is
  hard-capped and a nil receiver is a total no-op; PriorFunc resolves
  ONCE per query, never per candidate. See docs/priors.md.
- `internal/arbiter` -- the PURE half of learned composition
  arbitration: one feature definition shared by training and serving,
  pairwise logistic SGD over the local ranking log, and an ACTIVATION
  GATE the model must pass (enough joined picks, and it must strictly
  beat the delivered order on a held-out time split) before it changes
  anything. Its file delta is clamped under the blend's tier-jump
  threshold, so it can never invert a match class.
  See docs/arbiter.md.
- `internal/telemetry` -- the ALWAYS-ON local ranking log: one
  append-only JSONL record per pick at <configDir>/telemetry.jsonl,
  size-capped to two generations. Deliberately no off switch -- it is
  private by staying on the machine. Feature values are joined from
  the app's own impression ring, never from the frontend's report, so
  a frontend cannot forge them. See docs/telemetry.md.
- `internal/sysstats` -- the system-stats sampler behind the
  frontend's stats row (linux /proc + sysfs, darwin mach/sysctl/IOKit,
  elsewhere placeholders). THE invariant: nothing outside the sampler
  goroutines ever does IO -- Snapshot() is a mutex-guarded copy and a
  hidden bar samples nothing, so a start-hidden app reads zero bytes
  until the first summon. See docs/sysstats.md.
- `internal/terminal` -- resolves the terminal emulator a command
  should run in, pure over injectable GOOS/Getenv/LookPath seams so
  the whole matrix is headless-tested (the internal/gsettings
  pattern). `Detect(Options)` -> (Terminal{Name, Path,
  SupportsArgs}, ok): unix = $TERMINAL if it resolves (argument
  convention from the table, "-e" for an unknown name) else the
  first hit of unixCandidates -- x-terminal-emulator FIRST (on
  Debian-family systems it IS the user's choice), then the
  desktop-environment terminals, then the minimal ones -- each with
  the flag that takes a command as SEPARATE trailing arguments
  ("--" gnome-terminal, "-x" the xfce4/mate/terminator family,
  nothing for kitty/foot, "start --" wezterm, "-e" for the rest);
  darwin = `open -a Terminal` with SupportsArgs FALSE (it starts a
  program, it cannot carry a command line); windows = wt.exe else
  `cmd.exe /c start "" cmd.exe /k`. `Terminal.Command(argv)` builds
  the full argv, nil for an empty command or an argument-carrying
  one on a !SupportsArgs terminal. DELIBERATELY no terminal whose
  run-a-command flag wants ONE re-quoted shell string (tilix -e):
  re-quoting is where launchers get command injection wrong, and
  every supported terminal takes the words as separate arguments.
  Consumed by internal/app runterm.go (the terminalRunner builder
  over plat.lookPath, logged once) and internal/plugin
  builtin_runterm.go.
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
- `internal/plugin` -- the plugin system, pure and headless-testable.
  Builtins are candidate SOURCES ranked through internal/match, so
  mintResults is the ONLY path stamping Score/MatchRanges; external
  plugins go through SanitizeResponse first and can never set the
  section priority. A manifest can never shadow a builtin id or bang.
  See docs/plugin.md.
- `internal/launch` -- the pure decision half of "focus and raise on
  launch": handler resolution, activation credentials, .desktop Exec
  expansion, the org.freedesktop.Application D-Bus call and the X-side
  raise watcher. An Exec whose program token does not survive a
  target-less expansion is unlaunchable -- the target must never
  become argv[0]. See docs/launch.md.
- `internal/preview` -- the preview-pane engine, pure (no Wails
  imports): file/dir/image/text previews under per-request timeouts,
  plus the Kagi web search and the ONE user-described AI endpoint.
  Nothing is ever fetched automatically -- FetchWeb/FetchAI are the
  frontend's explicit Ctrl+K / Ctrl+I triggers -- and keys and base
  URLs never reach a log, an error or a payload.
  See docs/preview.md.
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
  the bound ResolveIcons (keys "dir", "file:<base>", "app:<ref>",
  "favicon:<pageURL>"). Every input dir and external command sits
  behind an Options seam; every payload is sniffed by magic bytes, so
  a declared type is never trusted and a miss falls back to the
  builtin glyph. See docs/icons.md.
- `internal/fileicons` -- the per-file-type icon MAPPING layer: the
  committed artifact data.bin (a binpazer container from
  wow-look-at-my/bin-file-fmt -- one IconRules user block, GUID
  IconRulesGUID, CRC-32C trailer -- whose payload is the compact
  encoding v1 written by frontend/src/fileicons/tools/emitbin.mjs;
  writer and reader are lockstep-pinned by the committed-artifact
  tests) go:embedded beside its decoder. The container walk uses the
  format's FIRST-PARTY Go reader (module
  github.com/wow-look-at-my/bin-file-fmt/go -- the /go suffix is the
  module root; a private org module, fetched via the git insteadOf
  shim, GOPRIVATE locally); ONLY the payload layer is implemented
  here (bounds-checked cursor, reserved-bit/range/exclusivity
  validation, count-vs-size claims before allocation, unknown
  ancillary blocks skipped, unknown critical refused). `Load()` =
  cached decode that NEVER fails (error -> one log line + the
  fallback table: zero rules, the octicon file-text/file-directory
  defaults); `DecodeTable(data)` is the testable entry. Wire shape
  (json tags fileRules/dirRules/defFile/defDir; rules
  font/cp/suffix/regex/flags/dark/light) feeds internal/app's
  GetFileIcons bound method (fileicons.go there), fetched once by
  the frontend at wire-up. Tests: the retargeted committed-data
  integrity gate (counts 2363/51, shapes, pack-content pins incl.
  the recovered Devicons-face and icon-class rules,
  corruption/hardening over first-party-writer-built containers) +
  woff2cmap_test.go, the Go port of tools/woff2cmap.mjs (test-only;
  github.com/andybalholm/brotli) cross-checking every rule codepoint
  against the five committed woff2 cmaps -- the tofu-glyph drift
  gate, reading the fonts from frontend/src/fileicons/fonts/.
- `internal/appctx` -- app-context collection for the plugin system
  (focused app, running apps, installed apps, open windows) behind a
  Source seam, plus the /proc process-tree snapshot the cwd derivation
  reads. CaptureFocused is synchronous and runs BEFORE the bar steals
  focus; every refresh is single-flight, never blocks a caller, and
  keeps old data on failure. See docs/appctx.md.
- `internal/firefox` -- the Firefox data layer (frequent sites, open
  tabs, favicons), pure and headless-tested. It NEVER opens a live
  Firefox database: places.sqlite and favicons.sqlite are copied to a
  temp dir and read from the copy. Pure-Go modernc.org/sqlite only --
  a cgo driver would break the windows cross-compile.
  See docs/firefox.md.
- `internal/watch` -- keeps the index live after the initial walk:
  Watcher (a bounded hot set of fsnotify watches, or a
  whole-filesystem fanotify/FSEvents backend), Sweeper and Rescanner.
  Their CONTRACT is identical final index state, differing only in
  latency -- an event is only a DIRTY PATH and lstat at apply time
  decides, so application is order-independent by construction.
  Degrades, never crashes and never spins. See docs/watch.md.
- `internal/portal` -- XDG Desktop Portal GlobalShortcuts client over
  godbus, the Wayland-native global-hotkey path. A session may attempt
  BindShortcuts exactly once (the portal remembers approvals across
  sessions, keyed on a ShortcutID that must stay stable), and a denied
  response STOPS the chain -- never write a keybinding after the user
  said no. See docs/portal.md.
- `internal/tray` -- the tray icon: StatusNotifierItem + dbusmenu
  spoken DIRECTLY over godbus, no cgo and no GTK tray library, so
  nothing fights Wails for a main loop. It opens a PRIVATE
  never-autostarting bus connection: no bus means one quiet log line
  and a degraded run, never an error. See docs/tray.md.
- `internal/gsettings` -- the GNOME custom-keybinding fallback for
  Wayland GNOME sessions whose portal lacks GlobalShortcuts, pure over
  an injectable Runner. The app's entry is STICKY (a user's edit in
  GNOME Settings survives) and fresh writes go entry keys FIRST, list
  append LAST -- gsd drops an entry whose command is still empty when
  the list changes, so never simplify that back to list-first.
  See docs/gsettings.md.
- `internal/platform` -- the PURE half of the platform layer, fully
  unit-tested headlessly: hotkey parsing, display geometry and the
  clamp-to-screen rule, open/reveal argv construction, the observed-
  grace Launcher, session detection, and the stable-vs-resolved
  executable paths (StableExecutable for anything that outlives the
  process, ResolvedExecutable for setcap, which refuses symlinks).
  See docs/platform.md.
- `internal/platform/native` -- the thin OS glue (X11/EWMH, Cocoa,
  win32, the GTK-thread launch mint), DELIBERATELY with NO test files:
  it needs a live display server, and go-toolchain skips coverage for
  packages without tests. Keep it minimal and defensive; logic worth
  testing belongs in internal/platform. See docs/platform-native.md.
- `wails.json` -- Wails CLI project config (app name, frontend
  install/build commands) read by `wails dev`/`wails build` only; the
  no-CLI go-toolchain path does not use it.
- `frontend/` -- vanilla TypeScript + Vite, no framework, with a
  vitest + jsdom suite that is a hard CI gate. Two UIs from one
  bundle: GetStartupMode picks the searchbar or the settings editor.
  Conventions that are load-bearing: NO innerHTML anywhere except
  highlight.ts's setHighlighted, plugin accent colors reach CSS ONLY
  through the --plugin-accent custom property, style.css's :root block
  is sync_test-locked to internal/theme's dark.json, and wails.d.ts is
  kept in lockstep with the Go payload structs' json tags.
  See docs/frontend.md.
- `webextension/` -- the shipped Firefox companion extension behind
  switch-to-tab (MV2, persistent background page -- an MV3 event page
  idles out and the host can never wake it, since native-messaging
  connections are always extension-initiated; permissions exactly
  [nativeMessaging, tabs]; pinned gecko id = ffext.ExtensionID).
  logic.mjs is ALL the logic, pure and importable (constants +
  tabRow/listTabs/handleMessage -- tabRow projects favIconUrl onto
  the wire (the "tabs" permission grants it; the sync_test-pinned
  favicon hint the app feeds its icon resolver), activate is
  tabs.update THEN
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
  No em-dashes, no smart quotes, no unicode glyphs. ENFORCED by the
  `Check sources are ASCII-only` CI step (ci.yml, linux job: every
  tracked text-extension file, reported as file:line). The recurring
  trap is gofmt's typographic substitution inside DOC COMMENTS: a
  comment containing a doubled straight quote (the `'\''` shell splice
  is the one that keeps happening) is rewritten to a curly quote, which
  both violates this rule and leaves the tree non-canonical for
  whichever gofmt build runs next -- word such comments so no quote
  pair appears (see internal/ffext manifest.go shQuote and
  internal/watchsetup shellSingleQuote, both worded around it).
  Intentional non-ASCII TEST DATA is written as a `\uXXXX` escape
  (internal/icons mimedb_test.go's U+212A case), which keeps the value
  identical and the source ASCII.
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

## CI

`.github/workflows/ci.yml` runs on every branch push. Its `branches: ['**']` filter excludes only tag pushes, and go-toolchain fails a push trigger that names no filter. Three jobs -- `linux` (frontend build + vitest, the go-toolchain build, the deb, the Xvfb screenshot capture), `darwin` (a mac runner: cgo build, the full unit-test suite, and the GUI smoke script) and `publish` (one buildhost release per push) -- plus the org-required `all-builds` commit status, which the required-builds-manager app
posts by tallying those jobs itself.

- NEVER name a job, check run or workflow `all-builds`: it cannot
  satisfy the gate (the required check is pinned to the app) and only
  shadows the real status. go-toolchain hard-fails the build over it.
- The screenshot step is a HARD GATE, not decoration: a window that
  never maps, a blank capture, a refused hotkey grab or an Escape that
  does not hide the bar fails the job. Treat a failure as a real UI
  regression; if a builtin theme deliberately changed, RE-DERIVE the
  blank-detection bounds from a local run rather than guessing.
- darwin targets must never join the linux job's target list -- darwin
  needs cgo against the Apple SDK.

Every mechanism behind those -- the deb's `Depends` line and why
buildhost's own debs cannot carry one, the cache hand-offs between
jobs, each darwin-smoke SMOKE id, and the screenshot script's fixture
tree, window matching and per-theme bounds -- lives in docs/ci.md.
