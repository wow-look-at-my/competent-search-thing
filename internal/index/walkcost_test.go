package index

import (
	"bufio"
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// walkCostFixtureEnv names an existing directory tree to walk. The
// benchmark below skips without it, so CI never pays for it: it exists
// to compare a walk against ITSELF across a change, which needs the
// same tree on both sides and therefore a tree the benchmark does not
// build. walkCostExcludesEnv adds a comma-separated exclude set, which
// turns on the walker's per-file scratch-buffer path.
const (
	walkCostFixtureEnv  = "COMPETENT_SEARCH_WALK_FIXTURE"
	walkCostExcludesEnv = "COMPETENT_SEARCH_WALK_EXCLUDES"
)

// BenchmarkWalkCost reports one whole walk across every axis a walker
// change can regress: wall time, CPU, memory and disk IO. The counts it
// reports alongside them (entries, dirs) are what makes the comparison
// meaningful -- a walk that got faster by reading fewer directories did
// not get faster.
//
// Disk IO deserves a word. os.ReadDir issues getdents, which /proc IO
// counts under neither rchar nor syscr, so those stay near zero. What
// does move is read_bytes and blk-in, and both are zero once the tree
// is in the page cache. A cold-cache comparison needs the caches
// dropped between the two sides.
func BenchmarkWalkCost(b *testing.B) {
	root := os.Getenv(walkCostFixtureEnv)
	if root == "" {
		b.Skipf("set %s to a directory tree to walk", walkCostFixtureEnv)
	}
	var excludes []string
	if s := os.Getenv(walkCostExcludesEnv); s != "" {
		excludes = strings.Split(s, ",")
	}

	var indexed, dirs int
	var last *Store
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	io0 := readProcIO(b)
	var ru0, ru1 syscall.Rusage
	require.Nil(b, syscall.Getrusage(syscall.RUSAGE_SELF, &ru0))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		stats, err := Walk(context.Background(), st, []string{root}, excludes, nil)
		require.Nil(b, err)
		indexed, dirs = stats.Indexed, stats.Dirs
		last = st
	}
	b.StopTimer()

	require.Nil(b, syscall.Getrusage(syscall.RUSAGE_SELF, &ru1))
	io1 := readProcIO(b)
	runtime.ReadMemStats(&m1)

	// Retained heap with the last store still reachable: the index's
	// real footprint. ru_maxrss is reported too, but it lands on one GC
	// plateau or another from run to run, so it is the weaker signal of
	// the two.
	runtime.GC()
	var held runtime.MemStats
	runtime.ReadMemStats(&held)
	runtime.KeepAlive(last)

	n := float64(b.N)
	b.ReportMetric(float64(indexed), "entries")
	b.ReportMetric(float64(dirs), "dirs")
	b.ReportMetric((secs(ru1.Utime)-secs(ru0.Utime))/n, "cpu-user-s/op")
	b.ReportMetric((secs(ru1.Stime)-secs(ru0.Stime))/n, "cpu-sys-s/op")
	b.ReportMetric(float64(m1.TotalAlloc-m0.TotalAlloc)/n/1e6, "alloc-MB/op")
	b.ReportMetric(float64(held.HeapAlloc)/1e6, "held-heap-MB")
	b.ReportMetric(float64(ru1.Maxrss)/1024, "peak-rss-MiB")
	b.ReportMetric(float64(ru1.Inblock-ru0.Inblock)/n, "blk-in/op")
	b.ReportMetric(float64(ru1.Majflt-ru0.Majflt)/n, "majflt/op")
	for _, k := range []string{"rchar", "read_bytes"} {
		b.ReportMetric(float64(io1[k]-io0[k])/n, "io-"+k+"/op")
	}
}

func secs(t syscall.Timeval) float64 {
	return float64(t.Sec) + float64(t.Usec)/1e6
}

// readProcIO returns the process-wide IO counters.
func readProcIO(tb testing.TB) map[string]uint64 {
	tb.Helper()
	f, err := os.Open("/proc/self/io")
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	out := make(map[string]uint64)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		out[k] = n
	}
	return out
}
