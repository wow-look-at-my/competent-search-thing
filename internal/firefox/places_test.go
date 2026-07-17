package firefox

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedNow anchors every window in these tests.
var fixedNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// visit is one moz_historyvisits fixture row.
type visit struct {
	at  time.Time
	typ int // visit_type; 0 means 1 (TRANSITION_LINK)
}

// page is one moz_places fixture row plus its visits.
type page struct {
	url    string
	title  any // string or nil (moz_places.title is nullable)
	hidden int
	visits []visit
}

// visitsAt fans out n copies of the same timestamp.
func visitsAt(at time.Time, n int) []visit {
	out := make([]visit, n)
	for i := range out {
		out[i] = visit{at: at}
	}
	return out
}

// openFixture creates dir/places.sqlite with just the schema columns
// the query touches and returns the open handle (single connection,
// so WAL fixtures behave deterministically).
func openFixture(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, placesFile)))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
CREATE TABLE moz_places (
  id INTEGER PRIMARY KEY,
  url TEXT,
  title TEXT,
  hidden INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE moz_historyvisits (
  id INTEGER PRIMARY KEY,
  place_id INTEGER,
  visit_date INTEGER,
  visit_type INTEGER NOT NULL DEFAULT 1
);`)
	require.NoError(t, err)
	return db
}

// insertPages writes the fixture rows (one transaction: hundreds of
// separate commits would fsync the test to a crawl).
func insertPages(t *testing.T, db *sql.DB, pages []page) {
	t.Helper()
	tx, err := db.Begin()
	require.NoError(t, err)
	for i, p := range pages {
		id := i + 1
		_, err := tx.Exec(`INSERT INTO moz_places (id, url, title, hidden) VALUES (?, ?, ?, ?)`,
			id, p.url, p.title, p.hidden)
		require.NoError(t, err)
		for _, v := range p.visits {
			typ := v.typ
			if typ == 0 {
				typ = 1
			}
			_, err := tx.Exec(`INSERT INTO moz_historyvisits (place_id, visit_date, visit_type) VALUES (?, ?, ?)`,
				id, v.at.UnixMicro(), typ)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tx.Commit())
}

// buildPlaces creates a plain (non-WAL) fixture database and closes
// it so all data lives in the main file.
func buildPlaces(t *testing.T, dir string, pages []page) {
	t.Helper()
	db := openFixture(t, dir)
	insertPages(t, db, pages)
	require.NoError(t, db.Close())
}

func TestFrequentSitesThresholdsAndFilters(t *testing.T) {
	within7 := fixedNow.Add(-2 * day)
	within30 := fixedNow.Add(-20 * day)
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		// Exactly the owner's rule: >10 in 30 days (>=11) AND >=1 in 7.
		{url: "https://in.example/", title: "In", visits: append(visitsAt(within30, 10), visit{at: within7})},
		{url: "https://often.example/", title: "Often", visits: append(visitsAt(within30, 29), visit{at: within7})},
		{url: "https://ten.example/", title: "Only ten", visits: append(visitsAt(within30, 9), visit{at: within7})},
		{url: "https://lapsed.example/", title: "Not this week", visits: visitsAt(within30, 15)},
		// 8 + 3 user-visible visits plus one EMBED (4) and one
		// FRAMED_LINK (8); the two frame transitions must not count, so
		// c30 is 11 -- barely in -- and the count excludes them.
		{url: "https://embeds.example/", title: "Mostly embeds", visits: append(append(
			visitsAt(within30, 8), visit{at: within7, typ: 4}, visit{at: within7, typ: 8}),
			visitsAt(within7, 3)...)},
		// Qualifies ONLY if frame transitions counted: 9 + 1 real visits
		// plus 5 EMBEDs would be 15; filtered it is 10, which is out.
		{url: "https://framedout.example/", title: "Framed out", visits: append(append(
			visitsAt(within30, 9), visitsAt(within7, 1)...),
			visit{at: within7, typ: 4}, visit{at: within7, typ: 4}, visit{at: within7, typ: 4},
			visit{at: within7, typ: 4}, visit{at: within7, typ: 4})},
		{url: "https://hidden.example/", title: "Hidden", hidden: 1, visits: visitsAt(within7, 20)},
		{url: "ftp://files.example/", title: "FTP", visits: visitsAt(within7, 20)},
		{url: "place:sort=8&maxResults=10", title: "Bookmark query", visits: visitsAt(within7, 20)},
		{url: "https:///nohost", title: "No host", visits: visitsAt(within7, 20)},
		{url: "https://untitled.example/", title: nil, visits: visitsAt(within7, 12)},
	})

	sites, err := FrequentSites(context.Background(), dir, QueryOptions{
		MinMonth: 11, MinWeek: 1, Now: fixedNow,
	})
	require.NoError(t, err)

	byHost := map[string]Site{}
	for _, s := range sites {
		byHost[s.Host] = s
	}
	require.Contains(t, byHost, "in.example", "11 visits in 30d, one this week")
	require.Equal(t, 11, byHost["in.example"].Visits)
	require.Equal(t, "In", byHost["in.example"].Title)
	require.Contains(t, byHost, "often.example")
	require.Equal(t, 30, byHost["often.example"].Visits)
	require.NotContains(t, byHost, "ten.example", "10 visits is not more than 10")
	require.NotContains(t, byHost, "lapsed.example", "no visit in the past 7 days")
	require.Contains(t, byHost, "embeds.example", "EMBED/FRAMED_LINK visits do not count")
	require.Equal(t, 11, byHost["embeds.example"].Visits, "the two type-4/8 visits are excluded from the count")
	require.NotContains(t, byHost, "framedout.example", "frame transitions cannot push a page over the bar")
	require.NotContains(t, byHost, "hidden.example", "hidden=1 rows are redirect noise")
	require.NotContains(t, byHost, "files.example", "non-http(s) schemes are out")
	require.NotContains(t, byHost, "", "hostless URLs are dropped in Go")
	require.Contains(t, byHost, "untitled.example")
	require.Equal(t, "", byHost["untitled.example"].Title, "NULL titles scan as empty")

	// Sanity: nothing unexpected slipped in.
	for host := range byHost {
		require.NotContains(t, []string{"ten.example", "lapsed.example", "hidden.example", "files.example"}, host)
	}
}

func TestFrequentSitesWindowBoundaries(t *testing.T) {
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		// 10 visits exactly at the 30-day edge + 1 exactly at the 7-day
		// edge: both count (>= cutoff), so the page qualifies.
		{url: "https://edge.example/", title: "Edge", visits: append(
			visitsAt(fixedNow.Add(-30*day), 10), visit{at: fixedNow.Add(-7 * day)})},
		// 10 visits one microsecond OUTSIDE the 30-day window plus one
		// recent: c30 is 1, so it does not qualify.
		{url: "https://outside.example/", title: "Outside", visits: append(
			visitsAt(fixedNow.Add(-30*day).Add(-time.Microsecond), 10), visit{at: fixedNow.Add(-time.Hour)})},
		// 11 in the month but the newest is one microsecond older than
		// 7 days: c7 is 0.
		{url: "https://almost.example/", title: "Almost", visits: append(
			visitsAt(fixedNow.Add(-20*day), 10), visit{at: fixedNow.Add(-7 * day).Add(-time.Microsecond)})},
	})

	sites, err := FrequentSites(context.Background(), dir, QueryOptions{
		MinMonth: 11, MinWeek: 1, Now: fixedNow,
	})
	require.NoError(t, err)
	require.Len(t, sites, 1)
	require.Equal(t, "edge.example", sites[0].Host)
	require.Equal(t, 11, sites[0].Visits)
}

func TestFrequentSitesOrderAndLimit(t *testing.T) {
	within7 := fixedNow.Add(-day)
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		{url: "https://bronze.example/", visits: visitsAt(within7, 12)},
		{url: "https://gold.example/", visits: visitsAt(within7, 40)},
		{url: "https://silver.example/", visits: visitsAt(within7, 25)},
		// Same count as bronze: the URL tiebreak keeps output stable.
		{url: "https://aaa.example/", visits: visitsAt(within7, 12)},
	})

	sites, err := FrequentSites(context.Background(), dir, QueryOptions{
		MinMonth: 11, MinWeek: 1, Now: fixedNow,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gold.example", "silver.example", "aaa.example", "bronze.example"},
		hosts(sites), "most-visited first, URL breaks ties")

	top2, err := FrequentSites(context.Background(), dir, QueryOptions{
		MinMonth: 11, MinWeek: 1, Now: fixedNow, Limit: 2,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"gold.example", "silver.example"}, hosts(top2))
}

func hosts(sites []Site) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.Host
	}
	return out
}

// TestFrequentSitesCopiesWAL proves the copy-then-query path works on
// a live WAL database: with checkpointing off and the writing
// connection still open, every row lives in places.sqlite-wal only,
// so the query can only see them if the sidecar was copied too.
func TestFrequentSitesCopiesWAL(t *testing.T) {
	dir := t.TempDir()
	db := openFixture(t, dir)
	var mode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode))
	require.Equal(t, "wal", mode)
	_, err := db.Exec(`PRAGMA wal_autocheckpoint=0`)
	require.NoError(t, err)
	insertPages(t, db, []page{
		{url: "https://live.example/", title: "Live", visits: visitsAt(fixedNow.Add(-day), 12)},
	})
	// The db stays OPEN (Firefox running); the WAL sidecar must exist.
	_, err = os.Stat(filepath.Join(dir, placesFile+"-wal"))
	require.NoError(t, err, "the fixture must actually be mid-WAL")

	sites, err := FrequentSites(context.Background(), dir, QueryOptions{
		MinMonth: 11, MinWeek: 1, Now: fixedNow,
	})
	require.NoError(t, err)
	require.Len(t, sites, 1)
	require.Equal(t, "live.example", sites[0].Host)
	require.Equal(t, 12, sites[0].Visits)
	require.NoError(t, db.Close())
}

func TestFrequentSitesSourceUntouchedAndTempCleaned(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch) // os.MkdirTemp("" ...) lands here on unix
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		{url: "https://a.example/", visits: visitsAt(fixedNow.Add(-day), 12)},
	})
	before, err := os.ReadFile(filepath.Join(dir, placesFile))
	require.NoError(t, err)

	_, err = FrequentSites(context.Background(), dir, QueryOptions{MinMonth: 1, MinWeek: 1, Now: fixedNow})
	require.NoError(t, err)

	after, err := os.ReadFile(filepath.Join(dir, placesFile))
	require.NoError(t, err)
	require.Equal(t, before, after, "the profile's database is never written")
	left, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, left, "the temp copy is removed")
}

func TestFrequentSitesDefaults(t *testing.T) {
	dir := t.TempDir()
	// 201 qualifying pages: the default limit caps at 200.
	pages := make([]page, 0, 201)
	for i := 0; i < 201; i++ {
		pages = append(pages, page{
			url:    fmt.Sprintf("https://h%03d.example/", i),
			visits: visitsAt(time.Now().Add(-time.Hour), 2),
		})
	}
	buildPlaces(t, dir, pages)
	// Zero Now/Limit take time.Now and DefaultLimit; MinMonth/MinWeek
	// pass through verbatim.
	sites, err := FrequentSites(context.Background(), dir, QueryOptions{MinMonth: 1, MinWeek: 1})
	require.NoError(t, err)
	require.Len(t, sites, DefaultLimit)
}

func TestFrequentSitesMissingDatabase(t *testing.T) {
	_, err := FrequentSites(context.Background(), t.TempDir(), QueryOptions{MinMonth: 1, MinWeek: 1})
	require.Error(t, err)
	require.ErrorContains(t, err, "firefox")
}

func TestFrequentSitesCorruptDatabase(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, placesFile), []byte("garbage, not sqlite"), 0o644))
	_, err := FrequentSites(context.Background(), dir, QueryOptions{MinMonth: 1, MinWeek: 1})
	require.Error(t, err)
}

func TestFrequentSitesCancelledContext(t *testing.T) {
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		{url: "https://a.example/", visits: visitsAt(fixedNow.Add(-day), 12)},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FrequentSites(ctx, dir, QueryOptions{MinMonth: 1, MinWeek: 1, Now: fixedNow})
	require.ErrorIs(t, err, context.Canceled)
}

func TestHostOf(t *testing.T) {
	require.Equal(t, "example.com", hostOf("https://example.com/path?q=1"))
	require.Equal(t, "example.com", hostOf("http://user@example.com:8080/x"), "ports and userinfo are stripped")
	require.Equal(t, "", hostOf("https:///nohost"))
	require.Equal(t, "", hostOf("://not a url"))
}
