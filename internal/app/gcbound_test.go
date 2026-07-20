package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// testPrevGCPercent is what newTestApp's recording setGCPercent fake
// answers as the "previous" GOGC value -- deliberately not 100 or
// buildGCPercent, so a restore writing it back is unambiguous in the
// recorded sequence.
const testPrevGCPercent = 250

func TestBoundBuildGCAppliesAndRestores(t *testing.T) {
	var got []int
	set := func(pct int) int {
		got = append(got, pct)
		return 175
	}
	restore := boundBuildGC(set)
	require.Equal(t, []int{buildGCPercent}, got, "the bound applies exactly the build percentage")
	restore()
	require.Equal(t, []int{buildGCPercent, 175}, got, "restore writes back whatever value the bound displaced")
}

func TestBoundBuildGCNilSetIsInert(t *testing.T) {
	restore := boundBuildGC(nil)
	require.NotNil(t, restore)
	restore() // must not panic
}

// TestBuildIndexBoundsGCForTheBuildWindow drives the real buildIndex
// over a tiny fixture tree and asserts the GC seam saw exactly the
// apply-then-restore pair.
func TestBuildIndexBoundsGCForTheBuildWindow(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "afile.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)

	a, r := newTestApp(t, m, Options{})
	a.buildIndex(context.Background())

	r.mu.Lock()
	got := append([]int(nil), r.gcPercents...)
	r.mu.Unlock()
	require.Equal(t, []int{buildGCPercent, testPrevGCPercent}, got,
		"the build lowers GOGC once and restores the displaced value once")
}

// TestBuildIndexRestoresGCOnFailure pins the restore on the error
// path: a build that fails (malformed exclude pattern) must still put
// the previous GOGC value back.
func TestBuildIndexRestoresGCOnFailure(t *testing.T) {
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)

	a, r := newTestApp(t, m, Options{})
	a.buildIndex(context.Background())

	r.mu.Lock()
	got := append([]int(nil), r.gcPercents...)
	r.mu.Unlock()
	require.Equal(t, []int{buildGCPercent, testPrevGCPercent}, got,
		"a failed build still restores the displaced GOGC value")
}
