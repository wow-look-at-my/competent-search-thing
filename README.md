# competent-search-thing

A cross-platform desktop searchbar: press a global hotkey, a small
frameless bar pops up on the display your cursor is on, and every
keystroke instantly filters an in-memory index of your file names --
Spotlight-style presentation with voidtools-Everything-style speed.
Built with Go and [Wails v2](https://wails.io), with a tiny vanilla
TypeScript + Vite frontend.

## Status

Feature-complete for v1 (docs and release polish pending):

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

Exclude patterns without a path separator are matched against each
entry's base name (`node_modules`, `*.tmp`); matching directories are
pruned, matching files skipped. Patterns containing a separator are
matched against the full path (`/home/*/secret`); `*` never crosses a
separator and there is no `**`. The same exclude semantics apply to
the initial walk, to live filesystem events, and to rescans.
`rescanIntervalMinutes` sets an optional periodic full re-index (a
safety net on top of the live fsnotify updates; also triggered
automatically when the watcher degrades, e.g. on inotify watch-limit
or event-queue overflow); `0` disables the periodic timer. `hotkey`
is the global summon shortcut: "+"-separated, case- and
whitespace-insensitive; modifiers `ctrl`/`control`, `shift`,
`alt`/`option`, `super`/`win`/`cmd`/`meta`; key `space`, `tab`,
`enter`/`return`, `esc`/`escape`, `a`-`z`, `0`-`9`, `f1`-`f12`, or an
arrow (`up`/`down`/`left`/`right`). An invalid or unregistrable hotkey
is logged and the app runs on without one.

## Wails v2 vs v3

This project uses **Wails v2** (latest stable, v2.13.0 at scaffold
time). Wails v3 was still in alpha as of 2026-07 (v3.0.0-alpha2.114,
daily alpha releases); v1 of this app wants a stable, documented base,
so v2 it is. Revisit once v3 ships a stable release.

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
  Compile-checked only on macOS -- CI builds linux/amd64 only, so the
  macOS paths are untested in CI.
- **Windows**: hotkey via RegisterHotKey and monitors via user32; the
  bar positions against the current monitor's work area. Like macOS,
  Windows code is compile-checked only on Windows and untested in CI
  (linux/amd64 only).
- **Reveal on Linux**: prefers the freedesktop `FileManager1` D-Bus
  interface and falls back to opening the parent directory with
  xdg-open when `dbus-send` is missing; a dbus-send that starts but
  finds no file manager is not detected (launches are fire-and-forget).
