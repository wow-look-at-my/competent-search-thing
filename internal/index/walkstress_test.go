package index

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This file is the concurrency/corruption stress gate for the walker +
// store fill path (field crash v395: intermittent
// "growslice: len out of range" inside appendName during startup
// indexing, with GC scanstack SIGSEGVs in sibling traces -- memory
// corruption signatures, not deterministic arithmetic).
//
// The walk runs against an injected readDirFn instead of the disk, so
// the walker's REAL concurrency (NumCPU workers, shared LIFO queue,
// store mutex, per-worker scratch buffers, unsafe.String views) runs
// at memory speed: hundreds of thousands of entries per second of
// allocation and append traffic under the production build window's
// GOGC=40. Every walk is followed by a full store integrity
// verification, so any corruption that does not already panic
// (growslice, index out of range) still fails the test.

const (
	stressRoot  = "/stress-synth-root"
	stressWidth = 6 // subdirs per dir above the leaf depth
	stressDepth = 4 // dir levels below the root
	// stressPruneDir is a full-path exclude: it both prunes a real
	// subtree and -- crucially -- makes Excluder.HasFullPatterns true,
	// so the per-file scratch-buffer + unsafe.String exclude path runs
	// for every file entry, exactly like production (system trees +
	// mount skips are always full-path patterns there).
	stressPruneDir = stressRoot + "/sd2/sd3"
)

// stressFiles is how many plain files a directory at the given depth
// carries. Varied on purpose: batch sizes and name-blob appends then
// cover many allocation size classes.
func stressFiles(depth int) int { return 9 + depth*17 }

type synthEntry struct {
	name string
	dir  bool
}

