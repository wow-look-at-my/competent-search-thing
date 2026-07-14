# competent-search-thing

A cross-platform desktop searchbar: press a global hotkey, a small
frameless bar pops up on the display your cursor is on, and every
keystroke instantly filters an in-memory index of your file names --
Spotlight-style presentation with voidtools-Everything-style speed.
Built with Go and [Wails v2](https://wails.io), with a tiny vanilla
TypeScript + Vite frontend.

## Screenshot

![The searchbar showing ranked, highlighted results for the query "rep"](docs/screenshot.png)

The real Linux webview, summoned with Alt+Space and captured under Xvfb
against the deterministic fixture tree CI uses (see `.github/scripts/`).
CI re-captures three screenshots like this on every push and uploads
them as run artifacts for visual comparison.

## Status

Feature-complete for v1 (release packaging still pending):

- [x] Window shell (frameless, always-on-top, hidden until summoned) + CI
- [x] Index engine: compact in-memory store, parallel walker, parallel
      ranked substring search, JSON config
- [x] Live index updates: per-directory fsnotify watchers, event
      debouncing, graceful watch-limit/overflow degradation, optional
      periodic rescans
- [x] Global hotkey (default Alt+Space) to summon/dismiss the bar
      (X11 mechanism on Linux -- see caveats; RegisterHotKey on
      Windows; CGEventTap on macOS, needs the Accessibility permission)
- [x] Bar positions itself on the display the cursor is on (falls back
      to centering when the platform cannot say, e.g. Wayland)
- [x] Open / Reveal: Enter opens the selection with the OS default
      handler, Ctrl+Enter (Cmd+Enter on macOS) reveals it in the file
      manager; both hide the bar on success
- [x] Search UI: as-you-type results with match highlighting, dimmed
      parent paths, folder/file glyphs, keyboard + mouse selection,
      live index status bar and a staleness warning chip

## Building

The frontend must be built before the Go binary: `frontend/dist` is
embedded into the binary via `go:embed` and is not checked in.

### Linux prerequisites

```
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev
```

Note on webkit: modern distros (Ubuntu >= 24.04, Debian >= 13) ship
only webkit2gtk **4.1**; Wails v2 defaults to 4.0, so builds need the
`webkit2_41` build tag (see below). On older distros that still have
`libwebkit2gtk-4.0-dev` you can drop the tag.

### With the Wails CLI

```
wails doctor   # verify your environment
wails dev      # live-reload development
wails build -tags webkit2_41   # production build (tag needed on webkit-4.1 distros)
```

### Without the Wails CLI (the path CI uses)

```
cd frontend && npm install && npm run build && cd ..
GOFLAGS=-tags=webkit2_41 go-toolchain --cgo
```

`go-toolchain` (this org's build tool) tidies modules, runs tests with
coverage, and builds into `build/`. CGO must be enabled (`--cgo`)
because the Linux webview binds gtk3/webkit via cgo. On macOS and
Windows the `GOFLAGS` tag is unnecessary.

### macOS

Xcode command line tools are required. Untested in CI (see caveats).

### Windows

WebView2 runtime is required (preinstalled on Windows 11).

## Configuration

Config lives at the platform config dir (set the
`COMPETENT_SEARCH_CONFIG_DIR` environment variable to point at a
different directory):

- Linux: `~/.config/competent-search-thing/config.json`
- macOS: `~/Library/Application Support/competent-search-thing/config.json`
- Windows: `%APPDATA%\competent-search-thing\config.json`

The file is created with defaults on first run:

```json
{
  "roots": ["<your home directory>"],
  "excludes": [".git", "node_modules", ".cache"],
  "hotkey": "alt+space",
  "rescanIntervalMinutes": 0,
  "maxResults": 50
}
```

Field reference:

- `roots` -- the directories to index (default: your home directory).
  Relative paths are made absolute; an empty list falls back to the
  default. Symlinks are indexed as entries but never descended.
- `excludes` -- patterns pruned from indexing (default `.git`,
  `node_modules`, `.cache`). A pattern without a path separator is
  matched against each entry's base name (`node_modules`, `*.tmp`):
  matching directories are pruned, matching files skipped. A pattern
  containing a separator is matched against the full path
  (`/home/*/secret`). `*` never crosses a separator and there is no
  `**`. The same exclude semantics apply to the initial walk, to live
  filesystem events, and to rescans.
- `hotkey` -- the global summon shortcut (default `alt+space`):
  "+"-separated, case- and whitespace-insensitive; modifiers
  `ctrl`/`control`, `shift`, `alt`/`option`, `super`/`win`/`cmd`/`meta`;
  key `space`, `tab`, `enter`/`return`, `esc`/`escape`, `a`-`z`,
  `0`-`9`, `f1`-`f12`, or an arrow (`up`/`down`/`left`/`right`).
  Examples: `alt+space`, `ctrl+shift+k`, `super+space`. An invalid or
  unregistrable hotkey is logged and the app runs on without one.
  Holding the hotkey down does not flicker the bar: OS key autorepeat
  re-fires the shortcut, so toggles are rate-limited to one per ~250ms.
- `rescanIntervalMinutes` -- optional periodic full re-index, a safety
  net on top of the live fsnotify updates; `0` (the default) disables
  the timer. Independent of this timer, a reconcile rescan runs
  automatically when the kernel event queue overflows (see the watcher
  degradation caveat below).
- `maxResults` -- the maximum number of results one query returns
  (default 50; zero or negative values are reset to the default).

## Wails v2 vs v3

This project uses **Wails v2** (latest stable, v2.13.0 at scaffold
time). Wails v3 was still in alpha as of 2026-07 (v3.0.0-alpha2.114,
daily alpha releases); v1 of this app wants a stable, documented base,
so v2 it is. Revisit once v3 ships a stable release.

## Performance

Benchmarks live in `internal/index/bench_test.go` and run on every
`go-toolchain` build. Reference numbers from a 4-CPU (GOMAXPROCS=4)
CI-class container (16 GB RAM, Go 1.25), query limit 50 -- "hits" is
how many indexed entries match; a query still returns only the top 50:

| store | query shape | query    | hits    | ms/query |
|-------|-------------|----------|---------|----------|
| 100k  | rare        | `zzqx`   | 3       | 0.11     |
| 100k  | common      | `data`   | 5,236   | 0.64     |
| 100k  | prefix      | `re`     | 20,209  | 0.90     |
| 100k  | single char | `a`      | 45,771  | 1.03     |
| 100k  | no match    | `qqqqzz` | 0       | 0.10     |
| 1M    | rare        | `zzqx`   | 26      | 0.74     |
| 1M    | common      | `data`   | 52,950  | 6.15     |
| 1M    | prefix      | `re`     | 202,719 | 11.7     |
| 1M    | single char | `a`      | 459,658 | 16.2     |
| 1M    | no match    | `qqqqzz` | 0       | 0.69     |

The worst case measured -- a single-character query matching ~46% of
1,000,000 names -- is 16.2 ms, well inside the 50 ms/keystroke budget;
typical substring queries run sub-millisecond to ~6 ms. The parallel
walker indexes a freshly written ~50k-entry on-disk tree at ~4.6M
entries/s (4 workers, warm page cache -- this measures walker overhead
rather than cold-disk latency). Numbers wobble up to ~2x with
container load; the shape holds.

## Known caveats

- **Wayland**: the hotkey and cursor layers speak X11 (pure-Go, via
  XWayland when available). On a Wayland-only session without an
  XWayland `DISPLAY` there is no global hotkey (the failure is logged
  once and the app keeps running) and the cursor position cannot be
  read, so the bar centers on the current monitor instead of following
  the cursor. Under XWayland the hotkey works for X11 clients, but
  some compositors do not forward keys grabbed this way from native
  Wayland windows.
- **Linux HiDPI**: with a GDK scale factor > 1 the X11 pixel
  coordinates and GTK's logical coordinates disagree, which can offset
  the bar's position on scaled multi-monitor setups.
- **macOS**: positioning uses a best-effort native Cocoa move of the
  app's first window (Wails' WindowSetPosition is relative to the
  window's current screen and cannot target another display); it falls
  back to centering. The global hotkey needs the app to be trusted
  under System Settings > Privacy & Security > Accessibility.
  CI builds linux/amd64 only, so the macOS code is never compiled or
  tested in CI (a cgo Cocoa target cannot be cross-compiled from the
  Linux runner); treat it as best-effort until exercised on a real Mac.
- **Windows**: hotkey via RegisterHotKey and monitors via user32; the
  bar positions against the current monitor's work area. Like macOS,
  the Windows code is never compiled or tested in CI (linux/amd64
  only).
- **Watch limits / event overflow**: every live indexed directory
  holds one fsnotify watch (inotify on Linux), so very large trees can
  exhaust `fs.inotify.max_user_watches`. Degradation is graceful and
  never fatal: a refused watch is counted, logged once, and skipped
  (that directory simply stops receiving live updates), and a kernel
  event-queue overflow (lost events) automatically requests a full
  reconcile rescan (requests are coalesced and spaced >= 30s apart, so
  an overflow storm cannot cause back-to-back walks). Either condition
  raises the staleness warning chip in the UI. If it happens
  routinely, raise the limit (e.g.
  `sudo sysctl fs.inotify.max_user_watches=524288`) and/or set
  `rescanIntervalMinutes` as a periodic safety net.
- **Reveal on Linux**: prefers the freedesktop `FileManager1` D-Bus
  interface and falls back to opening the parent directory with
  xdg-open when `dbus-send` is missing; a dbus-send that starts but
  finds no file manager is not detected (launches are fire-and-forget).
