package index

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestMain exists to clean up the on-disk walk-benchmark fixture, which
// is built lazily (only when BenchmarkWalk actually runs) and shared
// across b.N ramp-ups.
func TestMain(m *testing.M) {
	code := m.Run()
	if walkFixRoot != "" {
		os.RemoveAll(walkFixRoot)
	}
	os.Exit(code)
}

// In-memory search fixtures, built once per process (NO disk IO).
var (
	searchFixOnce sync.Once
	searchFix100k *Store
	searchFix1M   *Store
)

func searchFixtures() (*Store, *Store) {
	searchFixOnce.Do(func() {
		searchFix100k = buildSynthStore(101, 100_000)
		searchFix1M = buildSynthStore(202, 1_000_000)
	})
	return searchFix100k, searchFix1M
}

// benchQueries covers the interesting query shapes. Hit counts against
// the seeded 1M fixture are reported as a "hits" metric per benchmark.
var benchQueries = []struct{ name, q string }{
	{"rare", "zzqx"},   // a handful of planted markers
	{"common", "data"}, // very frequent word
	{"prefix", "re"},   // many names start with "re" (report, readme, ...)
	{"single", "a"},    // pathological single-byte query
	{"nomatch", "qqqqzz"},
}

// countMatches is a plain linear reference count of live entries whose
// folded name contains q (used for the hits metric, outside timing).
func countMatches(st *Store, q string) int {
	pat, ascii := foldPattern(q)
	qs := string(pat)
	n := 0
	st.ForEachLive(func(id int32) bool {
		if strings.Contains(testFold(st.Name(id), ascii), qs) {
			n++
		}
		return true
	})
	return n
}

func BenchmarkSearch(b *testing.B) {
	s100k, s1M := searchFixtures()
	sizes := []struct {
		name string
		st   *Store
	}{
		{"100k", s100k},
		{"1M", s1M},
	}
	for _, size := range sizes {
		for _, bq := range benchQueries {
			b.Run(size.name+"/"+bq.name, func(b *testing.B) {
				hits := countMatches(size.st, bq.q)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = size.st.Query(bq.q, 50)
				}
				b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
				b.ReportMetric(float64(hits), "hits")
			})
		}
	}
}

// fuzzySelectiveQuery is the crafted fuzzy-selective benchmark query:
// ZERO substring hits against the synth vocabulary (no name contains
// "zqxr" contiguously) but a handful of subsequence hits -- exactly
// the planted rare markers, whose "zzqx_marker_<i>.bin" names thread
// z..q..x..r ('q' occurs nowhere else in the vocabulary). Both counts
// are verified in the bench setup.
const fuzzySelectiveQuery = "zqxr"

// countFuzzyOnlyMatches counts live entries the fuzzy tier would add:
// subsequence matches that are not substring matches (naive reference
// walk, outside timing).
func countFuzzyOnlyMatches(st *Store, q string) int {
	pat, ascii := foldPattern(q)
	qs := string(pat)
	n := 0
	st.ForEachLive(func(id int32) bool {
		folded := testFold(st.Name(id), ascii)
		if !strings.Contains(folded, qs) && naiveSubseq(folded, qs, ascii) {
			n++
		}
		return true
	})
	return n
}

