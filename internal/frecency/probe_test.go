package frecency

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeInfo is a minimal os.FileInfo whose Sys() is nil, driving the
// mtime fallback of fileRecency on every OS.
type fakeInfo struct {
	name string
	mod  time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// countingLstat serves fixed mtimes and counts calls per path.
type countingLstat struct {
	mu    sync.Mutex
	times map[string]time.Time // missing = return an error
	calls map[string]int
}

func newCountingLstat(times map[string]time.Time) *countingLstat {
	return &countingLstat{times: times, calls: map[string]int{}}
}

func (c *countingLstat) lstat(path string) (os.FileInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[path]++
	mod, ok := c.times[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeInfo{name: filepath.Base(path), mod: mod}, nil
}

func (c *countingLstat) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[path]
}

func TestBatchRecencyReturnsRecencies(t *testing.T) {
	mod := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	fake := newCountingLstat(map[string]time.Time{"/a": mod, "/b": mod.Add(time.Hour)})
	p := NewProbe(ProbeOptions{Lstat: fake.lstat})
	got := p.BatchRecency(context.Background(), []string{"/a", "/b", "/missing"}, time.Second)
	require.Equal(t, mod, got["/a"])
	require.Equal(t, mod.Add(time.Hour), got["/b"])
	require.True(t, got["/missing"].IsZero(), "failed stats read as the zero time")
	require.Len(t, got, 2)
}

func TestBatchRecencyTTLCachesHitsAndMisses(t *testing.T) {
	clock := newClock()
	mod := clock.Now().Add(-time.Hour)
	fake := newCountingLstat(map[string]time.Time{"/a": mod})
	p := NewProbe(ProbeOptions{Lstat: fake.lstat, Now: clock.Now})

	for i := 0; i < 3; i++ {
		got := p.BatchRecency(context.Background(), []string{"/a", "/gone"}, time.Second)
		require.Equal(t, mod, got["/a"])
	}
	require.Equal(t, 1, fake.count("/a"), "repeat queries within the TTL are free")
	require.Equal(t, 1, fake.count("/gone"), "failures are cached negatively too")

	clock.Advance(DefaultProbeTTL + time.Second)
	_ = p.BatchRecency(context.Background(), []string{"/a", "/gone"}, time.Second)
	require.Equal(t, 2, fake.count("/a"), "past the TTL the path is re-statted")
	require.Equal(t, 2, fake.count("/gone"))
}

func TestBatchRecencyDedupesInputPaths(t *testing.T) {
	fake := newCountingLstat(map[string]time.Time{"/a": time.Now()})
	p := NewProbe(ProbeOptions{Lstat: fake.lstat})
	got := p.BatchRecency(context.Background(), []string{"/a", "/a", "", "/a"}, time.Second)
	require.Len(t, got, 1)
	require.Equal(t, 1, fake.count("/a"))
}

func TestBatchRecencyBudgetCutoff(t *testing.T) {
	unblock := make(chan struct{})
	mod := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	var slowDone atomic.Bool
	slow := func(path string) (os.FileInfo, error) {
		if path == "/slow" {
			<-unblock
			slowDone.Store(true)
		}
		return fakeInfo{name: filepath.Base(path), mod: mod}, nil
	}
	p := NewProbe(ProbeOptions{Lstat: slow})

	start := time.Now()
	got := p.BatchRecency(context.Background(), []string{"/fast", "/slow"}, 50*time.Millisecond)
	elapsed := time.Since(start)
	require.Less(t, elapsed, 2*time.Second, "the call never blocks past the budget")
	require.Equal(t, mod, got["/fast"], "paths statted in time are served")
	require.True(t, got["/slow"].IsZero(), "paths past the budget read as the zero time")

	// The straggler finishes in the background and lands in the TTL
	// cache: a later query gets it without waiting.
	close(unblock)
	require.Eventually(t, func() bool {
		if !slowDone.Load() {
			return false
		}
		return !p.BatchRecency(context.Background(), []string{"/slow"}, 0)["/slow"].IsZero()
	}, 5*time.Second, 10*time.Millisecond, "the straggler's result reaches the cache")
}

func TestBatchRecencyContextCancellation(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slow := func(string) (os.FileInfo, error) {
		<-block
		return nil, os.ErrNotExist
	}
	p := NewProbe(ProbeOptions{Lstat: slow})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	got := p.BatchRecency(ctx, []string{"/a"}, time.Hour)
	require.Less(t, time.Since(start), 5*time.Second, "cancellation aborts before the budget")
	require.Empty(t, got)
}

func TestBatchRecencyBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	slow := func(path string) (os.FileInfo, error) {
		cur := inFlight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return fakeInfo{name: path, mod: time.Now()}, nil
	}
	p := NewProbe(ProbeOptions{Lstat: slow, Workers: 2})
	paths := make([]string, 8)
	for i := range paths {
		paths[i] = fmt.Sprintf("/p%d", i)
	}
	got := p.BatchRecency(context.Background(), paths, 5*time.Second)
	require.Len(t, got, 8)
	require.LessOrEqual(t, peak.Load(), int32(2), "at most Workers stats run at once")
}

