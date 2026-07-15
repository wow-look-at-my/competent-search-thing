# competent-search-thing

A cross-platform desktop searchbar: press a global hotkey, a small
frameless bar pops up on the display your cursor is on, and every
keystroke instantly filters an in-memory index of your file names --
Spotlight-style presentation with voidtools-Everything-style speed.
An async [plugin system](#plugins) adds virtual results below the file
rows -- type `=2+2` and a calculator card answers, `#ff8800` previews a
color, `!ps fire` lists matching running apps -- without ever slowing
the file search down. Built with Go and [Wails v2](https://wails.io),
with a tiny vanilla TypeScript + Vite frontend.

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
- [x] Plugin system: async virtual results from external command/HTTP
      plugins (file search never waits on them), bang targeting and
      completion (`!calc 2+2`; a bare `!` lists every command),
      opt-in app-context awareness (focused/running/installed apps),
      built-in commands (`!rescan`, `!reload`, `!config`, `!version`,
      `!quit`, `!app`) and three documented example plugins

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
  "maxResults": 50,
  "plugins": { "disabled": false, "entries": {} },
  "bangs": { "sigils": ["!", "/", "@"], "aliases": {} }
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
- `plugins` -- the [plugin system](#plugins). `disabled` (default
  `false`) turns the whole system off, built-in providers included.
  `entries` maps a provider id to per-plugin config:
  `{ "entries": { "calc": { "disabled": false, "settings": { } } } }`.
  `disabled` turns that one provider off (the built-in ids `bangs`,
  `app` and `apps` work here too); `settings` is an opaque JSON object
  passed verbatim to that plugin in every request (its `settings`
  field), so plugins can be configured without editing their manifest.
- `bangs` -- bang parsing. `sigils` lists the characters that may start
  a bang query (default `["!", "/", "@"]`; each must be exactly one
  character and not a letter, digit, or space -- invalid sigils are
  logged and skipped, and an empty/all-invalid list falls back to the
  defaults). `aliases` maps extra names onto registered bangs, e.g.
  `{ "aliases": { "math": "calc" } }` makes `!math` target the plugin
  that registered `calc`.

## Plugins

The bar can show **virtual results** -- a calculator answer, a color
swatch, anything a small external program computes -- in sections below
the file results. Plugins are asynchronous by design: file search stays
instant and never waits on a plugin; each plugin's section appears
under the file rows whenever its answer arrives, and a slow or broken
plugin simply contributes nothing.

A plugin is a directory containing a `manifest.json` that tells the app
what the plugin reacts to and how to reach it, over one of two
transports:

- **command** -- the app runs your program once per query: the request
  JSON arrives on stdin (which is then closed), the response JSON is
  read from stdout, and the process exits. No shell is involved -- the
  manifest's `argv` is executed directly.
- **http** -- the app POSTs the request JSON
  (`Content-Type: application/json`) to a URL you configure and reads
  the response JSON from a 2xx reply.

### Installing a plugin

Copy the plugin's directory into `plugins/` inside the config
directory (next to `config.json`, see [Configuration](#configuration)),
so the manifest sits at `<config dir>/plugins/<name>/manifest.json`:

- Linux: `~/.config/competent-search-thing/plugins/`
- macOS: `~/Library/Application Support/competent-search-thing/plugins/`
- Windows: `%APPDATA%\competent-search-thing\plugins\`

If `COMPETENT_SEARCH_CONFIG_DIR` is set it replaces the config
directory; the plugin directory is always `<config dir>/plugins/`.

Plugins load at startup; run the built-in `!reload` command to pick up
new or edited plugins without restarting. A missing `plugins/`
directory is fine (you just have no plugins). A broken manifest is
skipped and logged -- all plugin problems land in the app's log on
standard error with a `plugin:` prefix (visible when the app is
launched from a terminal; a desktop session usually routes it to the
journal). When two manifests declare the same id, the
alphabetically-first directory wins and the duplicate is logged.

### Writing a plugin: the 60-second version

```
mkdir -p ~/.config/competent-search-thing/plugins/hello
cd ~/.config/competent-search-thing/plugins/hello
```

`manifest.json`:

```json
{
  "id": "hello",
  "type": "command",
  "trigger": { "prefix": "hi " },
  "command": { "argv": ["python3", "hello.py"] }
}
```

`hello.py`:

```python
import json, sys

req = json.load(sys.stdin)
who = req["stripped"] or "world"
json.dump({
    "v": 1,
    "results": [{
        "title": "Hello, " + who,
        "subtitle": "from your first plugin",
        "icon": "star",
        "action": {"type": "copy_text", "value": who},
    }],
}, sys.stdout)
```

Summon the bar, run `!reload`, then type `hi there`. A "Hello, there"
card appears below the file results; Enter copies "there" to the
clipboard. Because `bangs` defaults to the plugin id, `!hello there`
works too, bypassing the prefix trigger.

### The manifest

A complete command manifest (the shipped calc example) and a fuller
HTTP one showing the optional knobs:

```json
{
  "v": 1,
  "id": "calc",
  "name": "Calculator",
  "type": "command",
  "trigger": { "prefix": "=" },
  "bangs": ["calc", "c"],
  "timeout_ms": 1500,
  "command": { "argv": ["python3", "calc.py"] }
}
```

```json
{
  "v": 1,
  "id": "tickets",
  "name": "Ticket lookup",
  "type": "http",
  "trigger": {
    "regex": "^[a-z]{2,5}-[0-9]+$",
    "min_query_len": 4,
    "debounce_ms": 150,
    "focused_app": { "name_regex": "firefox|chrome" },
    "focused_boost": 20
  },
  "bangs": ["ticket", "t"],
  "context": ["focused"],
  "timeout_ms": 3000,
  "http": {
    "url": "http://127.0.0.1:9800/query",
    "headers": { "X-Api-Key": "swordfish" }
  },
  "allow_run_command": false
}
```

Top-level fields:

| field | type | default | rules |
|-------|------|---------|-------|
| `v` | int | 1 | manifest version; must be 1 |
| `id` | string | (required) | `^[a-z0-9][a-z0-9_-]{0,31}$`; unique -- the built-in ids `bangs`, `app`, `apps` are taken |
| `name` | string | the id | display name, shown as the section header and in the bang chip |
| `type` | string | (required) | `"command"` or `"http"` |
| `trigger` | object | none | when the plugin sees untargeted queries (table below); omit it to make the plugin bang-only |
| `bangs` | string[] | `[<id>]` | bang names targeting this plugin; same syntax as `id`, lowercased and deduped. An explicit `[]` means no bangs (then `trigger` is required -- a plugin with neither is rejected as unreachable) |
| `context` | string[] | `[]` | app-context parts sent with every request: any of `"focused"`, `"running"`, `"installed"`. Undeclared parts are never sent |
| `timeout_ms` | int | 1500 | per-query time budget, clamped to 100..10000 |
| `command` | object | -- | required for `type:"command"`: `{ "argv": [...] }` with at least one entry, none empty |
| `http` | object | -- | required for `type:"http"`: `{ "url": "...", "headers": {...} }` |
| `allow_run_command` | bool | `false` | must be `true` for this plugin's results to carry `run_command` actions; otherwise any result with one is dropped |

`trigger` fields (a plugin matches when ANY of the text paths --
`prefix`, `regex`, `all_queries`, tried in that order, first match
decides the stripped query -- matches AND the `focused_app` gate, when
present, matches):

| field | type | default | meaning |
|-------|------|---------|---------|
| `prefix` | string | "" | case-insensitive prefix match on the typed query; the remainder, trimmed, becomes the request's `stripped` |
| `regex` | string | "" | case-insensitive RE2 matched against the RAW query; on match `stripped` is the trimmed raw query |
| `all_queries` | bool | `false` | match every query |
| `min_query_len` | int | 0 | minimum `stripped` length in runes, gating ALL paths; when 0 and `all_queries` is set, the effective minimum is 2 (so an all-queries plugin does not fire on single keystrokes) |
| `debounce_ms` | int | 0 | extra delay before dispatch, clamped to 0..2000; a newer keystroke cancels the wait, so a debounced plugin only sees queries the user paused on |
| `focused_app` | object | none | `{ "name_regex": "...", "exe_regex": "..." }` -- the trigger only matches when the app focused at hotkey press matches (case-insensitive RE2; at least one pattern required, an empty one is a wildcard). When no focused app is known (Wayland, degraded platforms) the gate never matches |
| `focused_boost` | int | 0 | 0..100, added to every result score (clamped at 100) when the focused gate matches -- lets app-specific plugins outrank generic ones |

`command.argv` resolution: an absolute `argv[0]` runs as-is; one
containing a path separator resolves relative to the manifest's
directory; a bare name goes through the normal `PATH` lookup. The
working directory is always the manifest's directory, which is why
`["python3", "calc.py"]` just works.

`http.url` must be an absolute `http`/`https` URL with a host.
`headers` are set on every request (e.g. an API key). Redirects are
followed at most 3 hops and only to `http`/`https` targets.

### The wire protocol

One request per query. Command plugins read it from stdin; HTTP
plugins receive it as the POST body:

```json
{
  "v": 1,
  "query": "!calc 2+2",
  "stripped": "2+2",
  "gen": 42,
  "targeted": true,
  "bang": "calc",
  "settings": {},
  "context": {
    "focused_app": { "name": "firefox", "exe": "/usr/lib/firefox/firefox", "title": "Mozilla Firefox", "pid": 1234 },
    "running_apps": [ { "name": "kitty", "exe": "/usr/bin/kitty", "title": "~/src", "pid": 4321 } ],
    "installed_apps": [ { "name": "Firefox", "exec": "firefox %u", "id": "firefox.desktop" } ]
  }
}
```

- `v` -- protocol version, always 1. Reject anything else.
- `query` -- the raw text as typed.
- `stripped` -- the query with the trigger prefix or bang removed and
  trimmed; usually what you want to parse.
- `gen` -- monotonically increasing query generation. Purely
  informational for one-shot plugins.
- `targeted` / `bang` -- set when the query was bang-dispatched
  (`!calc 2+2`); `bang` is the canonical bang name used.
- `settings` -- this plugin's `settings` object from `config.json`,
  always at least `{}`.
- `context` -- only the parts declared in the manifest's `context`,
  and only when data is available; parts with nothing to report are
  omitted, and the whole field is absent when nothing remains. Privacy
  note: a plugin that declares nothing never sees any of it.

What the context parts contain, per platform:

| part | Linux/X11 | Linux/Wayland | Windows | macOS |
|------|-----------|---------------|---------|-------|
| `focused_app` | yes | absent | best-effort | best-effort (`title` always empty) |
| `running_apps` | yes | X11/XWayland clients only, else absent | best-effort | best-effort (`title` always empty) |
| `installed_apps` | `.desktop` entries | `.desktop` entries | uninstall-registry entries | `/Applications` scan |

The focused app is captured **at hotkey press, before the bar window
takes focus**, so it is the app the user was actually using. The
running list refreshes in the background at each summon; the installed
list refreshes at startup and then at most every 5 minutes at summon --
requests never block on any of it. The Windows and macOS paths compile
but are not exercised by CI (linux/amd64); treat them as best-effort.

The response, on stdout (command) or as the 2xx body (http):

```json
{
  "v": 1,
  "results": [
    {
      "title": "4",
      "subtitle": "2 + 2",
      "icon": "calculator",
      "badge": "CALC",
      "accent_color": "#a6e3a1",
      "score": 100,
      "fields": [
        { "label": "Hex", "value": "0x4" },
        { "label": "Binary", "value": "0b100" }
      ],
      "action": { "type": "copy_text", "value": "4" }
    }
  ]
}
```

A missing `"v"` means 1; any other value rejects the whole response.
`{"v":1,"results":[]}` is the correct "nothing to show" answer (it
renders nothing and is not an error).

### Results: fields, caps, styling

Everything a plugin returns is validated and clamped before it can
reach the UI. Oversized strings are truncated, invalid values cleared,
and anything dropped is logged with a reason.

| field | required | limit | notes |
|-------|----------|-------|-------|
| `title` | yes | 200 runes | trimmed; a result with an empty title is dropped |
| `subtitle` | no | 300 runes | dim second line |
| `icon` | no | see below | built-in icon name, or a literal glyph/emoji up to 32 bytes |
| `badge` | no | 24 runes | small accent-colored tag on the row's right edge |
| `accent_color` | no | `#rgb` / `#rrggbb` | must match `^#([0-9a-fA-F]{3}\|[0-9a-fA-F]{6})$`; anything else is cleared |
| `score` | no | 0..100 | default 50 when absent; clamped |
| `fields` | no | 8 fields; label 40 / value 200 runes | rendered as dim `label: value` pairs under the title |
| `action` | no | -- | what Enter/click does; see [Actions](#actions) |

Response-wide caps: at most 20 results per response and 1 MiB of
response body (both transports); control characters in any string are
replaced with spaces.

**Ordering**: file results always come first. Plugin sections sort by
their best result's score (then plugin id); results within a section
sort by score (then response order). A score of 100 puts your section
above other plugins' sections.

**Icons**: the built-in names are `calculator`, `globe`, `clock`,
`star`, `info`, `warning`, `link`, `terminal`, `text`, `hash`, `bolt`,
`app`, and `puzzle`. An unknown or absent name falls back to the
puzzle piece. A value that is not a lowercase name is rendered
literally, so a plugin may ship its own emoji (up to 32 bytes) as the
icon. Remote icon URLs are not supported.

**Styling**: `accent_color` is the only styling channel a plugin has.
It sets exactly one CSS custom property, `--plugin-accent`, on that
row; the stylesheet consumes it as
`var(--plugin-accent, var(--accent, #89b4fa))` (row left edge and
badge), so themes can define `--accent` as the app-wide fallback.
Plugins cannot inject HTML, CSS, or inline styles -- every string is
rendered as a text node.

### Actions

A result's `action` decides what Enter (or a click) does. Rows without
an action are inert display rows.

| type | payload | validation | on activation |
|------|---------|------------|---------------|
| `open_path` | `value` | non-empty absolute path, <= 2048 bytes | opens with the OS default handler; the bar hides |
| `open_url` | `value` | `http`/`https` URL with a host, <= 2048 bytes | opens in the browser; the bar hides |
| `copy_text` | `value` | non-empty, <= 8192 bytes | copies to the clipboard; the bar STAYS OPEN and flashes "Copied" |
| `run_command` | `argv` | 1..16 entries, each non-empty and <= 1024 bytes | launches the argv detached (no shell); the bar hides |

`run_command` is additionally gated by the manifest: unless it sets
`"allow_run_command": true`, any result carrying a `run_command`
action is dropped entirely (and logged). Because the manifest lives on
the user's disk, a plugin response can never grant itself local
execution.

Two more action types exist -- `set_query` (replace the search input)
and `run_builtin` (app commands) -- but they are **internal-only**,
produced by the built-in providers; the sanitizer strips them from
external plugin responses. Every action is re-validated in Go when it
is executed, so a malformed action is rejected, never run.

### Bangs

Bangs target a query at one specific plugin, bypassing every trigger
condition (prefix, regex, `all_queries`, `min_query_len`, the focused
gate). The default sigils are `!`, `/` and `@` -- all equivalent --
and are configurable (`bangs.sigils` in `config.json`).

- `!calc 2+2` -- sigil + bang name + a space + the rest. Only the
  plugin that registered `calc` is dispatched, with `targeted: true`,
  `bang: "calc"` and `stripped: "2+2"`. File search still runs on the
  raw text, and a chip in the query row names the targeted plugin.
- Resolution order: exact bang match, then a configured alias, then --
  when exactly one registered bang starts with what you typed -- that
  unique prefix (`!ca 2+2` resolves to `calc`).
- A bare sigil (`!`), a partial or ambiguous name (`!ca`), or a
  resolved name still missing its space: the built-in Commands
  provider suggests matching bangs (up to 12) as results; Enter on a
  suggestion completes the input in place, keeping your sigil and
  whatever followed the name.
- Sigil text that matches no bang at all falls through to the normal
  trigger path as a plain query.

A manifest with `bangs` but no `trigger` is a **bang-only plugin**: it
never sees untargeted queries at all (the shipped `ps` example). Note
that bang names come from plugin manifests, so installing a plugin is
also trusting its bang names; built-ins register first and can never
be shadowed.

### Built-in commands

Three built-in providers ship inside the app and go through the same
pipeline (disable them like any plugin via `plugins.entries` with ids
`bangs`, `app`, `apps`):

| bang | does |
|------|------|
| `!rescan` | rebuild the file index from disk now (errors while the initial build is still running) |
| `!reload` | re-read `config.json` and the plugin manifests, restart providers |
| `!config` | open `config.json` with the OS default handler |
| `!version` | copy the app version to the clipboard |
| `!quit` | exit the app |
| `!app <text>` / `!launch <text>` | search installed applications and launch the selection |

Type a bare `!` (or `/` or `@`) to list every available command.

`!app` searches the installed-apps snapshot: an empty query (`!app `,
note the space) lists the first 15 alphabetically; otherwise name
prefix matches score 100 and substring matches 80, capped at 15.
Selecting a row launches the app via its parsed `.desktop` `Exec`
line (freedesktop field codes like `%u` stripped), detached from the
searchbar. This is `.desktop`-based and therefore Linux-first; Windows
and macOS enumeration is best-effort. Installed apps currently appear
only when targeted -- surfacing them as untargeted results for plain
queries is a possible future config knob.

### Trust model

Plain words about what installing a plugin means:

- **Command plugins run programs with your privileges.** The app
  executes the manifest's `argv` as you, once per matching keystroke
  batch. Only install plugins whose code you trust (or have read --
  the shipped examples are a few dozen lines each).
- **HTTP plugins call endpoints you configured.** The app only ever
  POSTs query JSON to the manifest's URL and renders the reply; your
  query text (and any declared context) is sent to that endpoint, so
  point it only at services you trust.
- **Responses are data, never capability.** A response cannot execute
  anything by itself: `run_command` results are dropped unless the
  on-disk manifest opts in via `allow_run_command`; the internal
  `set_query`/`run_builtin` action types are stripped from external
  responses; `open_url` is restricted to `http`/`https`; every action
  is re-validated at execution time.
- **No markup, no remote fetches.** Plugins cannot inject HTML or CSS;
  all text renders as text nodes; icons are built-in names or literal
  glyphs, never URLs; styling is limited to the whitelisted knobs
  (icon, badge, accent color, fields). Control characters are stripped
  from every string, including clipboard payloads.
- **Context is opt-in per plugin.** The focused/running/installed app
  lists are only sent to plugins that declare them in their manifest;
  everything else never leaves the app.

### The shipped examples

Three documented, tested examples live in
[`examples/plugins/`](examples/plugins/) (each has its own README with
install steps):

| plugin | transport | trigger | bangs | context | demonstrates |
|--------|-----------|---------|-------|---------|--------------|
| `calc` | command (python3) | prefix `=` | `!calc`, `!c` | -- | arithmetic evaluation, Hex/Binary fields, `copy_text`, accent + badge |
| `color-http` | http (Go server) | prefix `#` | `!color` | -- | the HTTP transport end to end, `accent_color` as the parsed color, R/G/B + H/S/L fields |
| `ps` | command (python3) | none (bang-only) | `!ps` | `running` | bang-only targeting and the `context` declaration |

`calc` and `ps` need `python3` on `PATH` (on Windows change the argv
to `python`). `color-http`'s endpoint runs with
`go run ./examples/plugins/color-http/server`; its handler is a good
HTTP-plugin reference: POST-only (405 otherwise), 400 for a malformed
body -- the recommended answers, since the searchbar logs any non-2xx
as a plugin error.

### Limits and troubleshooting

- **Timeouts kill.** A command plugin still running at `timeout_ms` is
  hard-killed (the kill waits at most ~250ms extra for the process to
  let go of its pipes); an HTTP request is aborted. The same happens
  the moment a newer keystroke supersedes the query.
- **Logs are throttled.** Plugin errors and sanitizer drops are logged
  at most once per 5 seconds per plugin, so a broken plugin cannot
  flood the log. Logs go to the app's standard error with a `plugin:`
  prefix.
- **Common failures**: response is not valid JSON; `"v"` is neither
  absent nor 1; response body over 1 MiB (the command transport keeps
  draining stdout so the child still exits); a command exiting
  non-zero (its stderr, up to 8 KiB, is captured and quoted in the log
  line); an HTTP status outside 2xx.
- **Isolation**: a failing plugin never affects file search or other
  plugins -- its section just does not appear. A panic-level failure
  inside the dispatch pipeline is recovered and logged.

Not in v1, flagged as future work: HTTP GET mode (POST is the single
HTTP mode), a persistent JSON-Lines command mode (one process per
query is the only command mode), remote icons, plugin-supplied
HTML/CSS, Wayland focused-window support, and untargeted installed-app
results.

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
