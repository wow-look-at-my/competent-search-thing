package priors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func TestNormalizeQuery(t *testing.T) {
	cases := map[string]string{
		"  rep  ":  "rep",
		"Rep":      "rep",
		"REPORT x": "report x",
		"":         "",
		"  ":       "",
	}
	for in, want := range cases {
		require.Equal(t, want, normalizeQuery(in), "normalizeQuery(%q)", in)
	}
}

func TestExtKey(t *testing.T) {
	cases := map[string]string{
		"/a/b/report.md":  ".md",
		"/a/b/PHOTO.JPG":  ".jpg",
		"/a/b/Makefile":   "",
		"/a/b/archive":    "",
		"/a/b/a.tar.gz":   ".gz",
		"C:\\x\\file.TXT": ".txt",
	}
	for in, want := range cases {
		require.Equal(t, want, extKey(in), "extKey(%q)", in)
	}
}

func TestDirPrefixKey(t *testing.T) {
	cases := map[string]string{
		"/home/me/projects/app/main.go": "/home/me/projects",
		"/home/me/notes.md":             "/home/me",
		"/home/file.txt":                "/home",
		"/file.txt":                     "",
		"relative/a/b/c/d.txt":          "relative/a/b",
		"C:\\Users\\me\\docs\\f.txt":    "C:\\Users\\me",
		"//x//y/z.txt":                  "//x//y",
	}
	for in, want := range cases {
		require.Equal(t, want, dirPrefixKey(in), "dirPrefixKey(%q)", in)
	}
}

func TestDecayedWeight(t *testing.T) {
	require.Equal(t, 2.0, decayedWeight(2, testNow, testNow), "no elapse must not decay")
	require.Equal(t, 2.0, decayedWeight(2, testNow.Add(time.Hour), testNow), "future timestamps must not decay")
	require.InDelta(t, 1.0, decayedWeight(2, testNow.Add(-halfLife), testNow), 0.01, "one half-life must halve")
}

func TestRateTerm(t *testing.T) {
	m := map[string]rateCell{
		"hot":   {picks: 30, impressions: 100}, // rate well above baseline
		"cold":  {picks: 0, impressions: 500},  // seen a lot, never picked
		"quiet": {picks: 1, impressions: 19},   // ~baseline: (1+1)/(19+20)
	}
	require.Zero(t, rateTerm(m, "missing"), "missing key must contribute zero")
	hot := rateTerm(m, "hot")
	require.Positive(t, hot)
	require.LessOrEqual(t, hot, rateWeight*rateLogClamp, "the clamp bounds the nudge")
	cold := rateTerm(m, "cold")
	require.Negative(t, cold)
	require.GreaterOrEqual(t, cold, -rateWeight*rateLogClamp)
	require.InDelta(t, 0, rateTerm(m, "quiet"), 0.01, "near-baseline keys are near zero")
	m["extreme"] = rateCell{picks: 1e6, impressions: 1e6}
	require.Equal(t, rateWeight*rateLogClamp, rateTerm(m, "extreme"), "the clamp must bind")
}

func rec(ts time.Time, query string, shown []string, picked string) PickRecord {
	return PickRecord{TS: ts, Query: query, ShownFiles: shown, PickedPath: picked}
}

func TestBuildTablesCounts(t *testing.T) {
	recs := []PickRecord{
		rec(testNow.Add(-time.Hour), "rep", []string{"/a/report.md", "/a/report.txt"}, "/a/report.md"),
		rec(testNow.Add(-time.Minute), "rep", []string{"/a/report.md", "/a/report.txt"}, "/a/report.md"),
		rec(testNow, "other", []string{"/b/x.txt"}, ""), // non-file pick: impressions only
	}
	tb := BuildTables(recs, nil, testNow)
	q, e, d := tb.Counts()
	require.Equal(t, 1, q)
	require.NotZero(t, e)
	require.NotZero(t, d)
	// .md: 2 impressions + 2 picks; .txt: 3 impressions (incl the
	// non-file-pick record) + 0 picks.
	require.Equal(t, rateCell{picks: 2, impressions: 2}, tb.ext[".md"])
	require.Equal(t, rateCell{picks: 0, impressions: 3}, tb.ext[".txt"])
	// The repeat pick compounded: weight > 1 (decayed first + 1).
	e2 := tb.exact["rep"]["/a/report.md"]
	require.Greater(t, e2.c, 1.0)
	require.LessOrEqual(t, e2.c, 2.0)
	// The non-file pick contributed no exact entry.
	require.NotContains(t, tb.exact, "other", "non-file picks must not enter the exact table")
}

