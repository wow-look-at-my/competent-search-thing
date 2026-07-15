package appctx

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSource is a scripted Source: results are settable under a
// mutex, calls are counted atomically, and the list methods can be
// made to block on a channel to test single-flight behavior.
type fakeSource struct {
	mu          sync.Mutex
	focused     AppInfo
	focusedOK   bool
	running     []AppInfo
	runningOK   bool
	installed   []InstalledApp
	installedOK bool

	blockRunning   chan struct{} // non-nil: RunningApps waits until closed
	blockInstalled chan struct{} // non-nil: InstalledApps waits until closed

	focusedCalls   atomic.Int32
	runningCalls   atomic.Int32
	installedCalls atomic.Int32
}

func (f *fakeSource) FocusedApp() (AppInfo, bool) {
	f.focusedCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.focused, f.focusedOK
}

func (f *fakeSource) RunningApps() ([]AppInfo, bool) {
	f.runningCalls.Add(1)
	f.mu.Lock()
	block := f.blockRunning
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.runningOK
}

func (f *fakeSource) InstalledApps() ([]InstalledApp, bool) {
	f.installedCalls.Add(1)
	f.mu.Lock()
	block := f.blockInstalled
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed, f.installedOK
}

func (f *fakeSource) set(fn func(*fakeSource)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// fakeClock is a mutex-guarded settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// waitIdle blocks until no refresh goroutine is in flight, i.e. every
// kicked refresh has stored its result.
func waitIdle(t *testing.T, c *Cache) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return !c.runningInFlight && !c.installedInFlight
	}, 5*time.Second, 2*time.Millisecond)
}

func TestCacheCaptureFocused(t *testing.T) {
	want := AppInfo{Name: "firefox", Exe: "/usr/bin/firefox", Title: "Mozilla Firefox", PID: 42}
	src := &fakeSource{focused: want, focusedOK: true}
	c := NewCache(src)

	require.Nil(t, c.Snapshot().Focused, "nothing captured yet")

	c.CaptureFocused()
	got := c.Snapshot().Focused
	require.NotNil(t, got)
	require.Equal(t, want, *got)
	require.EqualValues(t, 1, src.focusedCalls.Load(), "capture is synchronous, exactly one call")

	// A failed capture clears the previous one.
	src.set(func(f *fakeSource) { f.focusedOK = false })
	c.CaptureFocused()
	require.Nil(t, c.Snapshot().Focused)
}

func TestCacheRefreshRunningAsyncLands(t *testing.T) {
	src := &fakeSource{running: []AppInfo{{Name: "a", PID: 1}}, runningOK: true}
	c := NewCache(src)

	c.RefreshRunningAsync()
	require.Eventually(t, func() bool {
		return len(c.Snapshot().Running) == 1
	}, 5*time.Second, 2*time.Millisecond)
	require.Equal(t, "a", c.Snapshot().Running[0].Name)

	// A failed refresh keeps the previous list.
	src.set(func(f *fakeSource) { f.runningOK = false; f.running = nil })
	c.RefreshRunningAsync()
	waitIdle(t, c)
	require.EqualValues(t, 2, src.runningCalls.Load())
	require.Len(t, c.Snapshot().Running, 1, "failure keeps the old data")
}

func TestCacheRefreshInstalledAsyncLands(t *testing.T) {
	src := &fakeSource{installed: []InstalledApp{{Name: "b", ID: "b.desktop"}}, installedOK: true}
	c := NewCache(src)

	c.RefreshInstalledAsync()
	require.Eventually(t, func() bool {
		return len(c.Snapshot().Installed) == 1
	}, 5*time.Second, 2*time.Millisecond)

	src.set(func(f *fakeSource) { f.installedOK = false; f.installed = nil })
	c.RefreshInstalledAsync()
	waitIdle(t, c)
	require.EqualValues(t, 2, src.installedCalls.Load())
	require.Len(t, c.Snapshot().Installed, 1, "failure keeps the old data")
}

func TestCacheSingleFlight(t *testing.T) {
	block := make(chan struct{})
	src := &fakeSource{
		running:      []AppInfo{{Name: "x", PID: 9}},
		runningOK:    true,
		blockRunning: block,
	}
	c := NewCache(src)

	c.RefreshRunningAsync()
	require.Eventually(t, func() bool {
		return src.runningCalls.Load() == 1
	}, 5*time.Second, 2*time.Millisecond, "first refresh entered the source")

	// While the first refresh is blocked, further kicks are dropped.
	c.RefreshRunningAsync()
	c.RefreshRunningAsync()
	close(block)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.runningCalls.Load(), "in-flight refresh absorbed the extra kicks")
	require.Len(t, c.Snapshot().Running, 1)

	// After completion the next kick runs again.
	src.set(func(f *fakeSource) { f.blockRunning = nil })
	c.RefreshRunningAsync()
	waitIdle(t, c)
	require.EqualValues(t, 2, src.runningCalls.Load())
}

