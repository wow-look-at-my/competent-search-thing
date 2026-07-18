package firefox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is an injectable, manually-advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: fixedNow} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// captureLog is a goroutine-safe Logf sink.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *captureLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// eventuallySites polls Sites until it is non-empty.
func eventuallySites(t *testing.T, c *Cache) []Site {
	t.Helper()
	var got []Site
	require.Eventually(t, func() bool {
		got = c.Sites()
		return len(got) > 0
	}, 3*time.Second, 5*time.Millisecond)
	return got
}

// settleCacheRefresh waits until no refresh goroutine is in flight
// (settleTabRefresh's sibling): a counter observed inside the fetch
// seam says nothing about the refresh's locked bookkeeping, which is
// what the next kick and the next clock advance depend on.
func settleCacheRefresh(t *testing.T, c *Cache) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.inFlight
	}, 3*time.Second, time.Millisecond)
}

func TestCacheFirstQuerySingleFlight(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	c := NewCache(context.Background(), CacheOptions{
		TTL: time.Hour,
		now: newFakeClock().now,
		fetch: func(context.Context) ([]Site, error) {
			calls.Add(1)
			<-gate
			return []Site{{URL: "https://a.example/", Host: "a.example", Visits: 12}}, nil
		},
	})
	require.Empty(t, c.Sites(), "the first call returns immediately, empty")
	require.Empty(t, c.Sites())
	require.Empty(t, c.Sites())
	close(gate)
	got := eventuallySites(t, c)
	require.Equal(t, "a.example", got[0].Host)
	require.Equal(t, int32(1), calls.Load(), "concurrent queries share ONE refresh")
}

func TestCacheTTLGovernsRefresh(t *testing.T) {
	clock := newFakeClock()
	var calls atomic.Int32
	c := NewCache(context.Background(), CacheOptions{
		TTL: 10 * time.Minute,
		now: clock.now,
		fetch: func(context.Context) ([]Site, error) {
			n := calls.Add(1)
			return []Site{{URL: "https://v.example/", Host: "v.example", Visits: int(n)}}, nil
		},
	})
	got := eventuallySites(t, c)
	require.Equal(t, 1, got[0].Visits)

	clock.advance(9 * time.Minute)
	for i := 0; i < 5; i++ {
		c.Sites()
	}
	require.Equal(t, int32(1), calls.Load(), "fresh data is served without re-reading")

	clock.advance(2 * time.Minute) // past the TTL
	c.Sites()                      // kicks refresh #2
	require.Eventually(t, func() bool { return c.Sites()[0].Visits == 2 },
		3*time.Second, 5*time.Millisecond)
	require.Equal(t, int32(2), calls.Load())
}

