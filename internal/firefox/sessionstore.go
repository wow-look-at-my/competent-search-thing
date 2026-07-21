package firefox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// recoveryFile is the crash-recovery session snapshot Firefox rewrites
// roughly every 15 seconds WHILE RUNNING, relative to the profile
// directory. On clean shutdown it is replaced by sessionstore.jsonlz4
// (the closed session); recovery.baklz4 is the previous snapshot.
const recoveryFile = "sessionstore-backups/recovery.jsonlz4"

// MaxOpenTabs caps the raw tab list one snapshot may yield, a guard
// against pathological session files (the ranking layer trims far
// lower anyway).
const MaxOpenTabs = 500

// Tab is one open Firefox tab from the session snapshot.
type Tab struct {
	URL   string
	Title string
	// Host is the URL's hostname (no port), parsed in Go.
	Host string
	// Pinned marks a pinned tab.
	Pinned bool
	// LastAccessed is the tab's last-activation time in milliseconds
	// since the Unix epoch (0 when the snapshot omits it).
	LastAccessed int64
	// FavIconURL is the tab's "image" attribute -- the favicon URL the
	// session snapshot recorded (an http(s) URL or a data: URI in
	// practice; Firefox occasionally stores internal schemes like
	// fake-favicon-uri:, passed through VERBATIM here -- consumers
	// validate). "" when the snapshot has none.
	FavIconURL string
}

// Session-snapshot JSON shapes, consumed fields only (the real file
// carries far more; unknown keys are ignored). Private windows are
// never persisted into the snapshot, so they can never appear here.
type sessionFile struct {
	Windows []sessionWindow `json:"windows"`
}

type sessionWindow struct {
	Tabs []sessionTab `json:"tabs"`
}

type sessionTab struct {
	// Entries is the tab's back/forward history; Index is 1-BASED into
	// it (the current page is Entries[Index-1]).
	Entries      []sessionEntry `json:"entries"`
	Index        int            `json:"index"`
	Hidden       bool           `json:"hidden"`
	Pinned       bool           `json:"pinned"`
	LastAccessed int64          `json:"lastAccessed"`
	// Image is the tab's favicon URL as SessionStore records it
	// (TabState collects gBrowser.getIcon(tab) under the "image" key).
	Image string `json:"image"`
}

type sessionEntry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// recoveryPath returns profileDir's recovery snapshot location.
func recoveryPath(profileDir string) string {
	return filepath.Join(profileDir, filepath.FromSlash(recoveryFile))
}

// RecoveryMTime returns the recovery snapshot's modification time, or
// the zero time when the file is missing or unreadable (Firefox not
// running). The tab cache uses it as its cheap staleness probe.
func RecoveryMTime(profileDir string) time.Time {
	fi, err := os.Stat(recoveryPath(profileDir))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// ReadOpenTabs reads profileDir's recovery snapshot and returns the
// currently-open tabs: hidden tabs are skipped, each tab contributes
// only its CURRENT history entry (Index clamped into range
// defensively; tabs with no usable entry are skipped), and only
// http(s) URLs with a host survive -- about:, moz-extension:, file:
// and friends are dropped (the open_url action layer would refuse
// them anyway).
//
// A missing snapshot returns empty WITHOUT error: Firefox only
// maintains recovery.jsonlz4 while it runs, so no file means the
// browser is closed and there are no open tabs. Deliberately NO
// fallback to sessionstore.jsonlz4 -- that is the last CLOSED
// session, and an "Open Tabs" section built from it would lie.
func ReadOpenTabs(profileDir string) ([]Tab, error) {
	data, err := os.ReadFile(recoveryPath(profileDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("firefox: %w", err)
	}
	raw, err := DecodeMozLz4(data, 0)
	if err != nil {
		return nil, fmt.Errorf("firefox: %s: %w", recoveryFile, err)
	}
	var sess sessionFile
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("firefox: parsing %s: %w", recoveryFile, err)
	}
	var tabs []Tab
	for _, w := range sess.Windows {
		for _, tb := range w.Tabs {
			if tb.Hidden || len(tb.Entries) == 0 {
				continue
			}
			e := tb.Entries[clampIndex(tb.Index, len(tb.Entries))-1]
			host, ok := httpHost(e.URL)
			if !ok {
				continue
			}
			tabs = append(tabs, Tab{
				URL:          e.URL,
				Title:        e.Title,
				Host:         host,
				Pinned:       tb.Pinned,
				LastAccessed: tb.LastAccessed,
				FavIconURL:   tb.Image,
			})
			if len(tabs) >= MaxOpenTabs {
				return tabs, nil
			}
		}
	}
	return tabs, nil
}

// clampIndex forces a 1-based session index into 1..n (n >= 1). Real
// snapshots are in range; 0 or out-of-range values are defensive
// territory.
func clampIndex(idx, n int) int {
	if idx < 1 {
		return 1
	}
	if idx > n {
		return n
	}
	return idx
}

// httpHost returns raw's hostname when raw is an http(s) URL with a
// host, ok=false otherwise.
func httpHost(raw string) (string, bool) {
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := u.Hostname()
	if host == "" {
		return "", false
	}
	return host, true
}
