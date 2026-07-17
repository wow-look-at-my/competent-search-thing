package firefox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeMTime is an injectable, mutable mtime probe that counts calls.
type fakeMTime struct {
	mu    sync.Mutex
	t     time.Time
	calls int
}

func (m *fakeMTime) get() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.t
}

func (m *fakeMTime) set(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.t = t
}

func (m *fakeMTime) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// eventuallyTabs polls Tabs until it is non-empty.
func eventuallyTabs(t *testing.T, c *TabCache) []Tab {
	t.Helper()
	var got []Tab
	require.Eventually(t, func() bool {
		got = c.Tabs()
		return len(got) > 0
	}, 3*time.Second, 5*time.Millisecond)
	return got
}

// settleTabRefresh waits until no refresh goroutine is in flight.
func settleTabRefresh(t *testing.T, c *TabCache) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.inFlight
	}, 3*time.Second, time.Millisecond)
}

func TestTabCacheFirstQuerySingleFlight(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	c := NewTabCache(context.Background(), TabCacheOptions{
		now:   newFakeClock().now,
		mtime: (&fakeMTime{t: fixedNow}).get,
		fetch: func(context.Context) ([]Tab, error) {
			calls.Add(1)
			<-gate
			return []Tab{{URL: "https://a.example/", Host: "a.example"}}, nil
		},
	})
	require.Empty(t, c.Tabs(), "the first call returns immediately, empty")
	require.Empty(t, c.Tabs())
	require.Empty(t, c.Tabs())
	close(gate)
	got := eventuallyTabs(t, c)
	require.Equal(t, "a.example", got[0].Host)
	require.Equal(t, int32(1), calls.Load(), "concurrent queries share ONE refresh")
}

func TestTabCacheMTimeChangeTriggersReRead(t *testing.T) {
	clock := newFakeClock()
	mt := &fakeMTime{t: fixedNow.Add(-time.Minute)}
	var calls atomic.Int32
	c := NewTabCache(context.Background(), TabCacheOptions{
		TTL: time.Hour, // huge: only the mtime can trigger here
		now: clock.now, mtime: mt.get,
		fetch: func(context.Context) ([]Tab, error) {
			n := calls.Add(1)
			return []Tab{{URL: "https://v.example/", Host: "v.example", LastAccessed: int64(n)}}, nil
		},
	})
	got := eventuallyTabs(t, c)
	require.Equal(t, int64(1), got[0].LastAccessed)
	probes := mt.count()

	// Within the probe gap nothing happens at all -- not even a stat.
	for i := 0; i < 5; i++ {
		c.Tabs()
	}
	require.Equal(t, probes, mt.count(), "queries inside the probe gap never stat")
	require.Equal(t, int32(1), calls.Load())

	// Past the gap with an unchanged mtime: a stat, but no re-parse.
	clock.advance(2 * tabProbeGap)
	c.Tabs()
	require.Eventually(t, func() bool { return mt.count() > probes }, 3*time.Second, time.Millisecond)
	settleTabRefresh(t, c)
	require.Equal(t, int32(1), calls.Load(), "an unchanged file is not re-parsed")

	// The file changed: the next due probe re-reads.
	mt.set(fixedNow)
	clock.advance(2 * tabProbeGap)
	c.Tabs()
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 3*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		tabs := c.Tabs()
		return len(tabs) == 1 && tabs[0].LastAccessed == 2
	}, 3*time.Second, time.Millisecond)
}

func TestTabCacheTTLForcesReRead(t *testing.T) {
	clock := newFakeClock()
	var calls atomic.Int32
	c := NewTabCache(context.Background(), TabCacheOptions{
		TTL: 15 * time.Second,
		now: clock.now,
		mtime: (&fakeMTime{t: fixedNow}).get, // never changes
		fetch: func(context.Context) ([]Tab, error) {
			calls.Add(1)
			return []Tab{{URL: "https://t.example/", Host: "t.example"}}, nil
		},
	})
	eventuallyTabs(t, c)
	require.Equal(t, int32(1), calls.Load())

	clock.advance(2 * time.Second) // past the probe gap, inside the TTL
	c.Tabs()
	settleTabRefresh(t, c)
	require.Equal(t, int32(1), calls.Load())

	clock.advance(14 * time.Second) // 16s since the read: past the TTL
	c.Tabs()
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 3*time.Second, time.Millisecond)
}