func TestProbeCacheCapResets(t *testing.T) {
	fake := newCountingLstat(map[string]time.Time{})
	mod := time.Now()
	paths := make([]string, probeCacheCap+10)
	for i := range paths {
		paths[i] = fmt.Sprintf("/p%05d", i)
		fake.times[paths[i]] = mod
	}
	// One worker processes the FIFO work queue in order, so the cache
	// fills to the cap, resets (nothing is expired), and ends holding
	// only the tail.
	p := NewProbe(ProbeOptions{Lstat: fake.lstat, Workers: 1})
	got := p.BatchRecency(context.Background(), paths, 30*time.Second)
	require.Len(t, got, len(paths), "the batch itself is complete")

	_ = p.BatchRecency(context.Background(), []string{paths[0], paths[len(paths)-1]}, 30*time.Second)
	require.Equal(t, 2, fake.count(paths[0]), "a reset-away entry is re-statted")
	require.Equal(t, 1, fake.count(paths[len(paths)-1]), "the tail survived the reset")
}

func TestBatchRecencyNilAndEmpty(t *testing.T) {
	var nilProbe *Probe
	require.Nil(t, nilProbe.BatchRecency(context.Background(), []string{"/a"}, time.Second))

	p := NewProbe(ProbeOptions{})
	require.Nil(t, p.BatchRecency(context.Background(), nil, time.Second))
}

func TestFileRecencyRealFileAtimeMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	mtime := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	atime := mtime.Add(48 * time.Hour)
	require.NoError(t, os.Chtimes(path, atime, mtime))

	fi, err := os.Lstat(path)
	require.NoError(t, err)
	got := fileRecency(fi)
	if runtime.GOOS == "linux" {
		require.WithinDuration(t, atime, got, time.Second,
			"linux takes the newer atime")
	} else {
		require.WithinDuration(t, mtime, got, time.Second,
			"off linux only mtime is consulted")
	}

	// Swapped stamps put the newer time in mtime, so every OS --
	// with or without atime support -- answers that newer stamp.
	require.NoError(t, os.Chtimes(path, mtime, atime))
	fi, err = os.Lstat(path)
	require.NoError(t, err)
	require.WithinDuration(t, atime, fileRecency(fi), time.Second)
}

func TestFileRecencyFallsBackToModTime(t *testing.T) {
	mod := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, mod, fileRecency(fakeInfo{mod: mod}),
		"a non-Stat_t Sys falls back to mtime")
}

func TestProbeRealLstatDefault(t *testing.T) {
	// NewProbe's default Lstat is os.Lstat: a probe with zero options
	// stats real files.
	path := filepath.Join(t.TempDir(), "real")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	p := NewProbe(ProbeOptions{})
	got := p.BatchRecency(context.Background(), []string{path}, 5*time.Second)
	require.False(t, got[path].IsZero())
}