func TestBuildTablesSkipsUnusableExactEntries(t *testing.T) {
	long := strings.Repeat("q", maxQueryBytes+1)
	longPath := "/" + strings.Repeat("p", maxPathBytes)
	recs := []PickRecord{
		rec(testNow, "", []string{"/a/x.md"}, "/a/x.md"),         // empty query
		rec(testNow, long, []string{"/a/x.md"}, "/a/x.md"),       // oversized query
		rec(testNow, "ok", []string{longPath}, longPath),         // oversized path
		rec(time.Time{}, "zero", []string{"/a/y.md"}, "/a/y.md"), // zero ts: stored at build now
	}
	tb := BuildTables(recs, nil, testNow)
	require.Len(t, tb.exact, 1)
	require.Equal(t, testNow, tb.exact["zero"]["/a/y.md"].t, "zero-ts picks must stamp build time")
	// Rates still counted the skipped rows.
	require.Equal(t, 3.0, tb.ext[".md"].picks)
}

func TestBuildTablesBootstrapGate(t *testing.T) {
	frec := map[string]float64{
		"/home/me/docs/a.md": 3,
		"/home/me/docs/b.md": 2,
		"/home/me/bin/tool":  1,
	}
	// Below the gate: bootstrap folds in.
	tb := BuildTables(nil, frec, testNow)
	require.Equal(t, rateCell{picks: 5, impressions: 6}, tb.ext[".md"])
	require.Equal(t, rateCell{picks: 5, impressions: 6}, tb.dir["/home/me/docs"])
	require.Empty(t, tb.exact, "the exact table must never bootstrap")

	// At or past the gate: telemetry only.
	var recs []PickRecord
	for i := 0; i < minTelemetryPicks; i++ {
		p := fmt.Sprintf("/t/pick%02d.txt", i)
		recs = append(recs, rec(testNow, "q", []string{p}, p))
	}
	tb = BuildTables(recs, frec, testNow)
	require.NotContains(t, tb.ext, ".md", "the bootstrap must not fold in once telemetry has enough picks")
}

func TestBuildTablesRateCap(t *testing.T) {
	var recs []PickRecord
	for i := 0; i < maxExts+50; i++ {
		p := fmt.Sprintf("/a/f%d.e%d", i, i)
		for j := 0; j < 1+i%3; j++ {
			recs = append(recs, rec(testNow, "", []string{p}, ""))
		}
	}
	tb := BuildTables(recs, nil, testNow)
	require.Len(t, tb.ext, maxExts)
	// A most-seen key (3 impressions: 560%3 == 2) must survive.
	require.Contains(t, tb.ext, ".e560", "a most-seen key must survive the cap")
}

func TestBuildTablesExactRowAndQueryCaps(t *testing.T) {
	var recs []PickRecord
	// One query with too many rows: only the heaviest survive.
	for i := 0; i < maxRowsPerQuery+3; i++ {
		p := fmt.Sprintf("/r/row%d.txt", i)
		for j := 0; j <= i; j++ { // later rows picked more often
			recs = append(recs, rec(testNow, "crowded", []string{p}, p))
		}
	}
	// More queries than the cap, each with one light pick, so the
	// eviction order is observable.
	for i := 0; i < maxQueries+40; i++ {
		q := fmt.Sprintf("query%04d", i)
		p := fmt.Sprintf("/q/f%04d.txt", i)
		recs = append(recs, rec(testNow, q, []string{p}, p))
	}
	tb := BuildTables(recs, nil, testNow)
	rows := tb.exact["crowded"]
	require.Len(t, rows, maxRowsPerQuery)
	require.Contains(t, rows, fmt.Sprintf("/r/row%d.txt", maxRowsPerQuery+2), "the heaviest row must survive")
	require.NotContains(t, rows, "/r/row0.txt", "the lightest row must be evicted")
	require.Len(t, tb.exact, maxQueries)
	require.Contains(t, tb.exact, "crowded", "the heaviest query must survive eviction")
}

