# CLAUDE.md -- competent-search-thing

Cross-platform desktop searchbar (Spotlight-style UI, Everything-style
speed) in Go + Wails v2 + vanilla TypeScript/Vite.

## Architecture map

- `main.go` -- Wails glue only: embeds `frontend/dist` (go:embed),
  configures the window (frameless, always-on-top, start-hidden,
  hide-on-close, fixed 680x460), binds the App object. Deliberately has
  NO test file and stays minimal (see coverage note below).
- `internal/app` -- the Wails-bound App object and its methods
  (Search/Open/Reveal/Hide/Startup/Shutdown). Bound methods appear in
  JS as `window.go.app.App.<Method>`. Holds the `index.Manager`;
  `Startup` kicks off the initial disk walk in a goroutine and, when
  the walk finishes, starts the watch layer (`startWatch`: a
  `watch.Watcher` + `watch.Rescanner` pair); `Shutdown` (wired to
  Wails OnShutdown) stops them cleanly and also flags a still-running
  initial build to skip starting them. `app.Result` is a type alias of
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
  rescanIntervalMinutes, maxResults). Lives under os.UserConfigDir();
  the `COMPETENT_SEARCH_CONFIG_DIR` env var overrides the directory
  (tests rely on this). `Load` never crashes: missing file -> defaults
  written, corrupt file -> defaults + error returned for logging.
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
  UI. `Rescanner` (rescan.go): serialized full rebuilds --
  `Manager.BuildFromDisk` (fresh-store swap; queries never block) then
  `syncWatches` to re-add/drop watches -- triggered by an optional
  interval ticker (config `rescanIntervalMinutes`) and by one-shot
  degradation requests (`Request`), coalesced through a 1-slot channel
  and spaced by `MinGap` (default 30s) so overflow storms cannot cause
  back-to-back walks. Both loops share the lifecycle.go Start/Stop
  plumbing: idempotent Stop, safe before/during Start, no goroutine
  leaks.
- `internal/platform` (later phase) -- global hotkey, display/cursor
  queries, open/reveal implementations per OS.
- `frontend/` -- vanilla TypeScript + Vite. No framework. `index.html`
  + `src/main.ts` + `src/style.css` + `src/wails.d.ts` (ambient types
  for the Wails-injected `window.go` / `window.runtime`).

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
