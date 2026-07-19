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
		if got := normalizeQuery(in); got != want {
			t.Errorf("normalizeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtKey(t *testing.T) {
	cases := map[string]string{
		"/a/b/report.md":  ".md",
		"/a/b/PHOTO.JPG":  ".jpg",
		"/a/b/Makefile":   "",
		"/a/b/archive":    "",
		"/a/b.d/noext":    "",
		"/a/b/a.tar.gz":   ".gz",
		"C:\\x\\file.TXT": ".txt",
	}
	for in, want := range cases {
		if got := extKey(in); got != want {
			t.Errorf("extKey(%q) = %q, want %q", in, got, want)
		}
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
		if got := dirPrefixKey(in); got != want {
			t.Errorf("dirPrefixKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecayedWeight(t *testing.T) {
	if got := decayedWeight(2, testNow, testNow); got != 2 {
		t.Fatalf("no elapse must not decay: %v", got)
	}
	if got := decayedWeight(2, testNow.Add(time.Hour), testNow); got != 2 {
		t.Fatalf("future timestamps must not decay: %v", got)
	}
	half := decayedWeight(2, testNow.Add(-halfLife), testNow)
	if half < 0.99 || half > 1.01 {
		t.Fatalf("one half-life must halve: %v", half)
	}
}

func TestRateTerm(t *testing.T) {
	m := map[string]rateCell{
		"hot":   {picks: 30, impressions: 100}, // rate well above baseline
		"cold":  {picks: 0, impressions: 500},  // seen a lot, never picked
		"quiet": {picks: 1, impressions: 19},   // exactly baseline: (1+1)/(19+20)~0.051
	}
	if got := rateTerm(m, "missing"); got != 0 {
		t.Fatalf("missing key must contribute zero, got %v", got)
	}
	hot := rateTerm(m, "hot")
	if hot <= 0 || hot > rateWeight*rateLogClamp {
		t.Fatalf("hot key must nudge up within the clamp, got %v", hot)
	}
	cold := rateTerm(m, "cold")
	if cold >= 0 || cold < -rateWeight*rateLogClamp {
		t.Fatalf("never-picked key must nudge down within the clamp, got %v", cold)
	}
	quiet := rateTerm(m, "quiet")
	if quiet < -0.01 || quiet > 0.01 {
		t.Fatalf("near-baseline key must be near zero, got %v", quiet)
	}
	// The clamp binds for extreme rates.
	m["extreme"] = rateCell{picks: 1e6, impressions: 1e6}
	if got := rateTerm(m, "extreme"); got != rateWeight*rateLogClamp {
		t.Fatalf("clamp must bind, got %v", got)
	}
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
	if q != 1 || e == 0 || d == 0 {
		t.Fatalf("Counts = %d/%d/%d", q, e, d)
	}
	// .md: 2 impressions + 2 picks; .txt: 3 impressions (incl the
	// non-file-pick record) + 0 picks.
	if c := tb.ext[".md"]; c.picks != 2 || c.impressions != 2 {
		t.Fatalf(".md cell = %+v", c)
	}
	if c := tb.ext[".txt"]; c.picks != 0 || c.impressions != 3 {
		t.Fatalf(".txt cell = %+v", c)
	}
	// The repeat pick compounded: weight > 1 (decayed first + 1).
	e2 := tb.exact["rep"]["/a/report.md"]
	if e2.c <= 1 || e2.c > 2 {
		t.Fatalf("repeat pick weight = %v", e2.c)
	}
	// The non-file pick contributed no exact entry.
	if _, ok := tb.exact["other"]; ok {
		t.Fatal("non-file pick must not enter the exact table")
	}
}

func TestBuildTablesSkipsUnusableExactEntries(t *testing.T) {
	long := strings.Repeat("q", maxQueryBytes+1)
	longPath := "/" + strings.Repeat("p", maxPathBytes)
	recs := []PickRecord{
		rec(testNow, "", []string{"/a/x.md"}, "/a/x.md"),      // empty query
		rec(testNow, long, []string{"/a/x.md"}, "/a/x.md"),    // oversized query
		rec(testNow, "ok", []string{longPath}, longPath),      // oversized path
		rec(time.Time{}, "zero", []string{"/a/y.md"}, "/a/y.md"), // zero ts: stored at build now
	}
	tb := BuildTables(recs, nil, testNow)
	if len(tb.exact) != 1 {
		t.Fatalf("exact table = %v", tb.exact)
	}
	if e := tb.exact["zero"]["/a/y.md"]; e.t != testNow {
		t.Fatalf("zero-ts pick must stamp build time, got %v", e.t)
	}
	// Rates still counted the skipped rows.
	if c := tb.ext[".md"]; c.picks != 3 {
		t.Fatalf(".md picks = %v", c.picks)
	}
}

func TestBuildTablesBootstrapGate(t *testing.T) {
	frec := map[string]float64{
		"/home/me/docs/a.md":  3,
		"/home/me/docs/b.md":  2,
		"/home/me/bin/tool":   1,
	}
	// Below the gate: bootstrap folds in.
	tb := BuildTables(nil, frec, testNow)
	if c := tb.ext[".md"]; c.picks != 5 || c.impressions != 6 {
		t.Fatalf("bootstrap .md cell = %+v", c)
	}
	if c := tb.dir["/home/me/docs"]; c.picks != 5 || c.impressions != 6 {
		t.Fatalf("bootstrap dir cell = %+v", c)
	}
	if len(tb.exact) != 0 {
		t.Fatal("the exact table must never bootstrap")
	}

	// At or past the gate: telemetry only.
	var recs []PickRecord
	for i := 0; i < minTelemetryPicks; i++ {
		p := fmt.Sprintf("/t/pick%02d.txt", i)
		recs = append(recs, rec(testNow, "q", []string{p}, p))
	}
	tb = BuildTables(recs, frec, testNow)
	if _, ok := tb.ext[".md"]; ok {
		t.Fatal("bootstrap must not fold in once telemetry has enough picks")
	}
}

func TestBuildTablesRateCap(t *testing.T) {
	var recs []PickRecord
	for i := 0; i < maxExts+50; i++ {
		p := fmt.Sprintf("/a/f%d.e%d", i, i)
		n := 1 + i%3
		for j := 0; j < n; j++ {
			recs = append(recs, rec(testNow, "", []string{p}, ""))
		}
	}
	tb := BuildTables(recs, nil, testNow)
	if len(tb.ext) != maxExts {
		t.Fatalf("ext table size = %d, want %d", len(tb.ext), maxExts)
	}
	// The most-seen keys survive.
	if _, ok := tb.ext[fmt.Sprintf(".e%d", maxExts+49)]; (maxExts+49)%3 == 2 && !ok {
		t.Fatal("a most-seen key was evicted")
	}
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
	// More queries than the cap; each with one light pick except the
	// heavy ones, so eviction order is observable.
	for i := 0; i < maxQueries+40; i++ {
		q := fmt.Sprintf("query%04d", i)
		p := fmt.Sprintf("/q/f%04d.txt", i)
		recs = append(recs, rec(testNow, q, []string{p}, p))
	}
	tb := BuildTables(recs, nil, testNow)
	rows := tb.exact["crowded"]
	if len(rows) != maxRowsPerQuery {
		t.Fatalf("crowded rows = %d, want %d", len(rows), maxRowsPerQuery)
	}
	if _, ok := rows[fmt.Sprintf("/r/row%d.txt", maxRowsPerQuery+2)]; !ok {
		t.Fatal("the heaviest row must survive the row cap")
	}
	if _, ok := rows["/r/row0.txt"]; ok {
		t.Fatal("the lightest row must be evicted")
	}
	if len(tb.exact) != maxQueries {
		t.Fatalf("query count = %d, want %d", len(tb.exact), maxQueries)
	}
	if _, ok := tb.exact["crowded"]; !ok {
		t.Fatal("the heaviest query must survive query eviction")
	}
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
	var bytes int
	for q, rows := range tb.exact {
		bytes += len(q) + approxQueryOverhead
		for p := range rows {
			bytes += len(p) + approxRowOverhead
		}
	}
	if bytes > exactBudgetBytes {
		t.Fatalf("byte budget exceeded: %d > %d", bytes, exactBudgetBytes)
	}
	if len(tb.exact) == 0 {
		t.Fatal("the budget must trim, not empty, the table")
	}
}

func TestStorePriorFunc(t *testing.T) {
	st := New(Options{Now: func() time.Time { return testNow }})
	if fn := st.PriorFunc("rep"); fn != nil {
		t.Fatal("empty store must resolve nil")
	}
	var nilStore *Store
	if fn := nilStore.PriorFunc("rep"); fn != nil {
		t.Fatal("nil store must resolve nil")
	}
	nilStore.SetTables(&Tables{}) // must not panic
	if q, e, d := nilStore.Counts(); q+e+d != 0 {
		t.Fatal("nil store counts")
	}

	recs := []PickRecord{
		rec(testNow.Add(-time.Hour), "Rep", []string{"/docs/report.md", "/docs/report.txt"}, "/docs/report.md"),
	}
	st.SetTables(BuildTables(recs, nil, testNow))
	if q, e, d := st.Counts(); q != 1 || e == 0 || d == 0 {
		t.Fatalf("Counts = %d/%d/%d", q, e, d)
	}

	fn := st.PriorFunc("  REP ") // normalization must match the stored key
	if fn == nil {
		t.Fatal("resolver must be non-nil with tables")
	}
	picked := fn("/docs/report.md")
	other := fn("/docs/report.txt")
	if picked <= other {
		t.Fatalf("picked row must outscore its sibling: %v vs %v", picked, other)
	}
	if picked < exactWeight/4 {
		t.Fatalf("exact memory must dominate the rate nudges: %v", picked)
	}
	// A different query gets no exact term; rate nudges still apply.
	fn2 := st.PriorFunc("unrelated")
	if v := fn2("/docs/report.md"); v >= picked {
		t.Fatalf("no exact hit for a different query: %v vs %v", v, picked)
	}
	// Unknown keys everywhere: exactly zero.
	if v := fn2("/elsewhere/deep/tree/x.zzz"); v != 0 {
		t.Fatalf("unknown ext+dir must contribute zero, got %v", v)
	}
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadTelemetryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.jsonl")

	if recs, err := ReadTelemetryFile(path); recs != nil || err != nil {
		t.Fatalf("missing file: %v %v", recs, err)
	}

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
		// A record with nothing learnable (no file rows, plugin pick).
		`{"v":1,"ts":"2026-07-19T10:04:00Z","query":"x","shown":[{"rank":0,"kind":"plugin","plugin":"p","score":1}],` +
			`"picked":{"rank":0,"kind":"plugin","plugin":"p","action":"copy_text"}}`,
		// A bad ts still parses (zero TS).
		`{"v":1,"ts":"nonsense","query":"z","shown":[{"rank":0,"kind":"file","path":"/a/z.md"}],` +
			`"picked":{"rank":0,"kind":"file","path":"/a/z.md","action":"open"}}`,
	}
	writeFile(t, path, strings.Join(lines, "\n"))

	recs, err := ReadTelemetryFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d: %+v", len(recs), recs)
	}
	r0 := recs[0]
	if r0.Query != "rep" || r0.PickedPath != "/a/report.md" || len(r0.ShownFiles) != 1 {
		t.Fatalf("record 0 = %+v", r0)
	}
	if r0.TS.IsZero() {
		t.Fatal("record 0 must carry its timestamp")
	}
	if recs[1].PickedPath != "" || len(recs[1].ShownFiles) != 1 {
		t.Fatalf("plugin-pick record = %+v", recs[1])
	}
	if !recs[2].TS.IsZero() {
		t.Fatal("unparseable ts must yield zero TS")
	}
}

func TestReadTelemetryFileOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxSourceFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadTelemetryFile(path)
	if recs != nil || err != nil {
		t.Fatalf("oversized file must read as empty: %v %v", recs, err)
	}
}

func TestReadFrecencyWeights(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frecency.json")

	if w, err := ReadFrecencyWeights(path, testNow); w != nil || err != nil {
		t.Fatalf("missing file: %v %v", w, err)
	}

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
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	w, err := ReadFrecencyWeights(path, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 2 {
		t.Fatalf("weights = %v", w)
	}
	if v := w["/home/me/docs/a.md"]; v < 1.9 || v > 2.1 {
		t.Fatalf("one half-life must halve 4 to ~2, got %v", v)
	}

	writeFile(t, path, `{"v":9,"entries":{}}`)
	if _, err := ReadFrecencyWeights(path, testNow); err == nil {
		t.Fatal("wrong version must error for logging")
	}
	writeFile(t, path, `garbage`)
	if _, err := ReadFrecencyWeights(path, testNow); err == nil {
		t.Fatal("corrupt file must error for logging")
	}
}