func TestBuildTablesByteBudget(t *testing.T) {
	longDir := "/" + strings.Repeat("d", 900)
	var recs []PickRecord
	for i := 0; i < 700; i++ {
		q := fmt.Sprintf("bulky%03d", i)
		p := fmt.Sprintf("%s/f%03d.bin", longDir, i)
		recs = append(recs, rec(testNow, q, []string{p}, p))
	}
	tb := BuildTables(recs, nil, testNow)
	approx := 0
	for q, rows := range tb.exact {
		approx += len(q) + approxQueryOverhead
		for p := range rows {
			approx += len(p) + approxRowOverhead
		}
	}
	require.LessOrEqual(t, approx, exactBudgetBytes, "the byte budget must hold")
	require.NotEmpty(t, tb.exact, "the budget must trim, not empty, the table")
}

func TestStorePriorFunc(t *testing.T) {
	st := New(Options{Now: func() time.Time { return testNow }})
	require.Nil(t, st.PriorFunc("rep"), "an empty store must resolve nil")
	var nilStore *Store
	require.Nil(t, nilStore.PriorFunc("rep"), "a nil store must resolve nil")
	nilStore.SetTables(&Tables{}) // must not panic
	q0, e0, d0 := nilStore.Counts()
	require.Zero(t, q0+e0+d0)

	recs := []PickRecord{
		rec(testNow.Add(-time.Hour), "Rep", []string{"/docs/report.md", "/docs/report.txt"}, "/docs/report.md"),
	}
	st.SetTables(BuildTables(recs, nil, testNow))
	q, e, d := st.Counts()
	require.Equal(t, 1, q)
	require.NotZero(t, e)
	require.NotZero(t, d)

	fn := st.PriorFunc("  REP ") // normalization must match the stored key
	require.NotNil(t, fn)
	picked := fn("/docs/report.md")
	other := fn("/docs/report.txt")
	require.Greater(t, picked, other, "the picked row must outscore its sibling")
	require.GreaterOrEqual(t, picked, exactWeight/4, "exact memory must dominate the rate nudges")
	// A different query gets no exact term; rate nudges still apply.
	fn2 := st.PriorFunc("unrelated")
	require.Less(t, fn2("/docs/report.md"), picked)
	// Unknown keys everywhere: exactly zero.
	require.Zero(t, fn2("/elsewhere/deep/tree/x.zzz"))
}