func TestTabCacheFailureKeepsDataAndLogsOnce(t *testing.T) {
	clock := newFakeClock()
	mt := &fakeMTime{t: fixedNow.Add(-time.Minute)}
	lg := &captureLog{}
	var fail atomic.Bool
	var calls atomic.Int32
	c := NewTabCache(context.Background(), TabCacheOptions{
		TTL: time.Hour, Logf: lg.logf,
		now: clock.now, mtime: mt.get,
		fetch: func(context.Context) ([]Tab, error) {
			calls.Add(1)
			if fail.Load() {
				return nil, errors.New("snapshot went sideways")
			}
			return []Tab{{URL: "https://ok.example/", Host: "ok.example"}}, nil
		},
	})
	eventuallyTabs(t, c)
	require.Equal(t, int32(1), calls.Load())

	fail.Store(true)
	mt.set(fixedNow) // the file changed, so the next probe re-reads...
	clock.advance(2 * tabProbeGap)
	c.Tabs()
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 3*time.Second, time.Millisecond)
	require.Equal(t, "ok.example", c.Tabs()[0].Host, "old data survives a failed refresh")
	require.Eventually(t, func() bool { return len(lg.all()) == 1 }, 3*time.Second, time.Millisecond)
	require.Contains(t, lg.all()[0], "snapshot went sideways")

	// Within the failure-retry gap nothing is re-attempted...
	clock.advance(failureRetryGap / 2)
	for i := 0; i < 5; i++ {
		c.Tabs()
	}
	require.Equal(t, int32(2), calls.Load(), "a failure is not retried on every keystroke")

	// ...after it the retry runs, and the identical error stays quiet.
	clock.advance(failureRetryGap)
	c.Tabs()
	require.Eventually(t, func() bool { return calls.Load() == 3 }, 3*time.Second, time.Millisecond)
	settleTabRefresh(t, c)
	require.Len(t, lg.all(), 1, "the same message is logged once, not per refresh")

	// Recovery replaces the snapshot and resets the dedup.
	fail.Store(false)
	clock.advance(failureRetryGap)
	c.Tabs()
	require.Eventually(t, func() bool { return calls.Load() == 4 }, 3*time.Second, time.Millisecond)
	settleTabRefresh(t, c)
	fail.Store(true)
	mt.set(fixedNow.Add(time.Hour))
	clock.advance(2 * tabProbeGap)
	c.Tabs()
	require.Eventually(t, func() bool { return len(lg.all()) == 2 }, 3*time.Second, time.Millisecond)
}

func TestTabCacheContextCancelStopsRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lg := &captureLog{}
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	c := NewTabCache(ctx, TabCacheOptions{
		Logf: lg.logf,
		now:  newFakeClock().now,
		mtime: (&fakeMTime{t: fixedNow}).get,
		fetch: func(fctx context.Context) ([]Tab, error) {
			calls.Add(1)
			started <- struct{}{}
			<-fctx.Done()
			return nil, fctx.Err()
		},
	})
	c.Tabs()
	<-started
	cancel()

	settleTabRefresh(t, c)
	require.Empty(t, lg.all(), "shutdown cancellation is not an error")
	for i := 0; i < 3; i++ {
		require.Empty(t, c.Tabs())
	}
	require.Equal(t, int32(1), calls.Load(), "a cancelled cache never starts another refresh")
}

func TestTabCacheSnapshotIsACopy(t *testing.T) {
	c := NewTabCache(context.Background(), TabCacheOptions{
		now:   newFakeClock().now,
		mtime: (&fakeMTime{t: fixedNow}).get,
		fetch: func(context.Context) ([]Tab, error) {
			return []Tab{{URL: "https://a.example/", Host: "a.example"}}, nil
		},
	})
	got := eventuallyTabs(t, c)
	got[0].Host = "mutated"
	require.Equal(t, "a.example", c.Tabs()[0].Host, "callers cannot mutate the cache")
}

func TestTabCacheNilSafe(t *testing.T) {
	var c *TabCache
	require.Nil(t, c.Tabs())
}

func TestTabCacheProductionSeams(t *testing.T) {
	dir := t.TempDir()
	writeRecovery(t, dir, sessionFile{Windows: []sessionWindow{window(
		tab(1, entry("https://real.example/", "Real")),
	)}})
	c := NewTabCache(context.Background(), TabCacheOptions{
		ProfileDir: dir,
		// TTL/Logf/now/fetch/mtime all defaulted: this covers the
		// production wiring end to end.
	})
	got := eventuallyTabs(t, c)
	require.Equal(t, "real.example", got[0].Host)
	require.Equal(t, "Real", got[0].Title)
	require.Equal(t, DefaultTabTTL, c.ttl)
}

func TestTabCacheProductionMissingFileIsEmptySuccess(t *testing.T) {
	lg := &captureLog{}
	c := NewTabCache(context.Background(), TabCacheOptions{
		ProfileDir: t.TempDir(), // no snapshot: the browser is "closed"
		Logf:       lg.logf,
	})
	require.Empty(t, c.Tabs())
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.haveRead && !c.inFlight
	}, 3*time.Second, time.Millisecond)
	require.Empty(t, c.Tabs(), "no recovery snapshot = no open tabs")
	require.Empty(t, lg.all(), "a closed browser is a state, not an error")
}