func (e synthEntry) Name() string { return e.name }
func (e synthEntry) IsDir() bool  { return e.dir }
func (e synthEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e synthEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

// synthDepth returns the depth of dir below stressRoot, or -1 when
// dir is outside the synthetic tree.
func synthDepth(dir string) int {
	if dir == stressRoot {
		return 0
	}
	if !strings.HasPrefix(dir, stressRoot+"/") {
		return -1
	}
	return strings.Count(dir[len(stressRoot):], "/")
}

// stressFileName builds file i's name for a directory at depth d: a
// fresh string per call (os.ReadDir also hands the walker newly
// allocated names), length varying ~14..110 bytes.
func stressFileName(d, i int) string {
	return fmt.Sprintf("f_%d_%04d_%s.dat", d, i, strings.Repeat("q", (i*7+d*13)%89))
}

// synthReadDir serves the deterministic in-memory tree: every dir has
// stressWidth subdirs while depth < stressDepth, stressFiles(depth)
// plain files, and two base-excluded ".tmp" files.
func synthReadDir(dir string) ([]fs.DirEntry, error) {
	d := synthDepth(dir)
	if d < 0 {
		return nil, fs.ErrNotExist
	}
	out := make([]fs.DirEntry, 0, stressWidth+stressFiles(d)+2)
	if d < stressDepth {
		for i := 0; i < stressWidth; i++ {
			out = append(out, synthEntry{name: fmt.Sprintf("sd%d", i), dir: true})
		}
	}
	n := stressFiles(d)
	for i := 0; i < n; i++ {
		out = append(out, synthEntry{name: stressFileName(d, i)})
	}
	return append(out, synthEntry{name: "a.tmp"}, synthEntry{name: "b.tmp"}), nil
}

// stressExpected recomputes, with independent plain recursion, how
// many entries a correct walk indexes: subdir entries + file entries,
// minus the base-excluded .tmp files and the pruned full-path subtree.
func stressExpected(dir string, depth int) int {
	total := stressFiles(depth)
	if depth < stressDepth {
		for i := 0; i < stressWidth; i++ {
			sub := fmt.Sprintf("%s/sd%d", dir, i)
			if sub == stressPruneDir {
				continue
			}
			total += 1 + stressExpected(sub, depth+1)
		}
	}
	return total
}

// verifyStoreIntegrity walks every entry of a filled store and checks
// the invariants corruption would break: exact entry count, strictly
// increasing name-offset table ending at the blob length, NUL-free
// sane names, and parent paths under the walk root. Plain loops with
// Fatalf so the whole scan stays cheap enough to run per iteration.
func verifyStoreIntegrity(t *testing.T, st *Store, want int) {
	t.Helper()
	if st.Len() != want || st.LiveCount() != want {
		t.Fatalf("store count: len=%d live=%d want=%d", st.Len(), st.LiveCount(), want)
	}
	if len(st.nameOff) != want+1 {
		t.Fatalf("offset table: len=%d want=%d", len(st.nameOff), want+1)
	}
	last := uint32(0)
	for i := 0; i < want; i++ {
		off := st.nameOff[i+1]
		if off <= last {
			t.Fatalf("offset table not increasing at %d: %d then %d", i, last, off)
		}
		last = off
		nb := st.nameBytes(int32(i))
		if len(nb) == 0 || len(nb) > 128 {
			t.Fatalf("entry %d: name length %d out of bounds", i, len(nb))
		}
		if bytes.IndexByte(nb, 0) >= 0 {
			t.Fatalf("entry %d: NUL inside name %q", i, nb)
		}
		if p := st.ParentDir(int32(i)); p != stressRoot && !strings.HasPrefix(p, stressRoot+"/") {
			t.Fatalf("entry %d: parent dir %q outside root", i, p)
		}
	}
	if int(last) != len(st.names) {
		t.Fatalf("blob length %d != final offset %d", len(st.names), last)
	}
}

// TestWalkStressIntegrity is the regression gate for the v395 startup
// indexing crash: concurrent Walks over the synthetic tree under the
// production GOGC=40 build window, full integrity verification after
// every walk. Corruption shows up either as the field panic itself
// (growslice/index out of range) or as a verification failure.
func TestWalkStressIntegrity(t *testing.T) {
	oldRead := readDirFn
	readDirFn = synthReadDir
	defer func() { readDirFn = oldRead }()

	// The production initial build runs under GOGC=40 (internal/app
	// gcbound.go); keep the GC just as hot here.
	oldGC := debug.SetGCPercent(40)
	defer debug.SetGCPercent(oldGC)

	excludes := []string{
		"*.tmp",        // base-name pattern
		stressPruneDir, // full-path pattern: activates the scratch/unsafe path
		"/nonexistent-mount-a",
		"/nonexistent-mount-b",
	}
	want := stressExpected(stressRoot, 0)
	require.Greater(t, want, 100_000, "synthetic tree unexpectedly small")

	// Bounded for CI (~3s); COMPETENT_SEARCH_STRESS_SECONDS extends
	// investigation runs without changing the shipped gate.
	budget := 3 * time.Second
	if s := os.Getenv("COMPETENT_SEARCH_STRESS_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			budget = time.Duration(n) * time.Second
		}
	}

	// 16 concurrent Walks x NumCPU workers each: on a 4-CPU CI runner
	// that is 64 walker goroutines -- the field machine's scale --
	// grinding through constant preemption.
	const conc = 16
	deadline := time.Now().Add(budget)
	iters := 0
	for iters < 2 || (time.Now().Before(deadline) && iters < 500) {
		iters++
		stores := make([]*Store, conc)
		statsv := make([]WalkStats, conc)
		errs := make([]error, conc)
		var wg sync.WaitGroup
		for i := 0; i < conc; i++ {
			stores[i] = NewStore()
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// The production walk always has a progress func (the
				// app's indexing line); run its throttle path too.
				var seen int
				statsv[i], errs[i] = Walk(context.Background(), stores[i], []string{stressRoot}, excludes,
					func(indexed int, done bool) { seen = indexed })
				_ = seen
			}(i)
		}
		wg.Wait()
		for i := 0; i < conc; i++ {
			require.NoError(t, errs[i], "iter %d walk %d", iters, i)
			require.Equal(t, want, statsv[i].Indexed, "iter %d walk %d indexed", iters, i)
			verifyStoreIntegrity(t, stores[i], want)
		}
	}
	t.Logf("stress: %d iterations x %d concurrent walks x %d entries, all verified", iters, conc, want)
}
