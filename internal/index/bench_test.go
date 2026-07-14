package index

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
// lowered name contains q (used for the hits metric, outside timing).
func countMatches(st *Store, q string) int {
	pat := []byte(strings.ToLower(q))
	n := 0
	st.ForEachLive(func(id int32) bool {
		if bytes.Contains(st.lowerNameBytes(id), pat) {
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
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		stats, err := Walk(context.Background(), st, []string{root}, nil, nil)
		if err != nil {
			b.Fatal(err)
		}
		if stats.Indexed != want {
			b.Fatalf("indexed %d entries, want %d", stats.Indexed, want)
		}
	}
	b.ReportMetric(float64(want)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
}