// BenchmarkSearchFuzzy measures the fuzzy tier's cost envelope:
// "selective" runs phase 2 for real (zero substring hits, few
// subsequence hits); "skip" is a common query whose phase-1 hit count
// fills the limit, so phase 2 is skipped and the cost must track the
// pre-fuzzy engine; the two "disabled" rows are the same queries with
// the fuzzy tier off (the pre-change engine) for direct comparison.
func BenchmarkSearchFuzzy(b *testing.B) {
	s100k, s1M := searchFixtures()
	sizes := []struct {
		name string
		st   *Store
	}{
		{"100k", s100k},
		{"1M", s1M},
	}
	for _, size := range sizes {
		subSel := countMatches(size.st, fuzzySelectiveQuery)
		fuzzSel := countFuzzyOnlyMatches(size.st, fuzzySelectiveQuery)
		subCommon := countMatches(size.st, "data")
		require.Zero(b, subSel, "the selective query must have no substring hits")
		require.Greater(b, fuzzSel, 0, "the selective query must have subsequence hits")
		require.GreaterOrEqual(b, subCommon, 50, "the skip query must fill the limit in phase 1")

		rows := []struct {
			name, q  string
			disabled bool
			hits     int
			fhits    int
		}{
			{"selective", fuzzySelectiveQuery, false, subSel, fuzzSel},
			{"skip", "data", false, subCommon, 0},
			{"disabled-selective", fuzzySelectiveQuery, true, subSel, 0},
			{"disabled-common", "data", true, subCommon, 0},
		}
		for _, row := range rows {
			b.Run(size.name+"/"+row.name, func(b *testing.B) {
				opts := QueryOptions{FuzzyDisabled: row.disabled}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = size.st.QueryWith(row.q, 50, opts)
				}
				b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
				b.ReportMetric(float64(row.hits), "hits")
				b.ReportMetric(float64(row.fhits), "fhits")
			})
		}
	}
}

// Multi-term benchmark rows. "sel2" is the selective conjunction: the
// literal phrase "zzqx marker" has ZERO substring hits (no name holds
// the space) while the term conjunction hits exactly the planted rare
// markers, and the rare driver term bounds the whole scan. "common2"
// is the degenerate all-common conjunction whose all-substring hits
// fill the limit (the skip rule keeps the fuzzy sweep off). "fuzzy2"
// makes the DRIVER subsequence-only ("zqxr" threads through
// zzqx_marker names), forcing the phase-B sweep. "disabled2" is the
// pure substring conjunction with the fuzzy tier off.
var multiBenchRows = []struct {
	name, q  string
	disabled bool
}{
	{"sel2", "zzqx marker", false},
	{"common2", "data report", false},
	{"fuzzy2", "zqxr marker", false},
	{"disabled2", "data report", true},
}

// countMultiMatches is the naive reference count of live entries the
// multi-term engine should match (the hits metric, outside timing).
func countMultiMatches(st *Store, q string, fuzzyOff bool) int {
	type termPat struct {
		qs    string
		ascii bool
	}
	var pats []termPat
	for _, f := range strings.Fields(q) {
		pat, ascii := foldPattern(f)
		pats = append(pats, termPat{qs: string(pat), ascii: ascii})
	}
	n := 0
	st.ForEachLive(func(id int32) bool {
		name := st.Name(id)
		for _, tp := range pats {
			folded := testFold(name, tp.ascii)
			if strings.Contains(folded, tp.qs) {
				continue
			}
			if !fuzzyOff && naiveSubseq(folded, tp.qs, tp.ascii) {
				continue
			}
			return true // this entry fails; keep iterating
		}
		n++
		return true
	})
	return n
}

// BenchmarkSearchMulti measures the multi-term engine: the selective
// and degenerate conjunctions, the driver-fuzzy sweep, and the
// fuzzy-disabled twin (see multiBenchRows).
func BenchmarkSearchMulti(b *testing.B) {
	s100k, s1M := searchFixtures()
	sizes := []struct {
		name string
		st   *Store
	}{
		{"100k", s100k},
		{"1M", s1M},
	}
	for _, size := range sizes {
		require.Zero(b, countMatches(size.st, "zzqx marker"),
			"the literal phrase must have no substring hits (the space bug this fixes)")
		require.Positive(b, countMultiMatches(size.st, "zzqx marker", false),
			"the term conjunction must hit the planted markers")
		for _, row := range multiBenchRows {
			b.Run(size.name+"/"+row.name, func(b *testing.B) {
				hits := countMultiMatches(size.st, row.q, row.disabled)
				opts := QueryOptions{FuzzyDisabled: row.disabled}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = size.st.QueryWith(row.q, 50, opts)
				}
				b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
				b.ReportMetric(float64(hits), "hits")
			})
		}
	}
}