func TestCacheSingleFlightInstalled(t *testing.T) {
	block := make(chan struct{})
	src := &fakeSource{
		installed:      []InstalledApp{{Name: "x", ID: "x.desktop"}},
		installedOK:    true,
		blockInstalled: block,
	}
	c := NewCache(src)

	c.RefreshInstalledAsync()
	require.Eventually(t, func() bool {
		return src.installedCalls.Load() == 1
	}, 5*time.Second, 2*time.Millisecond)

	c.RefreshInstalledAsync()
	close(block)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load())
	require.Len(t, c.Snapshot().Installed, 1)
}

func TestCacheEnsureFreshInstalledTTL(t *testing.T) {
	const ttl = 5 * time.Minute
	src := &fakeSource{installed: []InstalledApp{{Name: "a", ID: "a.desktop"}}, installedOK: true}
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := NewCache(src)
	c.now = clk.now

	// Never refreshed: kicks immediately.
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load())

	// Younger than the TTL: no new refresh.
	clk.advance(ttl - time.Minute)
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load())

	// Older than the TTL: refreshes again.
	clk.advance(2 * time.Minute)
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 2, src.installedCalls.Load())
}

func TestCacheEnsureFreshInstalledRetriesAfterFailure(t *testing.T) {
	const ttl = 5 * time.Minute
	src := &fakeSource{installedOK: false}
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := NewCache(src)
	c.now = clk.now

	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load())

	// The failed refresh did not advance the freshness timestamp, so
	// the next Ensure retries even though no time has passed.
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 2, src.installedCalls.Load())

	// Once a refresh succeeds, freshness holds.
	src.set(func(f *fakeSource) { f.installedOK = true; f.installed = []InstalledApp{{Name: "a"}} })
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 3, src.installedCalls.Load())
	c.EnsureFreshInstalled(ttl)
	waitIdle(t, c)
	require.EqualValues(t, 3, src.installedCalls.Load(), "fresh after the successful refresh")
}

func TestCacheSnapshotImmutability(t *testing.T) {
	src := &fakeSource{
		focused: AppInfo{Name: "term", PID: 7}, focusedOK: true,
		running: []AppInfo{{Name: "a", PID: 1}}, runningOK: true,
		installed: []InstalledApp{{Name: "b", ID: "b.desktop"}}, installedOK: true,
	}
	c := NewCache(src)
	c.CaptureFocused()
	c.RefreshRunningAsync()
	c.RefreshInstalledAsync()
	waitIdle(t, c)

	s := c.Snapshot()
	require.NotNil(t, s.Focused)
	s.Focused.Name = "mutated"
	s.Running[0] = AppInfo{Name: "mutated"}
	s.Installed[0] = InstalledApp{Name: "mutated"}

	s2 := c.Snapshot()
	require.Equal(t, "term", s2.Focused.Name)
	require.Equal(t, []AppInfo{{Name: "a", PID: 1}}, s2.Running)
	require.Equal(t, []InstalledApp{{Name: "b", ID: "b.desktop"}}, s2.Installed)
}

func TestCacheNilAndZeroValueSafety(t *testing.T) {
	exercise := func(c *Cache) {
		t.Helper()
		c.CaptureFocused()
		c.RefreshRunningAsync()
		c.RefreshInstalledAsync()
		c.EnsureFreshInstalled(time.Minute)
		require.Equal(t, Snapshot{}, c.Snapshot())
	}

	exercise(nil)          // nil receiver
	exercise(&Cache{})     // zero value (nil source AND nil clock)
	exercise(NewCache(nil))
}

func TestCacheDefaultClock(t *testing.T) {
	// A Cache built without NewCache has no injected clock; the
	// time.Now fallback must kick in for the freshness bookkeeping.
	src := &fakeSource{installed: []InstalledApp{{Name: "a"}}, installedOK: true}
	c := &Cache{src: src}

	c.EnsureFreshInstalled(time.Hour)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load())

	c.EnsureFreshInstalled(time.Hour)
	waitIdle(t, c)
	require.EqualValues(t, 1, src.installedCalls.Load(), "real clock marked the refresh fresh")
}