func TestStoreSwapUnderReads(t *testing.T) {
	st := New(Options{Now: func() time.Time { return testNow }})
	recs := []PickRecord{rec(testNow, "q", []string{"/a/x.md"}, "/a/x.md")}
	st.SetTables(BuildTables(recs, nil, testNow))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if fn := st.PriorFunc("q"); fn != nil {
					_ = fn("/a/x.md")
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		st.SetTables(BuildTables(recs, nil, testNow))
	}
	close(stop)
	wg.Wait()
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestReadTelemetryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")

	missing, err := ReadTelemetryFile(path)
	require.NoError(t, err)
	require.Nil(t, missing, "a missing file is empty data")

	lines := []string{
		// A full v1 record with unknown fields sprinkled in.
		`{"v":1,"ts":"2026-07-19T10:00:00Z","query":"rep","blendActive":true,"joined":true,"refined":false,` +
			`"shown":[{"rank":0,"kind":"file","path":"/a/report.md","class":1,"align":7,"mystery":9},` +
			`{"rank":1,"kind":"plugin","plugin":"apps-search","score":90}],` +
			`"picked":{"rank":0,"kind":"file","path":"/a/report.md","action":"open","revealed":false}}`,
		// A plugin pick: impressions only.
		`{"v":1,"ts":"2026-07-19T10:01:00Z","query":"calc","shown":[{"rank":0,"kind":"file","path":"/a/b.txt"}],` +
			`"picked":{"rank":1,"kind":"plugin","plugin":"calc","action":"copy_text"}}`,
		// Wrong version, malformed JSON, non-record garbage, blank.
		`{"v":2,"ts":"2026-07-19T10:02:00Z","shown":[{"kind":"file","path":"/x"}],"picked":{}}`,
		`{"v":1,"ts":"2026-07-19T10:03:00Z","shown":[{`,
		`not json at all`,
		``,
		// Nothing learnable: no file rows, plugin pick.
		`{"v":1,"ts":"2026-07-19T10:04:00Z","query":"x","shown":[{"rank":0,"kind":"plugin","plugin":"p","score":1}],` +
			`"picked":{"rank":0,"kind":"plugin","plugin":"p","action":"copy_text"}}`,
		// A bad ts still parses (zero TS).
		`{"v":1,"ts":"nonsense","query":"z","shown":[{"rank":0,"kind":"file","path":"/a/z.md"}],` +
			`"picked":{"rank":0,"kind":"file","path":"/a/z.md","action":"open"}}`,
	}
	writeTestFile(t, path, strings.Join(lines, "\n"))

	recs, err := ReadTelemetryFile(path)
	require.NoError(t, err)
	require.Len(t, recs, 3)
	r0 := recs[0]
	require.Equal(t, "rep", r0.Query)
	require.Equal(t, "/a/report.md", r0.PickedPath)
	require.Equal(t, []string{"/a/report.md"}, r0.ShownFiles, "plugin rows never enter ShownFiles")
	require.False(t, r0.TS.IsZero(), "the timestamp must carry through")
	require.Empty(t, recs[1].PickedPath, "a plugin pick carries no picked path")
	require.Len(t, recs[1].ShownFiles, 1)
	require.True(t, recs[2].TS.IsZero(), "an unparseable ts must yield zero TS")
}

func TestReadTelemetryFileOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxSourceFileBytes+1))
	require.NoError(t, f.Close())
	recs, err := ReadTelemetryFile(path)
	require.NoError(t, err)
	require.Nil(t, recs, "an oversized file must read as empty")
}

func TestReadFrecencyWeights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frecency.json")

	missing, err := ReadFrecencyWeights(path, testNow)
	require.NoError(t, err)
	require.Nil(t, missing, "a missing file is empty data")

	doc := map[string]any{
		"v": 1,
		"entries": map[string]any{
			"/home/me/docs/a.md": map[string]any{"c": 4, "t": testNow.Add(-halfLife).Format(time.RFC3339)},
			"/home/me/docs/b.md": map[string]any{"c": 2, "t": testNow.Format(time.RFC3339)},
			"":                   map[string]any{"c": 2, "t": testNow.Format(time.RFC3339)},
			"/bad/count":         map[string]any{"c": -1, "t": testNow.Format(time.RFC3339)},
			"/bad/time":          map[string]any{"c": 1},
		},
	}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	writeTestFile(t, path, string(data))
	w, err := ReadFrecencyWeights(path, testNow)
	require.NoError(t, err)
	require.Len(t, w, 2, "garbage entries must be dropped")
	require.InDelta(t, 2.0, w["/home/me/docs/a.md"], 0.1, "one half-life must halve 4 to ~2")

	writeTestFile(t, path, `{"v":9,"entries":{}}`)
	_, err = ReadFrecencyWeights(path, testNow)
	require.Error(t, err, "a wrong version must error for logging")
	writeTestFile(t, path, `garbage`)
	_, err = ReadFrecencyWeights(path, testNow)
	require.Error(t, err, "a corrupt file must error for logging")
}
