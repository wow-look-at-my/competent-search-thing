# internal/firefox

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/firefox` -- the Firefox data layer (frequent sites + open
tabs), pure and headless-tested (fixture profiles.ini trees, fixture
places.sqlite databases AND fixture recovery.jsonlz4 snapshots BUILT
IN THE TESTS -- the latter via a test-only literals-only mozLz4
compressor; injectable now/clock/fetch/mtime seams).
profiles.go: `BaseDirs(goos, home, getenv)` = the probe order
(linux: classic ~/.mozilla/firefox, snap
~/snap/firefox/common/.mozilla/firefox -- Ubuntu 22.04's default --
flatpak ~/.var/app/org.mozilla.firefox/.mozilla/firefox; windows
%APPDATA%\Mozilla\Firefox; darwin best-effort) +
`FindProfile(bases)`: per base, profiles.ini resolves to ONE
profile ([Install*] Default= wins, then [ProfileN] Default=1, then
a lone [ProfileN]; IsRelative=1 joins against the base, missing
IsRelative inferred from the path; the resolved dir must exist);
multiple bases = newest places.sqlite mtime wins, earlier base
breaks ties; ok=false = caller degrades. places.go:
`FrequentSites(ctx, profileDir, QueryOptions{MinMonth, MinWeek,
Now, Limit})` NEVER opens the live db (Firefox holds it locked,
WAL): copies places.sqlite + places.sqlite-wal to a fresh temp dir
(chunked, ctx-abortable), opens the COPY read-only via pure-Go
modernc.org/sqlite (driver "sqlite" -- windows/amd64 must keep
cross-compiling, so never swap in a cgo driver), one grouped query
(visit_date is MICROSECONDS since epoch; hidden=0, http(s)-only,
visit_type NOT IN (4,8) i.e. EMBED/FRAMED_LINK excluded; HAVING
c30 >= MinMonth AND c7 >= MinWeek, ORDER BY c30 DESC, LIMIT
default 200), host parsed in Go (net/url Hostname; empty host =
row dropped), temp dir removed on the way out. cache.go: `Cache`
(NewCache(ctx, CacheOptions)) = the appctx.Cache pattern for
sites: `Sites()` returns an immutable copy immediately and
single-flight-kicks ONE background refresh when stale (success
schedules the next attempt a TTL away, default 10m; failure keeps
old data, logs once per DISTINCT message, retries no sooner than
1m so a broken profile is not re-copied per keystroke); every
refresh goroutine is bounded by the constructor ctx (cancelled =
no new kicks, in-flight fetch aborts quietly). mozlz4.go:
`DecodeMozLz4(data, maxSize)` -- Firefox's .jsonlz4 container
(8-byte "mozLz40\0" magic + LE uint32 uncompressed size + raw LZ4
BLOCK format, no frame) with a hand-written ~80-line block decoder:
token nibbles, 0xFF-chained length extensions, byte-by-byte FORWARD
match copies (offset < length self-replication is legal), every
read bounds-checked, the block must produce EXACTLY the declared
size, declared sizes over the cap (default 64 MiB) rejected as
corruption. sessionstore.go: `ReadOpenTabs(profileDir)` reads
sessionstore-backups/recovery.jsonlz4 (rewritten ~15s by a RUNNING
Firefox; private windows never persisted) -> []Tab{URL, Title,
Host, Pinned, LastAccessed(ms), FavIconURL (the tab's "image"
attribute -- the favicon URL SessionStore records -- passed through
VERBATIM, consumers validate; absent = "")}: hidden tabs skipped,
entries[index-1]
is the current page (1-based index clamped into range, entry-less
tabs skipped), http(s)-with-host only, raw cap 500; a MISSING file
= (nil, nil) -- browser closed, deliberately NO
sessionstore.jsonlz4 fallback (those tabs are not open) -- while
corrupt/unreadable files are errors; `RecoveryMTime` = the cheap
staleness probe. tabcache.go: `TabCache` (NewTabCache(ctx,
TabCacheOptions)) = the Cache pattern with an mtime gate: `Tabs()`
serves the snapshot immediately; a due probe (>= 1s apart) stats
the file and re-reads ONLY when the mtime changed or the last read
is older than the TTL (default 15s, matching Firefox's write
cadence -- no config knob); success MAY legitimately store an
empty list (closed browser), failure keeps old data with the same
once-per-distinct-message logging and 1m retry gap, ctx bounds
every goroutine. favicons.go: `FaviconReader`
(NewFaviconReader(ctx, FaviconOptions{ProfileDir, TTL default 10m,
Logf; unexported mtime/now seams)) = the favicons.sqlite reader
behind the icon service's offline favicon tier, zero IO at
construction: `Lookup(pageURL, sizePx) (data, iconURL)` answers
from a PRIVATE snapshot (the places.go rule -- never the live db:
copy + -wal to a temp dir, opened read-only via the pure-Go
driver), the first Lookup paying the copy and later ones re-copying
ONLY when the source mtime changed AND the copy outlived the TTL
(probes spaced 1s, failures logged once per distinct message +
retried no sooner than 1m, everything degrades to "nothing known"
-- never an error); the exact-page query (moz_pages_w_icons x
moz_icons_to_pages x moz_icons; the *_hash columns are
Firefox-internal SQL functions, deliberately unused) runs first,
then the domain-root fallback (root=1 favicon.ico candidates for
the page's host incl. the www-toggle -- what serves
host-aggregated frequent-site rows); best-fit width = smallest >=
wanted else largest below (SVG's 65535 sentinel width needs no
special case), payloads over MaxFaviconBytes (1 MiB) or empty are
skipped but still surface their icon_url as the caller's fetch
hint; the first successful snapshot arms ONE ctx-bounded cleanup
goroutine (handle close + temp-dir removal when the app-lifetime
firefox ctx cancels). Consumed by
internal/app's firefox.go + the plugin registry's firefox-frequent
and firefox-tabs builtins -- where the open-tabs getter now serves
the internal/ffext live bridge snapshot FIRST (connected + fresh
within the same 15s bound) and this sessionstore layer is the
always-there fallback -- and (favicons.go) internal/app's icons.go.