func TestCacheFailureKeepsDataAndLogsOnce(t *testing.T) {
	clock := newFakeClock()
	lg := &captureLog{}
	var fail atomic.Bool
	var calls atomic.Int32
	c := NewCache(context.Background(), CacheOptions{
		TTL:  10 * time.Minute,
		Logf: lg.logf,
		now:  clock.now,
		fetch: func(context.Context) ([]Site, error) {
			// The outcome is decided BEFORE the observable increment:
			// the test flips fail as soon as the counter moves, and a
			// still-deciding refresh must not pick the new value up
			// retroactively.
			failing := fail.Load()
			calls.Add(1)
			if failing {
				return nil, errors.New("database is sulking")
			}
			return []Site{{URL: "https://ok.example/", Host: "ok.example", Visits: 12}}, nil
		},
	})
	eventuallySites(t, c)
	require.Equal(t, int32(1), calls.Load())

	// Each phase must settle the previous refresh's BOOKKEEPING before
	// advancing the clock and kicking again: a bare counter poll
	// returns mid-fetch, and a refresh goroutine parked between its
	// fetch and its locked bookkeeping would (a) swallow the next kick
	// via the in-flight flag -- nothing ever re-kicks, Sites is the
	// only kicker -- and (b) compute its retry time from the ALREADY
	// ADVANCED fake clock, pushing the next attempt out of the frozen
	// clock's reach for good. The phases whose refresh writes a log
	// line are settled by the log-count wait (written in the same
	// locked section); the quiet ones use settleCacheRefresh, exactly
	// like the tab cache test.
	fail.Store(true)
	clock.advance(11 * time.Minute)
	c.Sites() // kicks the failing refresh
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 3*time.Second, 5*time.Millisecond)
	require.Equal(t, "ok.example", c.Sites()[0].Host, "old data survives a failed refresh")
	require.Eventually(t, func() bool { return len(lg.all()) == 1 }, 3*time.Second, 5*time.Millisecond)
	require.Contains(t, lg.all()[0], "database is sulking")

	// Within the failure-retry gap nothing is re-attempted...
	clock.advance(failureRetryGap / 2)
	for i := 0; i < 5; i++ {
		c.Sites()
	}
	require.Equal(t, int32(2), calls.Load(), "a failure is not retried on every keystroke")

	// ...after it the retry runs, and the identical error stays quiet.
	// (The settle discipline above guarantees no refresh is in flight
	// here, so a lone Sites() kick cannot be swallowed by the
	// single-flight latch.)
	clock.advance(failureRetryGap)
	c.Sites()
	require.Eventually(t, func() bool { return calls.Load() == 3 }, 3*time.Second, 5*time.Millisecond)
	settleCacheRefresh(t, c) // the identical error logs nothing to wait on
	require.Len(t, lg.all(), 1, "the same message is logged once, not per refresh")

	// Recovery resets the dedup, so a NEW round of failures logs again.
	fail.Store(false)
	clock.advance(failureRetryGap)
	c.Sites()
	require.Eventually(t, func() bool { return calls.Load() == 4 }, 3*time.Second, 5*time.Millisecond)
	settleCacheRefresh(t, c)
	fail.Store(true)
	clock.advance(11 * time.Minute)
	require.Eventually(t, func() bool { c.Sites(); return len(lg.all()) == 2 }, 3*time.Second, 5*time.Millisecond)
}

func TestCacheContextCancelStopsRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lg := &captureLog{}
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	c := NewCache(ctx, CacheOptions{
		TTL:  time.Hour,
		Logf: lg.logf,
		now:  newFakeClock().now,
		fetch: func(fctx context.Context) ([]Site, error) {
			calls.Add(1)
			started <- struct{}{}
			<-fctx.Done() // an in-flight copy/query aborts with the ctx
			return nil, fctx.Err()
		},
	})
	c.Sites()
	<-started
	cancel()

	// The in-flight refresh unwinds quietly (no log), and no new
	// refresh is ever kicked.
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.inFlight
	}, 3*time.Second, 5*time.Millisecond)
	require.Empty(t, lg.all(), "shutdown cancellation is not an error")
	for i := 0; i < 3; i++ {
		require.Empty(t, c.Sites())
	}
	require.Equal(t, int32(1), calls.Load(), "a cancelled cache never starts another refresh")
}

func TestCacheSnapshotIsACopy(t *testing.T) {
	c := NewCache(context.Background(), CacheOptions{
		TTL: time.Hour,
		now: newFakeClock().now,
		fetch: func(context.Context) ([]Site, error) {
			return []Site{{URL: "https://a.example/", Host: "a.example"}}, nil
		},
	})
	got := eventuallySites(t, c)
	got[0].Host = "mutated"
	require.Equal(t, "a.example", c.Sites()[0].Host, "callers cannot mutate the cache")
}

func TestCacheNilSafe(t *testing.T) {
	var c *Cache
	require.Nil(t, c.Sites())
}

func TestCacheProductionFetch(t *testing.T) {
	dir := t.TempDir()
	buildPlaces(t, dir, []page{
		{url: "https://real.example/", title: "Real", visits: visitsAt(time.Now().Add(-time.Hour), 12)},
	})
	c := NewCache(context.Background(), CacheOptions{
		ProfileDir: dir,
		MinMonth:   11,
		MinWeek:    1,
		// TTL/Logf/now/fetch all defaulted: this covers the production
		// wiring end to end.
	})
	got := eventuallySites(t, c)
	require.Equal(t, "real.example", got[0].Host)
	require.Equal(t, 12, got[0].Visits)
	require.Equal(t, DefaultTTL, c.ttl)
}
