package app

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
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

// frequentSitesCfg is the config block the wiring tests use.
func frequentSitesCfg(profileDir string) config.FrequentSitesConfig {
	return config.FrequentSitesConfig{
		MinVisitsMonth: 11,
		MinVisitsWeek:  1,
		RefreshMinutes: 10,
		MaxResults:     6,
		ProfileDir:     profileDir,
	}
}

func TestFrequentSitesNoProfileIsQuietlyNil(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // firefoxBases pinned to nil
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	require.Nil(t, a.frequentSites(frequentSitesCfg("")),
		"no profile anywhere: no source, so the provider never registers")
	require.Contains(t, buf.String(), "firefox: no profile found; frequent-sites disabled")
}

func TestFrequentSitesProfileDirOverride(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	writePlacesFixture(t, dir, "https://daily.example/", "Daily", 12)

	getter := a.frequentSites(frequentSitesCfg(dir))
	require.NotNil(t, getter, "an explicit profileDir bypasses discovery entirely")
	require.Nil(t, getter(), "the first call returns immediately while the history loads")
	require.Eventually(t, func() bool { return len(getter()) == 1 },
		5*time.Second, 10*time.Millisecond)
	got := getter()[0]
	require.Equal(t, "https://daily.example/", got.URL)
	require.Equal(t, "Daily", got.Title)
	require.Equal(t, "daily.example", got.Host)
	require.Equal(t, 12, got.Visits)
}

func TestFrequentSitesDiscoversProfileFromBases(t *testing.T) {
	base := t.TempDir()
	prof := filepath.Join(base, "abc.default")
	require.NoError(t, os.MkdirAll(prof, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "profiles.ini"),
		[]byte("[Profile0]\nName=default\nIsRelative=1\nPath=abc.default\nDefault=1\n"), 0o644))
	writePlacesFixture(t, prof, "https://found.example/", "Found", 15)

	a, _ := newTestApp(t, nil, Options{})
	a.plat.firefoxBases = func() []string { return []string{base} }

	getter := a.frequentSites(frequentSitesCfg(""))
	require.NotNil(t, getter)
	require.Eventually(t, func() bool { return len(getter()) == 1 },
		5*time.Second, 10*time.Millisecond)
	require.Equal(t, "found.example", getter()[0].Host)
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

func TestBuildRegistryWiresFrequentSites(t *testing.T) {
	base := t.TempDir()
	prof := filepath.Join(base, "p.default")
	require.NoError(t, os.MkdirAll(prof, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "profiles.ini"),
		[]byte("[Profile0]\nPath=p.default\nIsRelative=1\nDefault=1\n"), 0o644))
	writePlacesFixture(t, prof, "https://wired.example/", "Wired", 20)

	a, r := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir())
	a.plat.firefoxBases = func() []string { return []string{base} }
	a.newRegistry = a.buildRegistry
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	// The provider dispatches for a plain query and emits its section
	// once the cache has data (the first generation may race the
	// initial history read, so keep querying).
	var em plugin.Emission
	require.Eventually(t, func() bool {
		gen := int(a.pluginGen.Load()) + 1
		a.QueryPlugins("wired", gen)
		for _, e := range r.emitted(eventPluginResults) {
			if len(e.payload) == 0 {
				continue
			}
			if got, ok := e.payload[0].(plugin.Emission); ok && got.Plugin == "firefox-frequent" {
				em = got
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, "Frequent Sites", em.Name)
	require.NotEmpty(t, em.Results)
	require.Equal(t, "Wired", em.Results[0].Title)
	require.Equal(t, "https://wired.example/", em.Results[0].Action.Value)
}
