# Vendored file-icons assets: provenance and licenses

Pack: file-icons/atom (the user-chosen icon pack), pinned commit
`28520868aee66e576145a0b18aa2cde5444a897a`. The package itself is MIT
(LICENSE.md, "Copyright (c) 2014-2016 Daniel Brooker, (c) 2016-2025
John Gardner"); its mapping data (config.cson) and stylesheets
(styles/*.less) are covered by that grant. The GLYPHS live in fonts
with their own licenses, verified per font from the actual source
repos on 2026-07-20 (devopicons re-verified 2026-07-21):

| font file | source @ pin | license | shipped? |
|---|---|---|---|
| fonts/file-icons.woff2 | file-icons/atom (built from file-icons/icons @ e6e6e6ac) | ISC ("Copyright (c) 2016-2021, John Gardner") | yes |
| fonts/fontawesome.woff2 | FontAwesome 4.7.0 (FortAwesome/Font-Awesome @ v4.7.0, a3fe90f) | Font: SIL OFL 1.1 (README.md L12-13: "The Font Awesome font is licensed under the SIL OFL 1.1") | yes |
| fonts/mfixx.woff2 | file-icons/MFixx @ aabb5bad | MIT ("Copyright (c) 2013-2015 Fizzed, Inc., (c) 2016-2025 John Gardner") | yes |
| fonts/octicons.woff2 | primer/octicons @ v4.4.0 (62c67273) | MIT ("Copyright (c) 2012-2016 GitHub, Inc.") | yes |
| fonts/devopicons.woff2 | file-icons/DevOpicons @ 2c2bf2bd (master HEAD; dist/DevOpicons.woff2 -- byte-identical, sha256 8124aa3adefdacaa5a06d5af0f9779ddbce616dea7ffc904ac806c3a2e22aa78, to the copy the pack bundles as fonts/devopicons.woff2) | see "The Devicons face" below: no license file of its own; shipped on the user's explicit decision as a derivative of the MIT vorillaz/devicons | yes |

## The Devicons face (fonts/devopicons.woff2)

History: this face was EXCLUDED from the original vendoring (PR #65
dropped all 159 rules using it) because file-icons/DevOpicons states
no license of its own. It is now shipped; the receipts, in full:

1. The repo state, re-verified 2026-07-21 at the pinned commit
   `2c2bf2bdb6507b8e4bfe695c1d54d639fbfed479` (which is still the
   current master HEAD): no LICENSE file, no license field, no README
   grant, and the font's own name table carries no license metadata
   (nameID 13/14 absent; the description reads only "Font generated
   by IcoMoon."). The one "MIT License" string in the repo
   (charmap.md) names a GLYPH depicting the MIT logo, not a grant.
2. The user's explicit decision to include it, quoted: "please
   include it" and "dont overcomplicate it just use
   https://github.com/file-icons/DevOpicons and dont reinvent the
   wheel".
3. The derivation chain: file-icons/DevOpicons describes itself
   (README.md) as "simply a heavily-optimised version of the
   Devicons font, created by Theodore Vorillas. All credit for the
   original icon vectors goes to him and his project's
   contributors". That upstream -- vorillaz/devicons -- IS licensed:
   MIT, verified at its pinned default-branch HEAD
   `592f9162379d2f3726729cd2199be7f67765aa25` (LICENSE, quoted
   verbatim below), corroborated by the project's legal page at
   https://devicons.io/legal/:

   > MIT License
   >
   > Permission is hereby granted, free of charge, to any person
   > obtaining a copy of this software and associated documentation
   > files (the "Software"), to deal in the Software without
   > restriction, including without limitation the rights to use,
   > copy, modify, merge, publish, distribute, sublicense, and/or
   > sell copies of the Software, and to permit persons to whom the
   > Software is furnished to do so, subject to the following
   > conditions:
   >
   > The above copyright notice and this permission notice shall be
   > included in all copies or substantial portions of the Software.
   >
   > THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
   > EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES
   > OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
   > NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
   > HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
   > WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
   > FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
   > OTHER DEALINGS IN THE SOFTWARE.

4. Trademark caveat (applies to this face exactly like the pack's
   other brand glyphs), from https://devicons.io/legal/, quoted: "The
   icons and logos in this collection depict logos and brand marks
   owned by their respective companies. Devicons does not claim
   ownership of these marks. Including an icon in this package does
   not grant you a trademark license from the brand owner."

Notes:

- "Octicons Regular" is referenced by file-icons/atom's icons.less
  but NOT bundled there (Atom itself shipped it); we vendor it from
  primer/octicons at v4.4.0, the last release distributing the icon
  font with the \f0xx codepoints icons.less uses. The same checkout's
  lib/font/codepoints.json also resolves the Atom-builtin
  "icon-<name>" classes (icon-file-pdf, icon-file-text, icon-mail,
  icon-circuit-board, icon-star, icon-paintcan) config.cson
  references -- Atom shipped those CSS classes itself, so the pack's
  icons.less never declares them.
- The fonts are binary assets and exempt from the repo's ASCII-only
  rule exactly like the committed PNGs.
- Attribution: this file plus the upstream grant texts quoted above;
  FontAwesome 4.x explicitly requires no attribution ("Attribution is
  no longer required as of Font Awesome 3.0").
