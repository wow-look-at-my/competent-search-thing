# competent-search-thing

A cross-platform desktop searchbar: press a global hotkey, a small
frameless bar pops up on the display your cursor is on, and every
keystroke instantly filters an in-memory index of your file names --
Spotlight-style presentation with voidtools-Everything-style speed.
Built with Go and [Wails v2](https://wails.io), with a tiny vanilla
TypeScript + Vite frontend.

## Status

Work in progress. This is the project scaffold: the window shell, the
bound API stubs, the frontend skeleton, and CI. The index engine,
filesystem watchers, platform layer (hotkey/open/reveal), and the real
UI land in later phases.

## Planned v1 features

- Global hotkey (default Alt+Space) to summon/dismiss the bar
- Frameless, always-on-top searchbar positioned on the display the
  cursor is currently on
- Instant substring search over indexed file and directory names
- Enter opens the selected entry, Ctrl+Enter reveals it in the file
  manager
- Live index updates via fsnotify
- Optional periodic full rescan as a safety net

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

## Configuration (planned)

Config will live at the platform config dir:

- Linux: `~/.config/competent-search-thing/config.json`
- macOS: `~/Library/Application Support/competent-search-thing/config.json`
- Windows: `%APPDATA%\competent-search-thing\config.json`

Planned format:

```json
{
  "roots": ["~"],
  "exclude": ["node_modules", ".git", ".cache"],
  "rescanMinutes": 30,
  "maxResults": 100
}
```

## Wails v2 vs v3

This project uses **Wails v2** (latest stable, v2.13.0 at scaffold
time). Wails v3 was still in alpha as of 2026-07 (v3.0.0-alpha2.114,
daily alpha releases); v1 of this app wants a stable, documented base,
so v2 it is. Revisit once v3 ships a stable release.

## Known caveats

- **Wayland**: global hotkeys and explicit window positioning are
  restricted under Wayland; the hotkey layer will use X11 APIs
  (works under XWayland for many compositors, but not guaranteed) and
  cursor-display positioning may fall back to the compositor's default
  placement.
- **macOS**: CI runs Linux only; macOS builds are expected to work with
  Wails v2 but are untested in CI.
- **Windows**: also untested in CI for now.
