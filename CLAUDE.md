# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

## Architecture map

- `main.go` -- Wails glue only: embeds `frontend/dist` (go:embed),
  configures the window (frameless, always-on-top, start-hidden,
  hide-on-close, fixed 680x460), binds the App object. Deliberately has
  NO test file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/GetTheme/GetCustomCSS/Startup/Shutdown).
  Bound methods appear in JS as `window.go.app.App.<Method>`. Holds
  the `index.Manager`; `Startup` saves the runtime ctx, registers the
  global hotkey (once; parse or register failure = log once, run on
  without it), starts theme hot reload (theme.go: a dedicated
  fsnotify watcher on the config dir + its themes/ subdir, events
  debounced 300ms into "theme:changed"; any failure = log + run on
  without live reload), and kicks the initial disk walk in a
  goroutine; when the walk finishes, `startWatch` brings up a
  `watch.Watcher` + `watch.Rescanner` pair; `Shutdown` (wired to
  Wails OnShutdown) releases the hotkey and stops them plus the theme
  watcher cleanly, and also flags a still-running initial build to
  skip starting them. GetTheme re-loads config.json (ONLY the theme
  field is consumed live) and returns theme.Resolve's token map --
  errors are logged once per distinct message and fall back to dark;
  GetCustomCSS returns <configDir>/themes/custom.css verbatim when
  <= 64KB (the unvalidated escape hatch), else "". The hotkey callback `toggle` (rate-limited 250ms
  against key autorepeat) hides the bar when visible, else
  `showOnCursorDisplay`: platform.CursorDisplays -> PickDisplay ->
  BarPosition (absolute coords), then darwin = native.MoveWindow,
  linux/windows = translate via DisplayForWindow + WailsPosition (Wails
  WindowSetPosition is RELATIVE to the window's current monitor -- and
  to the WORK AREA origin on Windows -- while WindowGetPosition is
  absolute; verified in the v2.13.0 sources), any failure -> WindowCenter;
  then WindowShow + "app:shown". Events emitted (all guarded so a nil
  ctx no-ops): "index:progress" {indexed,done,seconds},
  "watch:degraded" {watched,dropped,overflows}, "app:shown",
  "theme:changed" (no payload; frontend refetches
  GetTheme/GetCustomCSS). ALL Wails
  runtime calls and platform hooks sit behind seam structs
  (`runtimeSeams`/`platformSeams` in window.go, defaults in New); unit
  tests MUST replace them (see newTestApp) -- real runtime funcs abort
  the process without a Wails context. Open/Reveal call the platform
  launcher and hide the bar on success. `app.Result` is a type alias of
  `index.Result` (the JSON tags path/name/isDir live in
  internal/index). Unit-tested.
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
  rescanIntervalMinutes, maxResults, theme). Lives under
  os.UserConfigDir(); the `COMPETENT_SEARCH_CONFIG_DIR` env var
  overrides the directory (tests rely on this). `Load` never crashes:
  missing file -> defaults written, corrupt file -> defaults + error
  returned for logging. `Dir()` exposes the directory holding
  config.json (also the parent of themes/).
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
  windows/darwin files compile only on their OSes -- CI is linux/amd64
  -- so keep them boring and conventional.
- `wails.json` -- Wails CLI project config (app name, frontend
  install/build commands) read by `wails dev`/`wails build` only; the
  no-CLI go-toolchain path does not use it.
- `frontend/` -- vanilla TypeScript + Vite. No framework. `index.html`
  (query row with inline SVG magnifier, results list, status bar +
  degraded chip, <template> folder/file icons) + `src/main.ts` (search
  as-you-type: 15ms debounce + sequence-number stale-response drop;
  selection: ArrowUp/Down wrap, Home/End, hover; Enter=Open,
  Ctrl/Cmd+Enter=Reveal, click/ctrl-click likewise, Esc + window blur
  -> Hide; runtime events: "app:shown" -> focus+select+refresh,
  "index:progress" -> status text, "watch:degraded" -> warning chip)
  + `src/render.ts` (row DOM: icon, name with highlighted match, dim
  parent dir; pure text nodes, no innerHTML) + `src/theme.ts`
  (initTheme called first in wire(): fetches GetTheme and sets each
  token as `--sb-<k>` on <html>, injects GetCustomCSS as the text of
  the single managed `<style id="sb-custom-css">`, refetches on
  "theme:changed") + `src/style.css` (Spotlight-ish bar, dark by
  default; dir ellipsizes before the name; thin scrollbar; ALL
  colors/sizes/effects flow through var(--sb-*) -- the :root block
  holds the dark fallbacks and MUST stay identical to
  internal/theme/builtin/dark.json, enforced by
  internal/theme/sync_test.go) + `src/wails.d.ts` (ambient types for
  the Wails-injected `window.go` / `window.runtime` incl. EventsOn
  and the event payload shapes -- keep in sync with internal/app's
  payload structs).

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
- One branch per session (`claude/searchbar-v1` for the v1 build),
  squash-merged; add follow-up commits rather than rebasing.
- Commit go-toolchain's auto-rewrites as part of your work.

## CI notes

- `.github/workflows/ci.yml` runs on every push (`on: push:`, no
  filters). The single job is named exactly `all-builds` -- the org
  ruleset requires a green `all-builds` status on the head SHA before
  a PR can merge to master. Do not rename it.
- The job: checkout -> apt install gtk/webkit/x11 dev packages plus
  xvfb/xdotool/imagemagick/x11-utils/openbox -> `npm ci && npm run build`
  in `frontend/` -> `wow-look-at-my/go-toolchain@v1` with
  `targets: linux/amd64`, `cgo: 'true'`, `autorelease: 'false'`,
  `timeout: '20'`, and env
  `GOFLAGS: "-tags=webkit2_41,desktop,production"` -> screenshot
  capture -> `actions/upload-artifact@v4`.
- `targets: linux/amd64` because the default target matrix
  (linux,darwin,windows x amd64,arm64) cannot cross-compile a cgo/webkit
  app from a Linux runner.
- `autorelease: 'false'` because buildhost publishing needs the
  `actions: read` permission this workflow does not grant.
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
  build/ artifact itself.
- To capture locally: `apt-get install -y xvfb xdotool imagemagick
  x11-utils openbox`, build with the full GOFLAGS above, then follow
  the same sequence (the script is directly readable as the runbook).
- `docs/screenshot.png` is the committed reference image used by
  README.md (the 02-results state, captured from the real app under
  Xvfb). If the UI changes deliberately, recapture and replace it.
