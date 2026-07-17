package firefox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeRecoveryRaw writes raw bytes as profileDir's recovery.jsonlz4.
func writeRecoveryRaw(t *testing.T, profileDir string, data []byte) {
	t.Helper()
	p := recoveryPath(profileDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, data, 0o644))
}

// writeRecovery marshals sess, compresses it with the test-only
// reference compressor, and writes the fixture recovery.jsonlz4.
func writeRecovery(t *testing.T, profileDir string, sess any) {
	t.Helper()
	raw, err := json.Marshal(sess)
	require.NoError(t, err)
	writeRecoveryRaw(t, profileDir, mozLz4Compress(raw))
}

// entry / tab / window are terse fixture builders over the parse
// shapes.
func entry(url, title string) sessionEntry { return sessionEntry{URL: url, Title: title} }

func tab(idx int, entries ...sessionEntry) sessionTab {
	return sessionTab{Entries: entries, Index: idx}
}

func window(tabs ...sessionTab) sessionWindow { return sessionWindow{Tabs: tabs} }

func TestReadOpenTabsMultiWindow(t *testing.T) {
	dir := t.TempDir()
	pinned := tab(1, entry("https://pin.example/x", "Pinned page"))
	pinned.Pinned = true
	pinned.LastAccessed = 1700000000123
	writeRecovery(t, dir, sessionFile{Windows: []sessionWindow{
		window(
			pinned,
			tab(2, entry("https://old.example/", "Old"), entry("http://cur.example:8080/p?q=1", "Current")),
		),
		window(tab(1, entry("https://second-window.example/", "Second window"))),
	}})

	tabs, err := ReadOpenTabs(dir)
	require.NoError(t, err)
	require.Equal(t, []Tab{
		{URL: "https://pin.example/x", Title: "Pinned page", Host: "pin.example", Pinned: true, LastAccessed: 1700000000123},
		{URL: "http://cur.example:8080/p?q=1", Title: "Current", Host: "cur.example"},
		{URL: "https://second-window.example/", Title: "Second window", Host: "second-window.example"},
	}, tabs, "tabs from every window, each contributing its CURRENT entry, port stripped from the host")
}

func TestReadOpenTabsIndexClamping(t *testing.T) {
	dir := t.TempDir()
	entries := []sessionEntry{
		entry("https://first.example/", "First"),
		entry("https://middle.example/", "Middle"),
		entry("https://last.example/", "Last"),
	}
	writeRecovery(t, dir, sessionFile{Windows: []sessionWindow{window(
		tab(0, entries...), // invalid 0: clamped to the first entry
		tab(4, entries...), // len+1: clamped to the last entry
		tab(2, entries...), // in range: 1-based, so the middle entry
	)}})

	tabs, err := ReadOpenTabs(dir)
	require.NoError(t, err)
	require.Len(t, tabs, 3)
	require.Equal(t, "https://first.example/", tabs[0].URL)
	require.Equal(t, "https://last.example/", tabs[1].URL)
	require.Equal(t, "https://middle.example/", tabs[2].URL)
}

func TestReadOpenTabsFilters(t *testing.T) {
	dir := t.TempDir()
	hidden := tab(1, entry("https://hidden.example/", "Hidden"))
	hidden.Hidden = true
	writeRecovery(t, dir, sessionFile{Windows: []sessionWindow{window(
		hidden,
		tab(1), // no entries at all
		tab(1, entry("about:newtab", "New Tab")),
		tab(1, entry("moz-extension://abc/page.html", "Extension")),
		tab(1, entry("file:///home/me/doc.pdf", "A file")),
		tab(1, entry("https:///nohost", "Hostless")),
		tab(1, entry("", "Empty URL")),
		tab(1, entry("https://keep.example/", "Keep me")),
	)}})

	tabs, err := ReadOpenTabs(dir)
	require.NoError(t, err)
	require.Len(t, tabs, 1, "hidden, entry-less, and non-http(s) tabs are all dropped")
	require.Equal(t, "keep.example", tabs[0].Host)
}

