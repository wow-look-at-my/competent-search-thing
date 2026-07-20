package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/telemetry"
)

// telTestApp builds a stubbed app with a small real index and the
// telemetry layer brought up per opt (newTestApp already points the
// config dir at a per-test temp dir, so the log lands there).
func telTestApp(t *testing.T, opt config.TelemetryConfig) (*App, string) {
	t.Helper()
	m := index.NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/notes", "report-q3.md", false))
	require.NoError(t, m.Add("/notes", "reports", true))
	a, _ := newTestApp(t, m, Options{Telemetry: opt})
	a.telOnce.Do(a.startTelemetry)
	dir, err := config.Dir()
	require.NoError(t, err)
	return a, filepath.Join(dir, telemetryFileName)
}

// readTelemetryRecords drains the async appends and parses the log.
func readTelemetryRecords(t *testing.T, a *App, path string) []map[string]any {
	t.Helper()
	a.telWG.Wait()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "line %q", line)
		out = append(out, m)
	}
	return out
}

func pickReportFor(a *App, query string) telemetry.PickReport {
	res := a.Search(query)
	shown := make([]telemetry.ShownRef, len(res))
	for i, r := range res {
		shown[i] = telemetry.ShownRef{Kind: telemetry.KindFile, Path: r.Path}
	}
	return telemetry.PickReport{
		Query:  query,
		Shown:  shown,
		Picked: telemetry.PickedRef{Rank: 0, Action: telemetry.ActionOpen},
	}
}

func TestTelemetryDisabledByDefault(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{})
	require.Nil(t, a.telLayer(), "the zero value keeps the feature off (opt-in)")

	// Search takes the plain Manager.Query path and RecordPick no-ops.
	require.Len(t, a.Search("report"), 2)
	require.NoError(t, a.RecordPick(pickReportFor(a, "report")))
	require.NoFileExists(t, path, "disabled telemetry never touches the disk")
}

func TestSearchStashesImpressionWhenEnabled(t *testing.T) {
	a, _ := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64})
	l := a.telLayer()
	require.NotNil(t, l)

	res := a.Search("  report  ") // Search trims; the ring key is the trimmed query
	require.Len(t, res, 2)
	imp := l.lookup("report")
	require.NotNil(t, imp, "the impression is stashed under the trimmed query")
	require.Len(t, imp.byPath, 2)
	sig, ok := imp.byPath["/notes/report-q3.md"]
	require.True(t, ok)
	require.False(t, sig.IsDir)
	require.False(t, imp.blendActive, "no blend wired in newTestApp")
	require.Nil(t, l.lookup("other"), "unknown queries miss")
}

func TestRecordPickWritesJoinedRecord(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64, RetainQueries: true})
	rep := pickReportFor(a, "report")
	rep.Shown = append(rep.Shown, telemetry.ShownRef{Kind: telemetry.KindPlugin, Plugin: "apps-search", Score: 90})
	require.NoError(t, a.RecordPick(rep))

	recs := readTelemetryRecords(t, a, path)
	require.Len(t, recs, 1)
	rec := recs[0]
	require.Equal(t, "report", rec["query"])
	require.Equal(t, true, rec["joined"], "the impression was found in the ring")
	require.Equal(t, false, rec["blendActive"])
	require.Equal(t, false, rec["refined"])

	shown, ok := rec["shown"].([]any)
	require.True(t, ok)
	require.Len(t, shown, 3)
	first, ok := shown[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "file", first["kind"])
	require.Equal(t, float64(0), first["rank"], "ranks are stamped Go-side from the index")
	require.Contains(t, first, "class")
	require.Contains(t, first, "boost")
	require.Equal(t, float64(2), first["depth"], "/notes/... has two components")
	last, ok := shown[2].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "plugin", last["kind"])
	require.Equal(t, "apps-search", last["plugin"])
	require.Equal(t, float64(90), last["score"])
	require.NotContains(t, last, "path", "plugin rows carry no file fields")

	picked, ok := rec["picked"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "file", picked["kind"])
	require.Equal(t, rep.Shown[0].Path, picked["path"], "the picked identity comes from the shown row")
	require.Equal(t, "open", picked["action"])
	require.Equal(t, false, picked["revealed"])

	require.Equal(t, filepath.Ext(rep.Shown[0].Path), first["ext"])
}

func TestRecordPickBlendActiveAndSignalsFlow(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64, RetainQueries: true})
	// Wire an ACTIVE blend with recorded opens so the trace carries a
	// real boost for the file the pick reports.
	st := frecency.New("", frecency.Options{})
	for i := 0; i < 5; i++ {
		require.NoError(t, st.RecordOpen("/notes/report-q3.md"))
	}
	a.manager.SetBlend(&index.Blend{
		Signals:        frecency.Signals{Store: st},
		WeightFrecency: 1,
		TierJump:       3,
	})

	require.NoError(t, a.RecordPick(pickReportFor(a, "report")))
	rec := readTelemetryRecords(t, a, path)[0]
	require.Equal(t, true, rec["blendActive"])
	shown := rec["shown"].([]any)
	var boosted map[string]any
	for _, r := range shown {
		row := r.(map[string]any)
		if row["path"] == "/notes/report-q3.md" {
			boosted = row
		}
	}
	require.NotNil(t, boosted)
	require.Greater(t, boosted["boost"].(float64), 3.0, "the impression-time boost is joined into the record")
}

