package index

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
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

// blendBenchSignals seeds an ACTIVE frecency blend over st: nSeed
// spread-out entry paths get 1..5 recorded opens (some above the
// tier-jump threshold), the recency probe stats through an in-memory
// fake -- the bench measures the blend's bookkeeping and goroutine
// machinery, never real disk latency -- and a cwd boost is live so
// every signal's code path runs. Also returns the probe factory so
// the cold-probe row can rebuild an empty-cache probe per iteration.
func blendBenchSignals(st *Store, nSeed int) (*Blend, func() *frecency.Probe) {
	now := time.Now()
	nowFn := func() time.Time { return now }
	fstore := frecency.New("", frecency.Options{Now: nowFn})
	n := st.Len()
	step := n / nSeed
	if step == 0 {
		step = 1
	}
	seeded := 0
	for i := 0; i < n && seeded < nSeed; i += step {
		path := st.EntryPath(int32(i))
		for k := 0; k <= seeded%5; k++ {
			_ = fstore.RecordOpen(path) // memory-only: never errors
		}
		seeded++
	}
	lstat := func(path string) (os.FileInfo, error) {
		return fakeInfo{mod: now.Add(-time.Duration(len(path)) * time.Hour)}, nil
	}
	newProbe := func() *frecency.Probe {
		return frecency.NewProbe(frecency.ProbeOptions{Lstat: lstat, Now: nowFn})
	}
	return &Blend{
		Signals: frecency.Signals{
			Store:     fstore,
			Probe:     newProbe(),
			Cwd:       "/bench",
			CwdWeight: 1,
		},
		WeightFrecency: 1,
		WeightRecency:  1,
		WeightNoise:    1,
		TierJump:       3,
		Now:            nowFn,
	}, newProbe
}

// BenchmarkSearchBlend measures the frecency blend's whole cost
// envelope on the 1M store: the post-scan top-K pass (boost, penalty,
// and cwd lookups over the <= workers*limit merged candidates) plus
// the budgeted cold-candidate recency stats. "off" is the identical
// query with no blend (the pre-blend engine). "on" reuses one probe
// across iterations -- the steady state, where the TTL cache absorbs
// repeat keystrokes -- and "coldprobe" rebuilds the probe every
// iteration so every query pays the full stat batch, the worst case.
// "rare" merges a handful of candidates; "common" fills every shard
// heap, the largest merged set the blend can ever see.
func BenchmarkSearchBlend(b *testing.B) {
	_, s1M := searchFixtures()
	queries := []struct{ name, q string }{
		{"rare", "zzqx"},
		{"common", "data"},
	}
	for _, bq := range queries {
		hits := countMatches(s1M, bq.q)
		b.Run("1M/off/"+bq.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s1M.Query(bq.q, 50)
			}
			b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
			b.ReportMetric(float64(hits), "hits")
		})
		b.Run("1M/on/"+bq.name, func(b *testing.B) {
			blend, _ := blendBenchSignals(s1M, 200)
			opts := QueryOptions{Blend: blend}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s1M.QueryWith(bq.q, 50, opts)
			}
			b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
			b.ReportMetric(float64(hits), "hits")
		})
		b.Run("1M/coldprobe/"+bq.name, func(b *testing.B) {
			blend, newProbe := blendBenchSignals(s1M, 200)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bl := *blend
				bl.Signals.Probe = newProbe()
				_ = s1M.QueryWith(bq.q, 50, QueryOptions{Blend: &bl})
			}
			b.ReportMetric(b.Elapsed().Seconds()*1e3/float64(b.N), "ms/query")
			b.ReportMetric(float64(hits), "hits")
		})
	}
}
