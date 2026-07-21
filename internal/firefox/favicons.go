package firefox

import (
	"context"
	"database/sql"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// faviconsFile is the favicon database's file name inside a profile
// (beside places.sqlite; Firefox 55+).
const faviconsFile = "favicons.sqlite"

// DefaultFaviconTTL bounds how stale the favicon snapshot may get
// before a changed source file triggers a re-copy. Favicons churn
// slowly (only NEW pages add rows the resolver could want), so this is
// deliberately much longer than the tab cadence -- the icon service
// caches every resolved icon anyway.
const DefaultFaviconTTL = 10 * time.Minute

// faviconProbeGap spaces the cheap source-mtime stats, the tabProbeGap
// twin.
const faviconProbeGap = time.Second

// MaxFaviconBytes caps one stored favicon payload; larger rows are
// skipped as if absent (the internal/icons MaxFileBytes precedent --
// row icons are small, anything bigger is junk).
const MaxFaviconBytes = 1 << 20

// svgIconWidth is the moz_icons.width sentinel Firefox stores for SVG
// payloads (UINT16_MAX: an SVG covers every size). It needs no special
// case -- the best-fit rule below naturally treats it as the largest
// available width -- but the name keeps the fixture tests honest.
const svgIconWidth = 65535

// faviconPageQuery finds every icon linked to one exact page URL.
// moz_pages_w_icons/moz_icons_to_pages/moz_icons is the Firefox 55+
// favicon schema; the *_hash columns are computed by a
// Firefox-internal SQL function and deliberately unused here (the
// tables are small enough that the equality scan is cheap on a
// per-batch cadence).
const faviconPageQuery = `
SELECT i.width, i.data, i.icon_url
FROM moz_icons AS i
JOIN moz_icons_to_pages AS ip ON ip.icon_id = i.id
JOIN moz_pages_w_icons AS p ON p.id = ip.page_id
WHERE p.page_url = ?`

// faviconRootQuery finds domain-root icons ("favicon.ico" at the
// host's root, flagged root=1 -- usable for ANY page on that host) by
// their exact icon URL candidates, computed in Go.
const faviconRootQuery = `
SELECT width, data, icon_url
FROM moz_icons
WHERE root = 1 AND icon_url IN (?, ?, ?, ?)`

// FaviconOptions configures NewFaviconReader.
type FaviconOptions struct {
	// ProfileDir is the profile directory holding favicons.sqlite.
	ProfileDir string
	// TTL is the minimum age of the snapshot before a changed source
	// mtime triggers a re-copy (non-positive = DefaultFaviconTTL).
	TTL time.Duration
	// Logf receives refresh failures (default log.Printf via the cache
	// convention: nil = a silent logger is NOT substituted here, the
	// zero value falls back to a no-op); repeats of the same message
	// are logged once.
	Logf func(format string, args ...any)

	// mtime and now are test seams: nil means a stat of ProfileDir's
	// favicons.sqlite and time.Now.
	mtime func() time.Time
	now   func() time.Time
}

// FaviconReader answers page-URL -> stored-favicon lookups from a
// PRIVATE SNAPSHOT of the profile's favicons.sqlite -- the live
// database is never opened (Firefox holds it locked, WAL; the
// places.go rule). The snapshot is copied ONCE and re-copied only when
// the source mtime changed AND the copy is older than the TTL, so a
// burst of lookups (one ResolveIcons batch) shares one copy and one
// open handle. Construction does zero IO; the first Lookup pays the
// copy (bounded by the constructor context -- the app's Shutdown
// cancels it, which also closes the handle and removes the temp
// directory). Goroutine-safe; a nil reader answers nothing.
type FaviconReader struct {
	ctx   context.Context
	dir   string
	ttl   time.Duration
	logf  func(format string, args ...any)
	mtime func() time.Time
	now   func() time.Time

	mu        sync.Mutex
	db        *sql.DB
	tmp       string
	lastMTime time.Time
	lastCopy  time.Time
	nextProbe time.Time
	lastErr   string // last logged failure (dedup)

	cleanupOnce sync.Once
}

// NewFaviconReader builds a reader over opt. ctx bounds the snapshot
// copies and, once a snapshot exists, its cleanup (nil = never
// cancelled, resources live until process exit).
func NewFaviconReader(ctx context.Context, opt FaviconOptions) *FaviconReader {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.TTL <= 0 {
		opt.TTL = DefaultFaviconTTL
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	if opt.mtime == nil {
		src := filepath.Join(opt.ProfileDir, faviconsFile)
		opt.mtime = func() time.Time {
			fi, err := os.Stat(src)
			if err != nil {
				return time.Time{}
			}
			return fi.ModTime()
		}
	}
	return &FaviconReader{
		ctx:   ctx,
		dir:   opt.ProfileDir,
		ttl:   opt.TTL,
		logf:  opt.Logf,
		mtime: opt.mtime,
		now:   opt.now,
	}
}

// Lookup resolves one page URL to its best stored favicon for a wanted
// pixel size: icons linked to the exact page first, then the
// domain-root icon (root=1) matching the page's host -- the fallback
// that serves host-aggregated frequent-site rows. Among the stored
// widths the smallest >= sizePx wins, else the largest below (the icns
// selection rule; SVG rows, stored width 65535, naturally scale).
// data carries the raw stored payload (PNG/ICO/SVG -- the caller
// sniffs); iconURL is a known favicon URL worth fetching when the
// database knows the icon but holds no usable payload. Both empty =
// nothing known. Never returns an error: every failure degrades to
// "nothing known" with once-per-distinct-message logging.
func (r *FaviconReader) Lookup(pageURL string, sizePx int) (data []byte, iconURL string) {
	if r == nil {
		return nil, ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ensureSnapshotLocked() {
		return nil, ""
	}
	if data, iconURL = r.queryLocked(faviconPageQuery, sizePx, pageURL); len(data) > 0 {
		return data, iconURL
	}
	rootData, rootURL := r.rootLookupLocked(pageURL, sizePx)
	if len(rootData) > 0 {
		return rootData, rootURL
	}
	// No payload anywhere: prefer the page-linked icon URL as the
	// fetch hint, then the root one.
	if iconURL == "" {
		iconURL = rootURL
	}
	return nil, iconURL
}

// rootLookupLocked runs the domain-root fallback for pageURL's host.
func (r *FaviconReader) rootLookupLocked(pageURL string, sizePx int) ([]byte, string) {
	u, err := neturl.Parse(pageURL)
	if err != nil {
		return nil, ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, ""
	}
	// Firefox stores root icons under the exact host; try the page's
	// spelling plus the www-toggled twin (host aggregation upstream may
	// have picked either).
	alt := "www." + host
	if strings.HasPrefix(host, "www.") {
		alt = strings.TrimPrefix(host, "www.")
	}
	args := []any{
		"https://" + host + "/favicon.ico",
		"http://" + host + "/favicon.ico",
		"https://" + alt + "/favicon.ico",
		"http://" + alt + "/favicon.ico",
	}
	return r.queryLocked(faviconRootQuery, sizePx, args...)
}

// queryLocked runs one candidate query and picks the best-fit row.
// Callers hold r.mu (the open handle may be swapped by a refresh).
func (r *FaviconReader) queryLocked(query string, sizePx int, args ...any) (data []byte, iconURL string) {
	ctx, cancel := context.WithTimeout(r.ctx, 2*time.Second)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		r.logOnceLocked(fmt.Sprintf("firefox: favicon query: %v", err))
		return nil, ""
	}
	defer rows.Close()
	bestWidth := -1
	for rows.Next() {
		var (
			width int
			blob  []byte
			url   string
		)
		if err := rows.Scan(&width, &blob, &url); err != nil {
			r.logOnceLocked(fmt.Sprintf("firefox: favicon rows: %v", err))
			return nil, ""
		}
		if iconURL == "" && url != "" {
			iconURL = url // any known URL beats none (the fetch hint)
		}
		if len(blob) == 0 || len(blob) > MaxFaviconBytes {
			continue
		}
		if !betterWidth(bestWidth, width, sizePx) {
			continue
		}
		bestWidth = width
		data = blob
		if url != "" {
			iconURL = url
		}
	}
	if err := rows.Err(); err != nil {
		r.logOnceLocked(fmt.Sprintf("firefox: favicon rows: %v", err))
		return nil, iconURL
	}
	return data, iconURL
}

// betterWidth reports whether candidate width beats the current best
// for a wanted size under the smallest->=want-else-largest-below rule.
// best -1 = nothing yet.
func betterWidth(best, cand, want int) bool {
	if best < 0 {
		return true
	}
	bestCovers, candCovers := best >= want, cand >= want
	switch {
	case candCovers && !bestCovers:
		return true
	case !candCovers && bestCovers:
		return false
	case candCovers: // both cover: smaller wins
		return cand < best
	default: // neither covers: larger wins
		return cand > best
	}
}

// ensureSnapshotLocked opens (or refreshes) the private snapshot.
// false = no snapshot available (missing database, failed copy);
// callers answer "nothing known". Callers hold r.mu.
func (r *FaviconReader) ensureSnapshotLocked() bool {
	now := r.now()
	if r.db != nil {
		if now.Before(r.nextProbe) {
			return true
		}
		r.nextProbe = now.Add(faviconProbeGap)
		mt := r.mtime()
		if mt.Equal(r.lastMTime) || now.Sub(r.lastCopy) < r.ttl {
			return true // unchanged, or changed but inside the TTL
		}
		if r.openSnapshotLocked() {
			return true
		}
		// The refresh failed; keep serving the previous snapshot.
		return r.db != nil
	}
	if now.Before(r.nextProbe) {
		return false // a recent attempt failed; do not hammer
	}
	r.nextProbe = now.Add(failureRetryGap)
	return r.openSnapshotLocked()
}

// openSnapshotLocked copies the database (+ -wal) into a fresh temp
// dir and swaps the open handle over to it, closing and removing the
// previous snapshot. Callers hold r.mu.
func (r *FaviconReader) openSnapshotLocked() bool {
	src := filepath.Join(r.dir, faviconsFile)
	mt := r.mtime()
	if _, err := os.Stat(src); err != nil {
		r.logOnceLocked(fmt.Sprintf("firefox: favicons: %v", err))
		return false
	}
	tmp, err := os.MkdirTemp("", "competent-search-favicons-")
	if err != nil {
		r.logOnceLocked(fmt.Sprintf("firefox: favicons temp dir: %v", err))
		return false
	}
	dst := filepath.Join(tmp, faviconsFile)
	if err := copyFile(r.ctx, src, dst); err != nil {
		_ = os.RemoveAll(tmp)
		r.logOnceLocked(fmt.Sprintf("firefox: copying %s: %v", faviconsFile, err))
		return false
	}
	if _, err := os.Stat(src + "-wal"); err == nil {
		if err := copyFile(r.ctx, src+"-wal", dst+"-wal"); err != nil {
			_ = os.RemoveAll(tmp)
			r.logOnceLocked(fmt.Sprintf("firefox: copying %s-wal: %v", faviconsFile, err))
			return false
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst)+"?mode=ro")
	if err != nil {
		_ = os.RemoveAll(tmp)
		r.logOnceLocked(fmt.Sprintf("firefox: opening the favicon copy: %v", err))
		return false
	}
	r.closeSnapshotLocked()
	r.db, r.tmp = db, tmp
	r.lastMTime = mt
	r.lastCopy = r.now()
	r.nextProbe = r.now().Add(faviconProbeGap)
	r.lastErr = ""
	// The first successful snapshot arms the one cleanup goroutine:
	// when the app-lifetime context ends, the handle closes and the
	// temp directory goes away.
	r.cleanupOnce.Do(func() {
		go func() {
			<-r.ctx.Done()
			r.mu.Lock()
			r.closeSnapshotLocked()
			r.mu.Unlock()
		}()
	})
	return true
}

// closeSnapshotLocked releases the current snapshot, if any. Callers
// hold r.mu.
func (r *FaviconReader) closeSnapshotLocked() {
	if r.db != nil {
		_ = r.db.Close()
		r.db = nil
	}
	if r.tmp != "" {
		_ = os.RemoveAll(r.tmp)
		r.tmp = ""
	}
}

// logOnceLocked logs msg unless it repeats the previous failure (the
// cache.go dedup convention). Shutdown-cancelled copies stay silent.
func (r *FaviconReader) logOnceLocked(msg string) {
	if r.ctx.Err() != nil {
		return
	}
	if msg == r.lastErr {
		return
	}
	r.lastErr = msg
	r.logf("%s", msg)
}
