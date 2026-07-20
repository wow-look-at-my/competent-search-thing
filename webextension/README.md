# competent-search-thing tab switch (Firefox companion extension)

Lets the searchbar SWITCH to an open Firefox tab you pick instead of
opening a duplicate. Without it, tab picks fall back to opening the
URL.

## Install

- **Try it now**: `about:debugging` > This Firefox > Load Temporary
  Add-on > pick `manifest.json` here. Lasts until Firefox restarts.
- **Durable**: `web-ext sign --channel unlisted` with free
  [AMO API keys](https://addons.mozilla.org/developers/addon/api/key/),
  then install the produced `.xpi` (release Firefox requires signing).
- Nothing else to set up: the app installs the native-messaging host
  manifest and wrapper automatically at startup.

## How it works

The persistent background page holds one native-messaging port to
`competent-search-thing firefox-host` (spawned by Firefox), which
relays to the running app over a local socket. The app asks for the
tab list on each summon; picking a tab row runs
`tabs.update` + `windows.update` inside Firefox.

## Files

- `manifest.json` -- MV2, pinned id, permissions
  `nativeMessaging` + `tabs` only.
- `logic.mjs` -- all logic, pure and importable; tested by
  `frontend/src/ffext-logic.test.ts` (vitest) and pinned against the
  Go side by `internal/ffext/sync_test.go`.
- `background.js` / `background.html` -- the thin entry.
