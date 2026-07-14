package index

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerBuildFromDiskAndQuery(t *testing.T) {
	root, want := makeDiskTree(t, 4, 10)
	m := NewManager([]string{root}, nil, 25)

	count, dur, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, want, count)
	require.Greater(t, dur, time.Duration(0))
	require.Equal(t, want, m.LiveCount())
	require.Equal(t, want, m.Len())

	res := m.Query("f_00", 0) // limit 0 selects the configured default
	require.NotEmpty(t, res)
	require.LessOrEqual(t, len(res), 25)

	// A rebuild picks up new files and swaps in a compacted store.
	writeFile(t, filepath.Join(root, "sub_000", "brand-new-file.xyz"))
	count2, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, want+1, count2)
	require.Len(t, m.Query("brand-new-file", 10), 1)
}

func TestManagerAddRemoveAndRatio(t *testing.T) {
	m := NewManager(nil, nil, 0)
	require.Equal(t, DefaultMaxResults, m.MaxResults())
	require.Equal(t, float64(0), m.TombstoneRatio(), "empty store has ratio 0")

	require.NoError(t, m.Add("/w", "alpha.txt", false))
	require.NoError(t, m.Add("/w", "beta", true))
	require.NoError(t, m.Add("/w/beta", "gamma.txt", false))
	require.Error(t, m.Add("relative", "bad", false), "AddEntry validation surfaces")

	require.Len(t, m.Query("gamma", 10), 1)
	require.Equal(t, 2, m.Remove("/w/beta"), "dir entry plus child")
	require.Empty(t, m.Query("gamma", 10))
	require.Equal(t, 3, m.Len())
	require.Equal(t, 1, m.LiveCount())
	require.InDelta(t, 2.0/3.0, m.TombstoneRatio(), 1e-9)
	require.Equal(t, 0, m.Remove("/w/missing"))
}

func TestManagerForEachLiveDir(t *testing.T) {
	m := NewManager(nil, nil, 0)
	require.NoError(t, m.Add("/w", "docs", true))
	require.NoError(t, m.Add("/w", "readme.txt", false))
	require.NoError(t, m.Add("/w/docs", "img", true))
	require.NoError(t, m.Add("/w", "gone", true))
	m.Remove("/w/gone")

	var dirs []string
	m.ForEachLiveDir(func(path string) bool {
		dirs = append(dirs, path)
		return true
	})
	require.Equal(t, []string{"/w/docs", "/w/docs/img"}, dirs,
		"live directory entries only: no files, no tombstones")

	// Early stop after the first hit.
	var first []string
	m.ForEachLiveDir(func(path string) bool {
		first = append(first, path)
		return false
	})
	require.Equal(t, []string{"/w/docs"}, first)
}

func TestManagerConfigAccessors(t *testing.T) {
	roots := []string{"/data"}
	excludes := []string{".git"}
	m := NewManager(roots, excludes, 7)
	require.Equal(t, 7, m.MaxResults())

	gotRoots := m.Roots()
	require.Equal(t, roots, gotRoots)
	gotRoots[0] = "/mutated"
	require.Equal(t, []string{"/data"}, m.Roots(), "Roots returns a copy")

	gotEx := m.Excludes()
	require.Equal(t, excludes, gotEx)
	gotEx[0] = "mutated"
	require.Equal(t, []string{".git"}, m.Excludes(), "Excludes returns a copy")

	empty := NewManager(nil, nil, 0)
	require.Nil(t, empty.Roots())
	require.Nil(t, empty.Excludes())
}

func TestManagerBuildErrorKeepsOldStore(t *testing.T) {
	m := NewManager([]string{t.TempDir()}, nil, 10)
	require.NoError(t, m.Add("/pre", "existing.txt", false))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := m.BuildFromDisk(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, m.Query("existing", 10), 1, "old store still answers after failed build")

	bad := NewManager([]string{t.TempDir()}, []string{"["}, 10)
	_, _, err = bad.BuildFromDisk(context.Background(), nil)
	require.Error(t, err, "bad exclude pattern fails the build")
}

// TestManagerConcurrentQueryAndMutate hammers Query from several
// goroutines while a writer adds and removes entries through the
// Manager, exercising the RWMutex contract (run under the race
// detector when the toolchain enables it).
func TestManagerConcurrentQueryAndMutate(t *testing.T) {
	m := NewManager(nil, nil, 20)
	const writes = 1500

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			queries := []string{"file", "chunk7", "nomatch-zz", "dir"}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.Query(queries[(i+r)%len(queries)], 20)
			}
		}(r)
	}

	rebuilds := 0
	for i := 0; i < writes; i++ {
		dir := fmt.Sprintf("/cc/chunk%d", i%10)
		require.NoError(t, m.Add("/cc", fmt.Sprintf("chunk%d", i%10), true))
		require.NoError(t, m.Add(dir, fmt.Sprintf("file%04d.txt", i), false))
		if i%7 == 0 {
			m.Remove(joinDir(dir, fmt.Sprintf("file%04d.txt", i)))
		}
		if i%500 == 250 {
			// Occasional full swap while queries are in flight.
			_, _, err := m.BuildFromDisk(context.Background(), nil)
			require.NoError(t, err)
			rebuilds++
		}
	}
	close(stop)
	wg.Wait()

	require.Equal(t, 3, rebuilds)
	// After the last rebuild (roots are empty) the store restarts from
	// zero; only writes after that point remain.
	require.Greater(t, m.LiveCount(), 0)
	res := m.Query("file", 20)
	require.NotEmpty(t, res)
	require.LessOrEqual(t, len(res), 20)
}
