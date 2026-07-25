package index

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Mutation-path benchmarks: the watcher drives these under the
// Manager's WRITE lock, so their cost is UI freeze time, not just
// throughput. The search benchmarks live in bench_test.go.

// benchLeafDir returns the path of an interned directory with no
// subdirectories of its own -- the shape whose removal must cost
// nothing beyond its own children.
func benchLeafDir(b *testing.B, st *Store) string {
	b.Helper()
	for did, dir := range st.dirs {
		hasSubdir := false
		for _, id := range st.children[uint32(did)] {
			if st.IsDir(id) {
				hasSubdir = true
				break
			}
		}
		if !hasSubdir && dir != "/bench" {
			return dir
		}
	}
	b.Fatal("synthetic store has no leaf subdirectory")
	return ""
}

// BenchmarkRemoveByPathLeaf measures tombstoning one interned directory
// that has no subdirectories. The cost must scale with the removed
// SUBTREE, never with the total number of directories in the store --
// the watcher issues one Remove per vanished child, so an O(all dirs)
// implementation turns `rm -rf` into a multi-second freeze held under
// the Manager's write lock.
func BenchmarkRemoveByPathLeaf(b *testing.B) {
	for _, total := range []int{100_000, 1_000_000} {
		st := buildSynthStore(303, total)
		leaf := benchLeafDir(b, st)
		b.Run(fmt.Sprintf("entries=%d/dirs=%d", total, len(st.dirs)), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				st.RemoveByPath(leaf)
			}
		})
	}
}

// BenchmarkRemoveByPathMissing measures the miss path: a path the store
// never knew. The watcher hits this constantly (every deleted file
// under an unindexed or already-pruned tree).
func BenchmarkRemoveByPathMissing(b *testing.B) {
	st := buildSynthStore(404, 1_000_000)
	b.ResetTimer() // the fixture build is not what this measures
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		st.RemoveByPath("/bench/does/not/exist")
	}
}

// defaultishExcludes mirrors the shape config actually ships (see
// config's baseExcludes + noiseExcludes + system trees): base names and
// absolute system paths, every one of them a literal. This is the
// pattern set a real index build runs against -- BenchmarkWalk passes
// none at all, so it cannot see the exclude cost.
var defaultishExcludes = []string{
	".git", "node_modules", ".cache",
	".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache",
	".ruff_cache", ".tox", ".nox", ".venv", "lost+found",
	"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp",
}

// BenchmarkWalkExcludes is BenchmarkWalk with the excludes a real build
// carries. Every walked entry pays MatchBase and -- because the set
// contains full-path patterns -- every file entry pays MatchFull too.
func BenchmarkWalkExcludes(b *testing.B) {
	root, want, err := walkFixture()
	require.Nil(b, err)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		st := NewStore()
		stats, err := Walk(context.Background(), st, []string{root}, defaultishExcludes, nil)
		require.Nil(b, err)
		require.Equal(b, want, stats.Indexed)
	}
	b.ReportMetric(float64(want)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
}

// BenchmarkExcluderMatch measures the walk's per-entry exclude check
// against a realistic default pattern set (see config's baseExcludes +
// noiseExcludes + system trees). Every walked entry pays MatchBase, and
// every file entry pays MatchFull too, so this runs tens of millions of
// times per whole-filesystem index build.
func BenchmarkExcluderMatch(b *testing.B) {
	patterns := []string{
		".git", "node_modules", ".cache",
		".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache",
		".ruff_cache", ".tox", ".nox", ".venv", "lost+found",
		"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp",
	}
	ex, err := NewExcluder(patterns)
	if err != nil {
		b.Fatal(err)
	}
	names := []string{"report.go", "README.md", "node_modules", "data.json", ".git"}
	fulls := []string{
		"/home/u/src/report.go", "/home/u/src/README.md",
		"/home/u/src/node_modules", "/var/lib/data.json", "/proc",
	}
	b.Run("base", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ex.MatchBase(names[i%len(names)])
		}
	})
	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ex.MatchFull(fulls[i%len(fulls)])
		}
	})
	// The glob-only set must keep working at the same semantics; it is
	// the slow path both above avoid.
	exGlob, err := NewExcluder([]string{"*.tmp", "*.swp", "/home/*/secret"})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("glob", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			exGlob.MatchBase(names[i%len(names)])
		}
	})
}
