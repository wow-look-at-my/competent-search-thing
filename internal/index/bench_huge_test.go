package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

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

	// The frecency-blend rows: the rare and common name queries with
	// the ranking blend active (seeded open counts, fake-stat probe,
	// live cwd boost; see BenchmarkSearchBlend). The blend-off twins
	// are the "name/rare" and "name/common" rows above.
	blend, _ := blendBenchSignals(st, 200)
	for _, row := range []struct{ name, q string }{{"rare", "zzqx"}, {"common", "data"}} {
		b.Run("blend/"+row.name, func(b *testing.B) {
			hits := hugeHitCount(st, row.q, false)
			opts := QueryOptions{Blend: blend}
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