func TestRecordPickRetainQueriesFalseLogsEmptyQuery(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64, RetainQueries: false})
	require.NoError(t, a.RecordPick(pickReportFor(a, "report")))
	rec := readTelemetryRecords(t, a, path)[0]
	require.Equal(t, "", rec["query"], "retainQueries false blanks the query text")
	require.Equal(t, true, rec["joined"], "the ring join still uses the real query")
	shown := rec["shown"].([]any)
	require.NotEmpty(t, shown[0].(map[string]any)["path"], "paths are still logged")
}

func TestRecordPickUnknownQueryLogsUnjoined(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64, RetainQueries: true})
	rep := telemetry.PickReport{
		Query:  "never-searched",
		Shown:  []telemetry.ShownRef{{Kind: telemetry.KindFile, Path: "/notes/report-q3.md"}},
		Picked: telemetry.PickedRef{Rank: 0, Action: telemetry.ActionOpen},
	}
	require.NoError(t, a.RecordPick(rep))
	rec := readTelemetryRecords(t, a, path)[0]
	require.Equal(t, false, rec["joined"], "an evicted/unknown impression is flagged")
	row := rec["shown"].([]any)[0].(map[string]any)
	require.Equal(t, float64(0), row["boost"], "no features without a join")
	require.Equal(t, float64(0), row["class"])
}

func TestRecordPickValidatesEchoedData(t *testing.T) {
	a, path := telTestApp(t, config.TelemetryConfig{Enabled: true, MaxSizeKB: 64, RetainQueries: true})
	base := pickReportFor(a, "report")

	blank := base
	blank.Query = "   "
	require.NoError(t, a.RecordPick(blank), "blank queries are a silent no-op")

	bad := base
	bad.Shown = []telemetry.ShownRef{{Kind: telemetry.KindFile, Path: "not-absolute"}}
	require.Error(t, a.RecordPick(bad), "echoed rows are re-validated")

	badRank := base
	badRank.Picked = telemetry.PickedRef{Rank: 99, Action: telemetry.ActionOpen}
	require.Error(t, a.RecordPick(badRank))

	a.telWG.Wait()
	require.NoFileExists(t, path, "rejected reports never reach the log")
}

func TestStartTelemetryUnresolvableConfigDirDisables(t *testing.T) {
	a, _ := newTestApp(t, index.NewManager(nil, nil, 0), Options{Telemetry: config.TelemetryConfig{Enabled: true}})
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	a.telOnce.Do(a.startTelemetry)
	require.Nil(t, a.telLayer(), "no config dir means telemetry stays off")
	require.NoError(t, a.RecordPick(telemetry.PickReport{Query: "x"}))
}

func TestApplyConfigTelemetryTogglesLive(t *testing.T) {
	a, _ := newTestApp(t, index.NewManager(nil, nil, 0), Options{})
	seedBaseline(a, config.Default())
	require.Nil(t, a.telLayer(), "default config keeps telemetry off")

	next := config.Default()
	next.Search.Telemetry.Enabled = true
	next.Search.Telemetry.RetainQueries = false
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "search.telemetry")
	require.Empty(t, res.Errors)
	l := a.telLayer()
	require.NotNil(t, l, "enabling search.telemetry builds the layer live")
	require.False(t, l.retainQueries, "the incoming config's knobs reach the fresh layer")

	off := config.Default()
	res = a.applyConfig(&off, "test")
	require.Contains(t, res.Applied, "search.telemetry")
	require.Nil(t, a.telLayer(), "disabling search.telemetry drops the layer live")
}

func TestApplyConfigTelemetryUnresolvableDirErrors(t *testing.T) {
	a, _ := newTestApp(t, index.NewManager(nil, nil, 0), Options{})
	seedBaseline(a, config.Default())
	t.Setenv(config.EnvConfigDir, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	next := config.Default()
	next.Search.Telemetry.Enabled = true
	res := a.applyConfig(&next, "test")
	require.NotEmpty(t, res.Errors, "asking for telemetry without a config dir is a reported apply error")
	require.Contains(t, res.Errors[0], "search.telemetry: ")
	require.NotContains(t, res.Applied, "search.telemetry")
	require.Nil(t, a.telLayer(), "the layer stays off when it cannot be built")
}

func TestTelemetryRingEvictsOldest(t *testing.T) {
	l := &telemetryLayer{store: telemetry.New(filepath.Join(t.TempDir(), "t.jsonl"), 64)}
	for i := 0; i < telemetryRingSize+2; i++ {
		q := "q" + string(rune('a'+i))
		l.stash(q, false, []index.ResultSignals{{Path: "/p/" + q}})
	}
	require.Nil(t, l.lookup("qa"), "the oldest entries are evicted")
	require.Nil(t, l.lookup("qb"))
	require.NotNil(t, l.lookup("qc"))
	newest := l.lookup("q" + string(rune('a'+telemetryRingSize+1)))
	require.NotNil(t, newest)

	// Re-stashing an existing query serves the NEWEST copy.
	l.stash("qc", true, nil)
	require.True(t, l.lookup("qc").blendActive)
}

func TestPathDepth(t *testing.T) {
	require.Equal(t, 0, pathDepth(""))
	require.Equal(t, 0, pathDepth("/"))
	require.Equal(t, 2, pathDepth("/notes/report.md"))
	require.Equal(t, 4, pathDepth("/home/u/reports/q3.md"))
	require.Equal(t, 4, pathDepth(`C:\Users\u\file.txt`))
}
