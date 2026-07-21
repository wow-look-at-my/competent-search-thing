# Vendored file-icons assets: provenance and licenses

Pack: file-icons/atom (the user-chosen icon pack), pinned commit
`28520868aee66e576145a0b18aa2cde5444a897a`. The package itself is MIT
(LICENSE.md, "Copyright (c) 2014-2016 Daniel Brooker, (c) 2016-2025
John Gardner"); its mapping data (config.cson) and stylesheets
(styles/*.less) are covered by that grant. The GLYPHS live in fonts
with their own licenses, verified per font from the actual source
repos on 2026-07-20:

| font file | source @ pin | license | shipped? |
|---|---|---|---|
| fonts/file-icons.woff2 | file-icons/atom (built from file-icons/icons @ e6e6e6ac) | ISC ("Copyright (c) 2016-2021, John Gardner") | yes |
| fonts/fontawesome.woff2 | FontAwesome 4.7.0 (FortAwesome/Font-Awesome @ v4.7.0, a3fe90f) | Font: SIL OFL 1.1 (README.md L12-13: "The Font Awesome font is licensed under the SIL OFL 1.1") | yes |
| fonts/mfixx.woff2 | file-icons/MFixx @ aabb5bad | MIT ("Copyright (c) 2013-2015 Fizzed, Inc., (c) 2016-2025 John Gardner") | yes |
| fonts/octicons.woff2 | primer/octicons @ v4.4.0 (62c67273) | MIT ("Copyright (c) 2012-2016 GitHub, Inc.") | yes |
| devopicons.woff2 | file-icons/DevOpicons @ 2c2bf2bd | NONE FOUND: no LICENSE file, no license field, no README grant (the repo optimizes vorillaz/devicons, itself MIT @ 592f9162, but the derivative we would embed states no license of its own) | NO -- excluded; every rule using the Devicons face is dropped at generation (159 rules) |

Notes:

- "Octicons Regular" is referenced by file-icons/atom's icons.less
  but NOT bundled there (Atom itself shipped it); we vendor it from
  primer/octicons at v4.4.0, the last release distributing the icon
  font with the \f0xx codepoints icons.less uses.
- The fonts are binary assets and exempt from the repo's ASCII-only
  rule exactly like the committed PNGs.
- Attribution: this file plus the upstream grant texts quoted above;
  FontAwesome 4.x explicitly requires no attribution ("Attribution is
  no longer required as of Font Awesome 3.0").
