# internal/icons

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/icons` -- result-row icon resolution to data URIs behind
the app's bound ResolveIcons, pure and headless-tested (every input
dir and external command sits behind Options seams). Key protocol
(the frontend wire contract): "dir", "file:<basename>",
"app:<ref>", "favicon:<pageURL>". NewService does NO IO; the first
Resolve pays
initialization (mime-db load + gsettings/settings.ini theme
detection), and everything is served through a positive + negative
(name|size)->URI LRU (512 entries each) under one mutex.
Linux/freedesktop half (#37): theme.go/lookup.go/mimedb.go -- the
detected GTK theme + Inherits chain + Adwaita/hicolor, exact size
match then closest, then unthemed/pixmap fallbacks; absolute
.png/.svg refs served directly; 1 MiB MaxFileBytes cap. Darwin half
(bundle.go + plist.go + icns.go): an "app:" ref that is an ABSOLUTE
path ending ".app" (case-insensitive -- the ref SHAPE selects the
branch, so fixture bundles test it on any OS) resolves
Contents/Info.plist -> CFBundleIconFile (".icns" appended when
extension-less; separators/".."/non-.icns rejected) ->
Contents/Resources/<file> -> icnsBestPNG. plist.go is a
hand-rolled bounded bplist00 reader (trailer + offset table + dict/
ASCII/UTF-16 strings/int extended counts ONLY -- the mozLz4
precedent, no plist dep; caps 65536 objects / 4096-rune strings)
plus an encoding/xml fallback matching the same root-dict-only
semantics (nested decoys never match). icns.go walks the container
(4CC + BE length entries) and passes through the best PNG-magic
payload -- smallest nominal size covering the want, else largest
below, else unknown-size band; NO image decoding ever -- skipping
legacy RLE/JPEG-2000 payloads and entries over 512KB
(maxIcnsEntryBytes; plist capped 4 MiB, icns container 32 MiB via
stat-first readCapped). When the pure plist/icns walk misses --
CFBundleIconName-only (Assets.car) apps (est. 5-15% of
/Applications, the Little Snitch field report), legacy-only icns
payloads, any structural miss -- the injectable Options.
NativeAppIcon seam (func(path string, sizePx int) []byte) is asked
BEFORE the miss is negative-cached: production wiring passes
native.AppIconPNG (NSWorkspace iconForFile rasterized to a PNG --
what Launchpad/Finder show, asset catalogs included; the !darwin
stub answers nil), a native hit lands in the positive cache like
any other, a nil answer negative-caches into the glyph (the honest
only-when-macOS-has-none fallback), and non-PNG/oversized seam
bytes are rejected (defense in depth). The pure path stays primary
(seam never consulted on a pure hit; bundle_test.go pins the whole
order headlessly) and a nil seam keeps pre-seam behavior
byte-identical. The darwin-only real_darwin_test.go runs un-gated
on the mac job: the pure-path tally (fails only when a POPULATED
/Applications resolves nothing), the acceptance sweep (with the
production seam wired EVERY /Applications bundle must resolve --
any miss is a bug), and the Assets.car-only pin (skipped when the
runner has no such app). WEBSITE FAVICONS (favicon.go, the
"favicon:<pageURL>" kind on the two builtin Firefox result
sources): resolution runs in Resolve's SECOND phase, OUTSIDE the
service mutex (a slow favicon can never stall app/file icon
batches), single-flighted per cache key, three tiers in order --
(1) the NoteFavicon hint side-channel (the browser-reported
favicon location per page: bounded LRU beside the caches,
validated to http(s) or data:image/* -- Firefox-internal schemes
like fake-favicon-uri: dropped -- capped 64 KiB, and a CHANGED
hint un-pins that page's negative-cached misses via
lru.deletePrefix so late-arriving hints self-heal; a data: hint
decodes and serves with ZERO IO), (2) the Options.FaviconLookup
seam (production = firefox.FaviconReader.Lookup over the profile's
favicons.sqlite snapshot; answers stored bytes or a known icon
URL), (3) ONE bounded GET of a KNOWN favicon URL only -- the
tier-1 hint or the tier-2 icon_url, never a guessed /favicon.ico
-- via an internal client (3s total timeout, 256 KiB body cap, 3
redirect hops http(s)-only; unexported favTransport/favTimeout/
favMaxFetch Options seams for tests). EVERY tier's payload is
SNIFFED by magic bytes (imageMIME: PNG/GIF/JPEG/WebP/ICO/BMP + SVG
by content; declared types never trusted, junk misses into the
glyph), successes land in the positive LRU, misses
negative-cache -- the builtin glyph stays the honest fallback.
Consumed by internal/app icons.go (the
newIcons seam).