func TestReadOpenTabsUnknownKeysIgnored(t *testing.T) {
	dir := t.TempDir()
	writeRecoveryRaw(t, dir, mozLz4Compress([]byte(`{
		"version": ["sessionrestore", 1],
		"windows": [{
			"selected": 1,
			"_closedTabs": [{"state": {}}],
			"tabs": [{
				"entries": [{"url": "https://real.example/", "title": "Real", "docshellUUID": "x"}],
				"index": 1, "attributes": {}, "userContextId": 0
			}]
		}],
		"_closedWindows": []
	}`)))

	tabs, err := ReadOpenTabs(dir)
	require.NoError(t, err)
	require.Len(t, tabs, 1)
	require.Equal(t, "https://real.example/", tabs[0].URL)
}

func TestReadOpenTabsMissingFileMeansClosedBrowser(t *testing.T) {
	dir := t.TempDir()
	// Even with a clean-shutdown sessionstore.jsonlz4 present, no
	// recovery snapshot = the browser is closed = no OPEN tabs. The
	// closed session is deliberately never read.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessionstore.jsonlz4"),
		mozLz4Compress([]byte(`{"windows":[{"tabs":[{"entries":[{"url":"https://stale.example/"}],"index":1}]}]}`)), 0o644))

	tabs, err := ReadOpenTabs(dir)
	require.NoError(t, err, "a missing recovery snapshot is a state, not an error")
	require.Empty(t, tabs)
}

func TestReadOpenTabsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	writeRecoveryRaw(t, dir, []byte("not a mozlz4 file at all"))
	tabs, err := ReadOpenTabs(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recovery.jsonlz4")
	require.Empty(t, tabs)

	// Valid compression wrapping invalid JSON is also an error.
	writeRecoveryRaw(t, dir, mozLz4Compress([]byte(`{"windows": [`)))
	tabs, err = ReadOpenTabs(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing")
	require.Empty(t, tabs)
}

func TestReadOpenTabsUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory where the snapshot file should be: ReadFile fails
	// with something other than ErrNotExist.
	require.NoError(t, os.MkdirAll(filepath.Join(recoveryPath(dir), "oops"), 0o755))
	_, err := ReadOpenTabs(dir)
	require.Error(t, err)
}

func TestReadOpenTabsCap(t *testing.T) {
	dir := t.TempDir()
	var tabs []sessionTab
	for i := 0; i < MaxOpenTabs+7; i++ {
		tabs = append(tabs, tab(1, entry(fmt.Sprintf("https://t%03d.example/", i), "T")))
	}
	writeRecovery(t, dir, sessionFile{Windows: []sessionWindow{window(tabs...)}})

	got, err := ReadOpenTabs(dir)
	require.NoError(t, err)
	require.Len(t, got, MaxOpenTabs, "pathological snapshots are capped")
}

func TestRecoveryMTime(t *testing.T) {
	dir := t.TempDir()
	require.True(t, RecoveryMTime(dir).IsZero(), "missing file probes as the zero time")

	writeRecovery(t, dir, sessionFile{})
	stamp := time.Date(2026, 7, 1, 8, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(recoveryPath(dir), stamp, stamp))
	require.True(t, RecoveryMTime(dir).Equal(stamp))
}

func TestHTTPHost(t *testing.T) {
	tests := []struct {
		raw  string
		host string
		ok   bool
	}{
		{"https://example.org/p", "example.org", true},
		{"http://example.org:8080/", "example.org", true},
		{"HTTPS://UPPER.example/", "UPPER.example", true},
		{"about:config", "", false},
		{"file:///etc/passwd", "", false},
		{"moz-extension://uuid/x", "", false},
		{"https:///nohost", "", false},
		{"", "", false},
		{"::bad::url::", "", false},
	}
	for _, tt := range tests {
		host, ok := httpHost(tt.raw)
		require.Equal(t, tt.ok, ok, "httpHost(%q)", tt.raw)
		require.Equal(t, tt.host, host, "httpHost(%q)", tt.raw)
	}
}
