package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // fixture history databases

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// writePlacesFixture builds a minimal places.sqlite in dir with one
// page visited `visits` times an hour ago.
func writePlacesFixture(t *testing.T, dir, url, title string, visits int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "places.sqlite")))
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE moz_places (id INTEGER PRIMARY KEY, url TEXT, title TEXT, hidden INTEGER NOT NULL DEFAULT 0);
CREATE TABLE moz_historyvisits (id INTEGER PRIMARY KEY, place_id INTEGER, visit_date INTEGER, visit_type INTEGER NOT NULL DEFAULT 1);
INSERT INTO moz_places (id, url, title, hidden) VALUES (1, ?, ?, 0);`, url, title)
	require.NoError(t, err)
	at := time.Now().Add(-time.Hour).UnixMicro()
	for i := 0; i < visits; i++ {
		_, err = db.Exec(`INSERT INTO moz_historyvisits (place_id, visit_date, visit_type) VALUES (1, ?, 1)`, at)
		require.NoError(t, err)
	}
}

// writeRecoveryFixture builds a minimal recovery.jsonlz4 in the
// profile dir with one open tab per url/title pair: the mozLz4 magic,
// the little-endian size, and a literals-only LZ4 block (which every
// block decoder accepts).
func writeRecoveryFixture(t *testing.T, dir, url, title string) {
	t.Helper()
	type entry struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	raw, err := json.Marshal(map[string]any{"windows": []map[string]any{{
		"tabs": []map[string]any{{"entries": []entry{{URL: url, Title: title}}, "index": 1}},
	}}})
	require.NoError(t, err)

	blob := []byte("mozLz40\x00")
	blob = binary.LittleEndian.AppendUint32(blob, uint32(len(raw)))
	// One literals-only sequence, 0xFF-chaining the length.
	if n := len(raw); n < 15 {
		blob = append(blob, byte(n)<<4)
	} else {
		blob = append(blob, 0xF0)
		rem := n - 15
		for rem >= 255 {
			blob = append(blob, 0xFF)
			rem -= 255
		}
		blob = append(blob, byte(rem))
	}
	blob = append(blob, raw...)

	p := filepath.Join(dir, "sessionstore-backups", "recovery.jsonlz4")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, blob, 0o644))
}

// writeProfilesINI marks prof (relative to base) as the default
// profile of base.
func writeProfilesINI(t *testing.T, base, prof string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(base, prof), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "profiles.ini"),
		[]byte(fmt.Sprintf("[Profile0]\nName=default\nIsRelative=1\nPath=%s\nDefault=1\n", prof)), 0o644))
}

// firefoxCfg is the config block the wiring tests use; the profileDir
// overrides default to empty (= shared discovery).
func firefoxCfg(sitesDir, tabsDir string) config.FirefoxConfig {
	cfg := config.DefaultFirefox()
	cfg.FrequentSites.ProfileDir = sitesDir
	cfg.OpenTabs.ProfileDir = tabsDir
	return cfg
}

func TestFirefoxSourcesNoProfileIsQuietlyNil(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // firefoxBases pinned to nil
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	sites, tabs := a.firefoxSources(firefoxCfg("", ""))
	require.Nil(t, sites, "no profile anywhere: no sources, so the providers never register")
	require.Nil(t, tabs)
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("firefox: no profile found")),
		"ONE quiet log line covers both sections")
}

func TestFirefoxSourcesProfileDirOverrides(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	sitesDir, tabsDir := t.TempDir(), t.TempDir()
	writePlacesFixture(t, sitesDir, "https://daily.example/", "Daily", 12)
	writeRecoveryFixture(t, tabsDir, "https://open.example/page", "Open page")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Independent per-section overrides bypass discovery entirely (the
	// pinned-nil firefoxBases would find nothing).
	sites, tabs := a.firefoxSources(firefoxCfg(sitesDir, tabsDir))
	require.NotNil(t, sites)
	require.NotNil(t, tabs)
	require.NotContains(t, buf.String(), "no profile found")

	require.Nil(t, sites(), "the first call returns immediately while the data loads")
	require.Eventually(t, func() bool { return len(sites()) == 1 },
		5*time.Second, 10*time.Millisecond)
	got := sites()[0]
	require.Equal(t, "https://daily.example/", got.URL)
	require.Equal(t, "Daily", got.Title)
	require.Equal(t, "daily.example", got.Host)
	require.Equal(t, 12, got.Visits)

	require.Eventually(t, func() bool { return len(tabs()) == 1 },
		5*time.Second, 10*time.Millisecond)
	tb := tabs()[0]
	require.Equal(t, "https://open.example/page", tb.URL)
	require.Equal(t, "Open page", tb.Title)
	require.Equal(t, "open.example", tb.Host)
}

func TestFirefoxSourcesSharedDiscovery(t *testing.T) {
	base := t.TempDir()
	writeProfilesINI(t, base, "abc.default")
	prof := filepath.Join(base, "abc.default")
	writePlacesFixture(t, prof, "https://found.example/", "Found", 15)
	writeRecoveryFixture(t, prof, "https://tab.example/", "Tab")

	a, _ := newTestApp(t, nil, Options{})
	var basesCalls atomic.Int32
	a.plat.firefoxBases = func() []string {
		basesCalls.Add(1)
		return []string{base}
	}

	sites, tabs := a.firefoxSources(firefoxCfg("", ""))
	require.NotNil(t, sites)
	require.NotNil(t, tabs)
	require.Equal(t, int32(1), basesCalls.Load(), "BOTH sections share ONE profile discovery")

	require.Eventually(t, func() bool { return len(sites()) == 1 },
		5*time.Second, 10*time.Millisecond)
	require.Equal(t, "found.example", sites()[0].Host)
	require.Eventually(t, func() bool { return len(tabs()) == 1 },
		5*time.Second, 10*time.Millisecond)
	require.Equal(t, "tab.example", tabs()[0].Host)
}

func TestFirefoxSourcesMixedOverrideAndFailedDiscovery(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // discovery finds nothing
	tabsDir := t.TempDir()
	writeRecoveryFixture(t, tabsDir, "https://only-tabs.example/", "Only tabs")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	sites, tabs := a.firefoxSources(firefoxCfg("", tabsDir))
	require.Nil(t, sites, "the override-less section degrades")
	require.NotNil(t, tabs, "the overridden section still works")
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("firefox: no profile found")))
}

func TestShutdownCancelsFirefoxContext(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	ctx := a.firefoxContext()
	require.Same(t, ctx, a.firefoxContext(), "one shared app-lifetime context")
	require.NoError(t, ctx.Err())

	a.Shutdown(context.Background())
	require.ErrorIs(t, ctx.Err(), context.Canceled,
		"Shutdown aborts in-flight history refreshes (bounded quit)")

	// A post-shutdown builder still gets a context -- the CANCELLED
	// one, so a late cache can never start refreshing -- and shutting
	// down again is harmless.
	require.Error(t, a.firefoxContext().Err())
	a.Shutdown(context.Background())
}

func TestBuildRegistryWiresFirefoxProviders(t *testing.T) {
	base := t.TempDir()
	writeProfilesINI(t, base, "p.default")
	prof := filepath.Join(base, "p.default")
	writePlacesFixture(t, prof, "https://wired.example/", "Wired", 20)
	writeRecoveryFixture(t, prof, "https://wired-tab.example/", "Wired tab")

	a, r := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir())
	a.plat.firefoxBases = func() []string { return []string{base} }
	a.newRegistry = a.buildRegistry
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	// Both providers dispatch for a plain query and emit their
	// sections once the caches have data (the first generations race
	// the initial reads, so keep querying).
	byPlugin := map[string]plugin.Emission{}
	require.Eventually(t, func() bool {
		gen := int(a.pluginGen.Load()) + 1
		a.QueryPlugins("wired", gen)
		for _, e := range r.emitted(eventPluginResults) {
			if len(e.payload) == 0 {
				continue
			}
			if got, ok := e.payload[0].(plugin.Emission); ok {
				byPlugin[got.Plugin] = got
			}
		}
		_, sitesOK := byPlugin["firefox-frequent"]
		_, tabsOK := byPlugin["firefox-tabs"]
		return sitesOK && tabsOK
	}, 10*time.Second, 50*time.Millisecond)

	em := byPlugin["firefox-frequent"]
	require.Equal(t, "Frequent Sites", em.Name)
	require.NotEmpty(t, em.Results)
	require.Equal(t, "Wired", em.Results[0].Title)
	require.Equal(t, "https://wired.example/", em.Results[0].Action.Value)

	em = byPlugin["firefox-tabs"]
	require.Equal(t, "Open Tabs", em.Name)
	require.NotEmpty(t, em.Results)
	require.Equal(t, "Wired tab", em.Results[0].Title)
	require.Equal(t, "https://wired-tab.example/", em.Results[0].Action.Value)
}
