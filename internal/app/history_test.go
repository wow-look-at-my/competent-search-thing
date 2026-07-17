package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
)

// The history store wiring: Startup builds it against the config dir
// (newTestApp points COMPETENT_SEARCH_CONFIG_DIR at a private temp
// dir), the bound methods round-trip through it, and everything
// degrades to safe no-ops without one.

func testHistoryPath(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(config.EnvConfigDir)
	require.NotEmpty(t, dir, "newTestApp must have pinned the config dir")
	return filepath.Join(dir, historyFileName)
}

func TestHistoryRoundTripAndPersists(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())

	require.NotNil(t, a.GetHistory())
	require.Empty(t, a.GetHistory())

	a.AddHistory("report q3")
	a.AddHistory("!version")
	require.Equal(t, []string{"report q3", "!version"}, a.GetHistory())

	data, err := os.ReadFile(testHistoryPath(t))
	require.NoError(t, err, "history.json written when persistence is on")
	var onDisk []string
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, []string{"report q3", "!version"}, onDisk)
}

func TestHistoryLoadsPreviousRunsEntries(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.NoError(t, os.WriteFile(testHistoryPath(t),
		[]byte(`["from last run"]`), 0o600))
	a.Startup(context.Background())
	require.Equal(t, []string{"from last run"}, a.GetHistory())
}

func TestHistoryPersistDisabledWritesNothing(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{HistoryPersistDisabled: true})
	a.Startup(context.Background())

	a.AddHistory("memory only")
	require.Equal(t, []string{"memory only"}, a.GetHistory(),
		"in-session recall still works")

	_, err := os.Stat(testHistoryPath(t))
	require.True(t, os.IsNotExist(err), "memory-only mode must not write history.json")
}

func TestHistoryCorruptFileLogsAndRunsOn(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.NoError(t, os.WriteFile(testHistoryPath(t), []byte("{nope"), 0o600))

	buf := captureLog(t)
	a.Startup(context.Background())
	require.Contains(t, buf.String(), "history: ", "the load failure is logged once")
	require.Contains(t, buf.String(), "starting empty")

	require.Empty(t, a.GetHistory(), "a corrupt file starts empty")
	a.AddHistory("fresh")
	require.Equal(t, []string{"fresh"}, a.GetHistory(), "the store stays functional")
}

func TestHistoryNilStoreNoOps(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// No Startup: the store was never built.
	got := a.GetHistory()
	require.NotNil(t, got)
	require.Empty(t, got)
	a.AddHistory("dropped") // must not panic
	require.Empty(t, a.GetHistory())
}

func TestHistoryDisabledWhenConfigDirUnresolvable(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	buf := captureLog(t)
	a.startHistory()
	require.Contains(t, buf.String(), "history: ")
	require.Empty(t, a.GetHistory())
	a.AddHistory("dropped")
	require.Empty(t, a.GetHistory(), "no store: adds are dropped")
}

func TestHistoryAddErrorIsLogged(t *testing.T) {
	// Point the config dir below a regular file: history.json's parent
	// can then never be created, so persisting fails while the
	// in-memory list keeps working.
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, filepath.Join(blocker, "sub"))
	buf := captureLog(t)
	a.startHistory()

	a.AddHistory("cannot persist")
	require.Contains(t, buf.String(), "history: ", "the save failure is logged")
	require.Equal(t, []string{"cannot persist"}, a.GetHistory(),
		"the in-memory list still updated")
}
