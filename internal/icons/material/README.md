# Vendored Material Icon Theme assets

File-type icons for the searchbar's file result rows (the icons
service's "file:<basename>" and "dir" keys -- see
internal/icons/fileicons.go).

- Upstream: https://github.com/material-extensions/vscode-material-icon-theme
- Pinned commit: `957d82b494e5737ef7b3c63e4d01f756d73a9936`
- License: MIT (the upstream `LICENSE` is committed verbatim in this
  directory). Several icons are redrawn third-party brand marks; the
  MIT license covers the artwork while the trademarks remain their
  owners', used here nominatively to identify file types exactly as
  the pack is used inside VS Code.

## Contents

- `svg/` -- the reachable subset of upstream `icons/` (mapping values,
  their `_light` variants, and the generated default `file.svg` +
  `folder.svg`), byte-identical to upstream except the two generated
  defaults. All files are pure ASCII, comment-free, script-free and
  free of external references (converter-verified; the only `href`s
  are same-document `#fragment` gradient refs).
- `mapping.json` -- the file-name/extension -> icon-name tables
  derived from upstream `src/core/icons/fileIcons.ts` at the same
  commit: `fileExtensions`, `fileNames` (lowercased keys, last entry
  wins, upstream's default "angular" icon pack active, clone entries
  and `.config/`-scoped names dropped -- see tools/convert.mjs),
  `light` (icon names whose `<name>_light.svg` renders on the light
  theme), and the `defaultFile`/`folder` icon names. Non-ASCII map
  keys (two upstream extensions) are `\u`-escaped so the file bytes
  stay ASCII. Like history.json/frecency.json this is an internal
  single-party format -- deliberately NO schema in `schemas/`.
- `tools/convert.mjs` -- the converter that generated both. It also
  regenerates the two default SVGs the way the upstream build does
  (path constants from fileGenerator.ts/folderGenerator.ts at the
  default color `#90a4ae`, opacity/saturation 1).

## Regenerating

    git clone https://github.com/material-extensions/vscode-material-icon-theme
    git -C vscode-material-icon-theme checkout <pinned-commit>
    node internal/icons/material/tools/convert.mjs \
        vscode-material-icon-theme internal/icons/material

Then update the pinned commit above and re-run the integrity tests
(internal/icons/fileicons_test.go gates: every mapping entry resolves
to an embedded SVG, light-flagged entries have their `_light.svg`,
pure ASCII, per-file and total byte caps).
