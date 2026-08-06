# CI and the screenshot gate

Extracted from CLAUDE.md, which keeps a short index section pointing
here. Everything below is those two sections, unchanged.

## CI notes

- `.github/workflows/ci.yml` runs on every push (`on: push:`, no
  filters). Jobs: `linux` (the original single build job + cache
  hand-offs of the linux and windows app binaries), `darwin`
  (macos-latest: darwin/arm64 cgo build + the full unit-test suite,
  tags `desktop,production`, no deb; then
  `.github/scripts/darwin-smoke.ts` (typescript action, `file:`
  input) boots the built binary on the runner's real WindowServer
  session and asserts boot + JSON-shaped IPC round-trips within hard
  deadlines + the legacy-rejection check (a8-legacy-rejected: a bare
  v1 line must earn the JSON invalid-request error or a silent
  close, never the old raw "ok") + the settings-window gate
  (a9-config-window: {"cmd":"config"} must earn
  {"ok":true,"accepted":"config"} AND the spawned settings process
  must answer `version` on its OWN socket
  (COMPETENT_SEARCH_CONFIG_SOCKET, per-scenario) within
  CONFIG_WINDOW_MS -- the proof that the spawn really happens on a
  real desktop -- after which it is quit so teardown sees only the
  searchbar; sent hidden after a7 and clear of
  the toggle pair, then an explicit hide restores the hidden state)
  + the fps meter gate
  (a4-fps-meter: every scenario boots with COMPETENT_SEARCH_FPS=1;
  after a3 shows the bar, a parseable "fps: N avg, ..." summary AND
  the "fps: meter on; display NHz max, ..." context line -- the
  darwin power-probe cgo's first-run proof -- must land in the app
  log within 12s; the MECHANISM is the gate, absolute rates are
  informational on the AC-powered VM) + a real on-screen window via a compiled
  CGWindowList Swift probe, including while a big index build is
  PROVABLY in flight (the hard B-midindex-window check: progress
  line present, completion line absent, before b1 runs); scenario B
  then WAITS OUT the big build (B-index-done, 180s bound; the B hard
  cap is 360s to fit it) and pins the macOS watcher field fixes:
  B-backend (the "watch: backend ..." log line must name fsevents --
  auto-selection + honest label in one grep) and B-fd-headroom (lsof
  row count vs kern.maxfilesperproc, threshold min(5000, limit/2) --
  a regressed unbounded kqueue path sits AT the fd ceiling and fails
  loudly) -- the step
  is a HARD GATE (no continue-on-error): every SMOKE id is pass/fail
  and any FAIL fails the darwin job and with it all-builds;
  screenshots are best-effort "evidence:" captures (never a SMOKE
  id), copied to smoke-shots/ and uploaded via actions/
  upload-artifact@v4 as `darwin-smoke-<sha>` (`if-no-files-found:
  ignore` -- the linux screenshots pattern), and EVERY capture is
  ordered AFTER the focus/visibility-sensitive hard gates: the job's
  FIRST screencapture can pop the macOS 26 TCC screen-recording
  consent dialog, a key-stealing system modal whose focus loss
  blur-auto-hides the bar (correct product behavior) -- it starved
  a4's rAF accumulation and inverted a5's toggle into a re-show on
  run 29728632030, so scenario A's one shot (01-summoned-macos.png,
  the a6-reshown bar = the summoned state) lands after a6, where the
  remaining gates (a7, a9, all of B) are proven dialog-tolerant --
  including the
  translucentEvidence run between scenarios A and B: one extra boot
  with window.translucent=true (startApp's extraCfg param) captured
  as 03-translucent-macos.png, EVERY failure swallowed as an
  "evidence: ... unavailable" line, no SMOKE ids -- and the full
  app-log
  dumps print only on failure (green runs get the
  hotkey:/index:/watch:/panic summary lines); hands off the
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
  xvfb/xdotool/imagemagick/x11-utils/openbox -> `npm ci && npm run
  build && npm test` in `frontend/` (npm test = the vitest gate:
  DOM ordering + the webextension logic suite) -> `wow-look-at-my/go-toolchain@v1`
  with `targets: linux/amd64,windows/amd64`, `cgo: 'true'`,
  `timeout: '20'`, `autorelease: 'false'`, and env
  `GOFLAGS: "-tags=webkit2_41,desktop,production"` -> two
  `wow-look-at-my/actions@cache-upload#latest` hand-offs
  (`app-linux-amd64`, `app-windows-amd64` -- job hand-offs ride the
  org's cache trio, never GitHub artifacts) -> deb build + publish
  (next item) -> screenshot capture -> `actions/upload-artifact@v4`
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
  restored as the explicit job the same day), and BOTH build jobs pin
  `autorelease: 'false'` on their go-toolchain steps -- the action
  DEFAULTS it to 'true', so #25's input-line removal had silently
  re-enabled in-job publishing (extra app + server releases per push,
  fixed 2026-07-19). The old autorelease also
  pushed the color-http example server binary to project
  `competent-search-thing/server`; that is deliberately NOT restored
  (dev sample, not a user-facing deliverable). Deb publishing (previous
  item) is unchanged, and README "Install" documents the real install
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
  ~200-file fixture tree (incl. four "rep"-prefixed code files --
  repo.go/reply.ts/repack.json/repair.css -- so the 02/03 shots show
  varied per-file-type icons) + `config.json` in a temp dir
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
- `docs/screenshot-macos.png` is the committed macOS reference image
  used by README.md's macOS install section (the darwin GUI smoke's
  01-summoned capture, taken from the `darwin-smoke-<sha>` artifact).
  If the macOS UI changes deliberately, recapture and replace it.
