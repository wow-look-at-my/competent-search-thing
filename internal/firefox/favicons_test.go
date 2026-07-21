package firefox

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// favIcon is one moz_icons fixture row.
type favIcon struct {
	id      int
	iconURL string
	width   int
	root    int
	data    []byte
}

// favPage links one page URL to icon ids.
type favPage struct {
	id      int
	pageURL string
	iconIDs []int
}

// buildFavicons creates dir/favicons.sqlite with the Firefox 55+
// schema columns the reader's queries touch, closed so all data lives
// in the main file.
func buildFavicons(t *testing.T, dir string, icons []favIcon, pages []favPage) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, faviconsFile)))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE moz_icons (
  id INTEGER PRIMARY KEY,
  icon_url TEXT NOT NULL,
  fixed_icon_url_hash INTEGER NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 0,
  root INTEGER NOT NULL DEFAULT 0,
  color INTEGER,
  expire_ms INTEGER NOT NULL DEFAULT 0,
  data BLOB
);
CREATE TABLE moz_pages_w_icons (
  id INTEGER PRIMARY KEY,
  page_url TEXT NOT NULL,
  page_url_hash INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE moz_icons_to_pages (
  page_id INTEGER NOT NULL,
  icon_id INTEGER NOT NULL,
  expire_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (page_id, icon_id)
);`)
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	for _, ic := range icons {
		_, err := tx.Exec(`INSERT INTO moz_icons (id, icon_url, width, root, data) VALUES (?, ?, ?, ?, ?)`,
			ic.id, ic.iconURL, ic.width, ic.root, ic.data)
		require.NoError(t, err)
	}
	for _, p := range pages {
		_, err := tx.Exec(`INSERT INTO moz_pages_w_icons (id, page_url) VALUES (?, ?)`, p.id, p.pageURL)
		require.NoError(t, err)
		for _, icID := range p.iconIDs {
			_, err := tx.Exec(`INSERT INTO moz_icons_to_pages (page_id, icon_id) VALUES (?, ?)`, p.id, icID)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, db.Close())
}

// pngBytes fakes a payload of n bytes starting with the PNG magic.
func pngBytes(n int) []byte {
	b := make([]byte, n)
	copy(b, "\x89PNG\r\n\x1a\n")
	return b
}

func newTestFaviconReader(t *testing.T, dir string) *FaviconReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewFaviconReader(ctx, FaviconOptions{ProfileDir: dir, Logf: t.Logf})
}

func TestFaviconLookupExactPage(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{{id: 1, iconURL: "https://a.example/icon.png", width: 32, data: pngBytes(20)}},
		[]favPage{{id: 1, pageURL: "https://a.example/page", iconIDs: []int{1}}},
	)
	r := newTestFaviconReader(t, dir)
	data, iconURL := r.Lookup("https://a.example/page", 64)
	require.Equal(t, pngBytes(20), data)
	require.Equal(t, "https://a.example/icon.png", iconURL)

	// An unknown page with no root icon answers nothing at all.
	data, iconURL = r.Lookup("https://nowhere.example/", 64)
	require.Nil(t, data)
	require.Empty(t, iconURL)
}

func TestFaviconLookupRootFallback(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{
			{id: 1, iconURL: "https://root.example/favicon.ico", width: 16, root: 1, data: pngBytes(16)},
			{id: 2, iconURL: "https://www.dub.example/favicon.ico", width: 16, root: 1, data: pngBytes(17)},
		},
		nil, // no page rows: only the root fallback can answer
	)
	r := newTestFaviconReader(t, dir)

	data, _ := r.Lookup("https://root.example/deep/page?x=1", 32)
	require.Equal(t, pngBytes(16), data, "a page never linked in moz_pages_w_icons resolves through its host's root icon")

	data, _ = r.Lookup("https://dub.example/somewhere", 32)
	require.Equal(t, pngBytes(17), data, "the www-toggled root candidate covers hosts aggregated without the prefix")
}

func TestFaviconLookupWidthSelection(t *testing.T) {
	dir := t.TempDir()
	mk := func(w int) []byte {
		b := pngBytes(12)
		b[11] = byte(w) // distinguishable payloads
		return b
	}
	buildFavicons(t, dir,
		[]favIcon{
			{id: 1, iconURL: "https://w.example/16.png", width: 16, data: mk(16)},
			{id: 2, iconURL: "https://w.example/32.png", width: 32, data: mk(32)},
			{id: 3, iconURL: "https://w.example/128.png", width: 128, data: mk(128)},
		},
		[]favPage{{id: 1, pageURL: "https://w.example/", iconIDs: []int{1, 2, 3}}},
	)
	r := newTestFaviconReader(t, dir)

	data, _ := r.Lookup("https://w.example/", 32)
	require.Equal(t, mk(32), data, "exact width wins")
	data, _ = r.Lookup("https://w.example/", 33)
	require.Equal(t, mk(128), data, "smallest width covering the request wins")
	data, _ = r.Lookup("https://w.example/", 300)
	require.Equal(t, mk(128), data, "nothing covers: the largest below wins")
	data, _ = r.Lookup("https://w.example/", 8)
	require.Equal(t, mk(16), data, "the smallest covering width wins over larger ones")
}

func TestFaviconLookupSVGSentinelWidth(t *testing.T) {
	dir := t.TempDir()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	buildFavicons(t, dir,
		[]favIcon{
			{id: 1, iconURL: "https://s.example/16.png", width: 16, data: pngBytes(16)},
			{id: 2, iconURL: "https://s.example/icon.svg", width: svgIconWidth, data: svg},
		},
		[]favPage{{id: 1, pageURL: "https://s.example/", iconIDs: []int{1, 2}}},
	)
	r := newTestFaviconReader(t, dir)
	data, _ := r.Lookup("https://s.example/", 64)
	require.Equal(t, svg, data, "the SVG sentinel width covers any request larger than the raster rows")
}

func TestFaviconLookupSkipsOversizedAndEmptyPayloads(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{
			{id: 1, iconURL: "https://big.example/huge.png", width: 64, data: pngBytes(MaxFaviconBytes + 1)},
			{id: 2, iconURL: "https://big.example/empty.png", width: 32, data: nil},
			{id: 3, iconURL: "https://big.example/ok.png", width: 16, data: pngBytes(10)},
		},
		[]favPage{{id: 1, pageURL: "https://big.example/", iconIDs: []int{1, 2, 3}}},
	)
	r := newTestFaviconReader(t, dir)
	data, iconURL := r.Lookup("https://big.example/", 64)
	require.Equal(t, pngBytes(10), data, "oversized and empty payloads are skipped")
	require.NotEmpty(t, iconURL)
}

func TestFaviconLookupIconURLHintWithoutPayload(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{{id: 1, iconURL: "https://h.example/known.png", width: 32, data: nil}},
		[]favPage{{id: 1, pageURL: "https://h.example/", iconIDs: []int{1}}},
	)
	r := newTestFaviconReader(t, dir)
	data, iconURL := r.Lookup("https://h.example/", 64)
	require.Nil(t, data)
	require.Equal(t, "https://h.example/known.png", iconURL,
		"a payload-less row still surfaces its icon URL as the fetch hint")
}

func TestFaviconLookupMissingDatabaseDegrades(t *testing.T) {
	r := newTestFaviconReader(t, t.TempDir()) // no favicons.sqlite at all
	data, iconURL := r.Lookup("https://a.example/", 64)
	require.Nil(t, data)
	require.Empty(t, iconURL)
	// And again -- the retry gap keeps it quiet, no error surface.
	data, _ = r.Lookup("https://a.example/", 64)
	require.Nil(t, data)

	var nilReader *FaviconReader
	data, iconURL = nilReader.Lookup("https://a.example/", 64)
	require.Nil(t, data)
	require.Empty(t, iconURL)
}

func TestFaviconLookupCorruptDatabaseDegrades(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, faviconsFile), []byte("not sqlite"), 0o644))
	r := newTestFaviconReader(t, dir)
	data, iconURL := r.Lookup("https://a.example/", 64)
	require.Nil(t, data)
	require.Empty(t, iconURL)
}

func TestFaviconSnapshotIncludesWAL(t *testing.T) {
	dir := t.TempDir()
	// Build a WAL-mode database whose rows live in the -wal sidecar.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, faviconsFile)))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE moz_icons (id INTEGER PRIMARY KEY, icon_url TEXT NOT NULL, fixed_icon_url_hash INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0, root INTEGER NOT NULL DEFAULT 0, color INTEGER, expire_ms INTEGER NOT NULL DEFAULT 0, data BLOB);
CREATE TABLE moz_pages_w_icons (id INTEGER PRIMARY KEY, page_url TEXT NOT NULL, page_url_hash INTEGER NOT NULL DEFAULT 0);
CREATE TABLE moz_icons_to_pages (page_id INTEGER NOT NULL, icon_id INTEGER NOT NULL, expire_ms INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (page_id, icon_id));
PRAGMA wal_checkpoint(TRUNCATE);`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO moz_icons (id, icon_url, width, root, data) VALUES (1, 'https://wal.example/i.png', 32, 0, ?)`, pngBytes(9))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO moz_pages_w_icons (id, page_url) VALUES (1, 'https://wal.example/')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO moz_icons_to_pages (page_id, icon_id) VALUES (1, 1)`)
	require.NoError(t, err)
	// Deliberately keep the handle open (no checkpoint on close): the
	// inserts stay in the WAL, like a running Firefox.
	t.Cleanup(func() { db.Close() })
	if _, err := os.Stat(filepath.Join(dir, faviconsFile+"-wal")); err != nil {
		t.Skip("driver checkpointed the WAL; nothing to prove here")
	}

	r := newTestFaviconReader(t, dir)
	data, _ := r.Lookup("https://wal.example/", 64)
	require.Equal(t, pngBytes(9), data, "rows still in the WAL must be visible in the snapshot")
}