// benchPathQueries covers the path-mode query shapes. "1/data"
// straddles the dir/name join deep in the tree (dirs named *_1 whose
// children start with "data"); "bench/" and "/b" hit every entry
// through the dir prematch (substring resp. path-prefix class);
// "/bench" is the root dir path itself (path-prefix everywhere).
var benchPathQueries = []struct{ name, q string }{
	{"straddle", "1/data"},
	{"dirheavy", "bench/"},
	{"shallow", "/b"},
	{"exactish", "/bench"},
	{"nomatch", "qq/zz"},
}

// countPathMatches is the naive reference count of live entries whose
// folded full path contains q (the hits metric, outside timing).
func countPathMatches(st *Store, q string) int {
	pat, ascii := foldPattern(q)
	qs := string(pat)
	n := 0
	st.ForEachLive(func(id int32) bool {
		if strings.Contains(testFold(st.EntryPath(id), ascii), qs) {
			n++
		}
		return true
	})
	return n
}

func BenchmarkSearchPath(b *testing.B) {
	s100k, s1M := searchFixtures()
	sizes := []struct {
		name string
		st   *Store
	}{
		{"100k", s100k},
		{"1M", s1M},
	}
	for _, size := range sizes {
		for _, bq := range benchPathQueries {
			b.Run(size.name+"/"+bq.name, func(b *testing.B) {
				hits := countPathMatches(size.st, bq.q)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = size.st.Query(bq.q, 50)
				}
				b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
				b.ReportMetric(float64(hits), "hits")
			})
		}
	}
}

// On-disk walk fixture: ~50k entries (250 dirs x 200 files), built once
// per process on first use, removed by TestMain. Kept at 50k (not 100k)
// to bound CI benchmark wall time; see BENCH notes.
const (
	walkFixDirs        = 250
	walkFixFilesPerDir = 200
)

var (
	walkFixOnce  sync.Once
	walkFixRoot  string
	walkFixCount int
	walkFixErr   error
)

func walkFixture() (string, int, error) {
	walkFixOnce.Do(func() {
		dir, err := os.MkdirTemp("", "cst-walkbench-")
		if err != nil {
			walkFixErr = err
			return
		}
		walkFixRoot = dir
		for d := 0; d < walkFixDirs; d++ {
			sub := filepath.Join(dir, fmt.Sprintf("dir_%03d", d))
			if err := os.Mkdir(sub, 0o755); err != nil {
				walkFixErr = err
				return
			}
			for f := 0; f < walkFixFilesPerDir; f++ {
				name := filepath.Join(sub, fmt.Sprintf("file_%03d_%03d.dat", d, f))
				if err := os.WriteFile(name, nil, 0o644); err != nil {
					walkFixErr = err
					return
				}
			}
		}
		walkFixCount = walkFixDirs + walkFixDirs*walkFixFilesPerDir
	})
	return walkFixRoot, walkFixCount, walkFixErr
}

