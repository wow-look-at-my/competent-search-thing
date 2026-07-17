package firefox

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"

	// The pure-Go SQLite driver (database/sql name "sqlite"). Chosen
	// over cgo bindings deliberately: CI cross-compiles windows/amd64
	// from the Linux runner, which auto-disables cgo.
	_ "modernc.org/sqlite"
)

// Site is one frequently-visited page from the Firefox history.
type Site struct {
	URL   string
	Title string
	// Host is the URL's hostname (no port), parsed in Go; rows whose
	// URL yields no host are dropped.
	Host string
	// Visits counts the user-visible visits in the past 30 days.
	Visits int
}

// DefaultLimit caps one history query's rows.
const DefaultLimit = 200

// copyBlock is the file-copy chunk size; the context is checked
// between chunks so shutdown aborts a large history copy promptly.
const copyBlock = 1 << 20

// day avoids repeating the 24h arithmetic on the window cutoffs.
const day = 24 * time.Hour

// QueryOptions parameterizes FrequentSites. The frequency rule is:
// include a page visited at least MinMonth times in the past 30 days
// AND at least MinWeek times in the past 7 days (non-positive
// minimums disable that bound; internal/config supplies the
// defaults).
type QueryOptions struct {
	MinMonth int
	MinWeek  int
	// Now anchors the 7/30-day windows (zero = time.Now).
	Now time.Time
	// Limit caps the returned rows (non-positive = DefaultLimit).
	Limit int
}

// frequentQuery counts each page's user-visible visits inside the
// 30-day window (c30) and its 7-day subset (c7). Schema facts the
// fixtures encode: moz_historyvisits.visit_date is MICROSECONDS since
// the Unix epoch; moz_places.hidden=1 marks redirect-source/framed
// pages; visit_type 4 (EMBED) and 8 (FRAMED_LINK) are transitions the
// user never saw as a page load. Only http(s) pages qualify.
// Parameters, in order: cut7us, cut30us, minMonth, minWeek, limit.
const frequentQuery = `
SELECT p.url, IFNULL(p.title, ''),
       COUNT(*) AS c30,
       SUM(CASE WHEN v.visit_date >= ? THEN 1 ELSE 0 END) AS c7
FROM moz_places AS p
JOIN moz_historyvisits AS v ON v.place_id = p.id
WHERE p.hidden = 0
  AND (p.url LIKE 'http://%' OR p.url LIKE 'https://%')
  AND v.visit_type NOT IN (4, 8)
  AND v.visit_date >= ?
GROUP BY p.id
HAVING c30 >= ? AND c7 >= ?
ORDER BY c30 DESC, p.url ASC
LIMIT ?`

// FrequentSites reads profileDir's places.sqlite and returns the
// frequently-visited pages, most-visited first. The live database is
// NEVER opened in place -- Firefox holds it locked (WAL mode) while
// running -- so the file and, when present, its -wal sidecar are
// copied into a fresh private temp directory, the copy is queried
// read-only, and the temp directory is removed (best effort) before
// returning.
func FrequentSites(ctx context.Context, profileDir string, opt QueryOptions) ([]Site, error) {
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}
	limit := opt.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	src := filepath.Join(profileDir, placesFile)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("firefox: %w", err)
	}
	tmp, err := os.MkdirTemp("", "competent-search-places-")
	if err != nil {
		return nil, fmt.Errorf("firefox: temp dir: %w", err)
	}
	defer os.RemoveAll(tmp) // best effort; the OS reaps temp leftovers

	dst := filepath.Join(tmp, placesFile)
	if err := copyFile(ctx, src, dst); err != nil {
		return nil, fmt.Errorf("firefox: copying %s: %w", placesFile, err)
	}
	// The write-ahead log holds every transaction not yet checkpointed
	// into the main file; while Firefox runs that is usually all of the
	// recent history, so the copy is useless without it.
	if _, err := os.Stat(src + "-wal"); err == nil {
		if err := copyFile(ctx, src+"-wal", dst+"-wal"); err != nil {
			return nil, fmt.Errorf("firefox: copying %s-wal: %w", placesFile, err)
		}
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst)+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("firefox: opening the history copy: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, frequentQuery,
		now.Add(-7*day).UnixMicro(), now.Add(-30*day).UnixMicro(),
		opt.MinMonth, opt.MinWeek, limit)
	if err != nil {
		return nil, fmt.Errorf("firefox: querying the history copy: %w", err)
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var (
			url, title string
			c30, c7    int
		)
		if err := rows.Scan(&url, &title, &c30, &c7); err != nil {
			return nil, fmt.Errorf("firefox: reading history rows: %w", err)
		}
		host := hostOf(url)
		if host == "" {
			continue
		}
		sites = append(sites, Site{URL: url, Title: title, Host: host, Visits: c30})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("firefox: reading history rows: %w", err)
	}
	return sites, nil
}

// hostOf parses url and returns its hostname ("" when unparseable or
// hostless).
func hostOf(url string) string {
	u, err := neturl.Parse(url)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// copyFile copies src to dst (created 0600) in copyBlock chunks,
// aborting between chunks when ctx is done.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, copyBlock)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}