func TestFaviconSnapshotRefreshOnMTimeAndTTL(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{{id: 1, iconURL: "https://r.example/old.png", width: 16, data: pngBytes(11)}},
		[]favPage{{id: 1, pageURL: "https://r.example/", iconIDs: []int{1}}},
	)

	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 7, 20, 11, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := NewFaviconReader(ctx, FaviconOptions{
		ProfileDir: dir,
		Logf:       t.Logf,
		now:        func() time.Time { return now },
		mtime:      func() time.Time { return mtime },
	})

	data, _ := r.Lookup("https://r.example/", 64)
	require.Equal(t, pngBytes(11), data)

	// Rewrite the source with a fresh row set.
	require.NoError(t, os.Remove(filepath.Join(dir, faviconsFile)))
	buildFavicons(t, dir,
		[]favIcon{{id: 1, iconURL: "https://r.example/new.png", width: 16, data: pngBytes(22)}},
		[]favPage{{id: 1, pageURL: "https://r.example/", iconIDs: []int{1}}},
	)

	// mtime changed but the TTL has not elapsed: the old snapshot
	// keeps serving.
	mtime = mtime.Add(time.Minute)
	now = now.Add(2 * faviconProbeGap)
	data, _ = r.Lookup("https://r.example/", 64)
	require.Equal(t, pngBytes(11), data, "inside the TTL the snapshot is not re-copied")

	// TTL elapsed + changed mtime: the next lookup re-copies.
	now = now.Add(DefaultFaviconTTL)
	data, _ = r.Lookup("https://r.example/", 64)
	require.Equal(t, pngBytes(22), data, "a changed source past the TTL refreshes the snapshot")
}

func TestFaviconSnapshotCleanupOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	buildFavicons(t, dir,
		[]favIcon{{id: 1, iconURL: "https://c.example/i.png", width: 16, data: pngBytes(5)}},
		[]favPage{{id: 1, pageURL: "https://c.example/", iconIDs: []int{1}}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	r := NewFaviconReader(ctx, FaviconOptions{ProfileDir: dir, Logf: t.Logf})
	data, _ := r.Lookup("https://c.example/", 64)
	require.NotEmpty(t, data)
	r.mu.Lock()
	tmp := r.tmp
	r.mu.Unlock()
	require.NotEmpty(t, tmp)

	cancel()
	require.Eventually(t, func() bool {
		_, err := os.Stat(tmp)
		return os.IsNotExist(err)
	}, 3*time.Second, 10*time.Millisecond, "cancelling the constructor context removes the snapshot temp dir")
}

func TestBetterWidth(t *testing.T) {
	// (best, cand, want) -> cand wins?
	require.True(t, betterWidth(-1, 16, 64), "anything beats nothing")
	require.True(t, betterWidth(16, 64, 32), "covering beats not covering")
	require.False(t, betterWidth(64, 16, 32), "not covering never beats covering")
	require.True(t, betterWidth(128, 64, 32), "both cover: smaller wins")
	require.False(t, betterWidth(64, 128, 32), "both cover: larger loses")
	require.True(t, betterWidth(16, 24, 32), "neither covers: larger wins")
	require.False(t, betterWidth(24, 16, 32), "neither covers: smaller loses")
}