func BenchmarkWalk(b *testing.B) {
	root, want, err := walkFixture()
	require.Nil(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		stats, err := Walk(context.Background(), st, []string{root}, nil, nil)
		require.Nil(b, err)

		require.Equal(b, want, stats.Indexed)

	}
	b.ReportMetric(float64(want)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
}

// BenchmarkWalkRootMeasure walks the container's whole filesystem
// ("/", default excludes + mount skips, composed like BuildFromDisk)
// into a fresh store per iteration. The benchmark phase is not
// coverage-instrumented, so this is the honest wall-clock walk time
// (page-cache-hot after the first pass). Skips unless
// COMPETENT_SEARCH_MEASURE=1 -- see measure_test.go.
func BenchmarkWalkRootMeasure(b *testing.B) {
	if os.Getenv(measureEnv) == "" {
		b.Skip("set COMPETENT_SEARCH_MEASURE=1 to run the whole-filesystem walk benchmark")
	}
	roots := []string{"/"}
	excludes, _ := measureExcludes(roots)
	indexed := 0
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		stats, err := Walk(context.Background(), st, roots, excludes, nil)
		require.Nil(b, err)
		require.Greater(b, stats.Indexed, 0)
		indexed = stats.Indexed
	}
	b.ReportMetric(float64(indexed)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
	b.ReportMetric(float64(indexed), "entries")
}

// The huge-store query battery: the interesting name shapes plus the
// path-mode shapes (straddle = dir/name join, dirheavy = substring in
// every dir, exactish = path prefix of everything). Hit counts come
// from the cached reference scans in measure_test.go.
var hugeNameQueries = []struct{ name, q string }{
	{"rare", "zzqx"},
	{"common", "data"},
	{"prefix", "re"},
	{"nomatch", "qqqqzz"},
}

var hugePathQueries = []struct{ name, q string }{
	{"straddle", "1/data"},
	{"dirheavy", "bench/"},
	{"exactish", "/bench"},
	{"nomatch", "qq/zz"},
}

// BenchmarkSearchHuge measures query latency against the shared huge
// synthetic store (hugeStoreEntries entries; built once per process on
// first use, outside the timed sections). Skips unless
// COMPETENT_SEARCH_MEASURE_HUGE=1.
func BenchmarkSearchHuge(b *testing.B) {
	if os.Getenv(measureHugeEnv) == "" {
		b.Skip("set COMPETENT_SEARCH_MEASURE_HUGE=1 to run the huge-store search benchmarks")
	}
	st := hugeStore(b)
	run := func(kind string, queries []struct{ name, q string }, pathMode bool) {
		for _, bq := range queries {
			b.Run(kind+"/"+bq.name, func(b *testing.B) {
				hits := hugeHitCount(st, bq.q, pathMode)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = st.Query(bq.q, 50)
				}
				b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
				b.ReportMetric(float64(hits), "hits")
			})
		}
	}
	run("name", hugeNameQueries, false)
	run("path", hugePathQueries, true)

	// The fuzzy rows (see BenchmarkSearchFuzzy for the scenario
	// definitions), with the disabled twins for direct deltas.
	fuzzyRows := []struct {
		name, q  string
		disabled bool
	}{
		{"selective", fuzzySelectiveQuery, false},
		{"skip", "data", false},
		{"disabled-selective", fuzzySelectiveQuery, true},
		{"disabled-common", "data", true},
	}
	for _, row := range fuzzyRows {
		b.Run("fuzzy/"+row.name, func(b *testing.B) {
			hits := hugeHitCount(st, row.q, false)
			fhits := hugeFuzzyHitCount(st, row.q)
			opts := QueryOptions{FuzzyDisabled: row.disabled}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = st.QueryWith(row.q, 50, opts)
			}
			b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
			b.ReportMetric(float64(hits), "hits")
			b.ReportMetric(float64(fhits), "fhits")
		})
	}

	// The multi-term rows (see multiBenchRows for the scenarios).
	for _, row := range multiBenchRows {
		b.Run("multi/"+row.name, func(b *testing.B) {
			hits := countMultiMatches(st, row.q, row.disabled)
			opts := QueryOptions{FuzzyDisabled: row.disabled}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = st.QueryWith(row.q, 50, opts)
			}
			b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
			b.ReportMetric(float64(hits), "hits")
		})
	}
}

// BenchmarkHugeStoreMeasure performs the one-shot huge-store memory
// measurement (footprint, heap, timed live GCs, then release +
// baseline GCs -- see hugeMeasureAndReport) and afterwards times
// forced GCs on the emptied heap as its b.N loop, so ns/op is the
// post-release GC baseline. Declared AFTER BenchmarkSearchHuge: the
// measurement releases the shared store when done. Skips unless
// COMPETENT_SEARCH_MEASURE_HUGE=1.
func BenchmarkHugeStoreMeasure(b *testing.B) {
	if os.Getenv(measureHugeEnv) == "" {
		b.Skip("set COMPETENT_SEARCH_MEASURE_HUGE=1 to run the huge-store measurement")
	}
	hugeMeasureAndReport(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.GC()
	}
	b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/baselineGC")
}
