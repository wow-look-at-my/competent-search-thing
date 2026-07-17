package firefox

import (
	"context"
	"log"
	"sync"
	"time"
)

// Default tab-cache tuning, mirrored by internal/config's firefox
// defaults.
const (
	// DefaultTabTTL is how old a successful snapshot may get before a
	// query forces a full re-read even when the recovery file's mtime
	// looks unchanged (Firefox rewrites it about every 15 seconds
	// while running, so this backstop matches its own cadence).
	DefaultTabTTL = 15 * time.Second
	// tabProbeGap spaces the cheap mtime probes: while the user types,
	// at most one stat per gap, and a full decompress+parse only when
	// the mtime actually changed or the TTL expired.
	tabProbeGap = time.Second
)

// TabCacheOptions configures NewTabCache.
type TabCacheOptions struct {
	// ProfileDir is the profile directory holding the session
	// snapshot.
	ProfileDir string
	// TTL is the forced re-read window (non-positive = DefaultTabTTL).
	TTL time.Duration
	// Logf receives refresh failures (default log.Printf); repeats of
	// the same message are logged once, not per refresh.
	Logf func(format string, args ...any)

	// fetch, mtime and now are test seams: nil means ReadOpenTabs over
	// ProfileDir, RecoveryMTime over ProfileDir, and time.Now.
	fetch func(ctx context.Context) ([]Tab, error)
	mtime func() time.Time
	now   func() time.Time
}

// TabCache holds a goroutine-safe open-tabs snapshot so plugin queries
// never re-parse the session file per keystroke (firefox.Cache is the
// model). Tabs returns the current snapshot immediately; when a probe
// is due, ONE background refresh is kicked (single-flight) that first
// stats the recovery file and re-reads only when its mtime changed or
// the last read is older than the TTL. A failed refresh keeps the
// previous data and logs once per distinct message; every refresh
// goroutine is bounded by the constructor's context -- the app cancels
// it in Shutdown.
type TabCache struct {
	ctx   context.Context
	fetch func(ctx context.Context) ([]Tab, error)
	mtime func() time.Time
	ttl   time.Duration
	logf  func(format string, args ...any)
	now   func() time.Time

	mu        sync.Mutex
	tabs      []Tab
	inFlight  bool
	nextCheck time.Time // zero = probe on the next Tabs call
	haveRead  bool      // a fetch has succeeded at least once
	lastMTime time.Time // recovery-file mtime probed before that fetch
	lastRead  time.Time // when that fetch succeeded
	lastErr   string    // last logged failure (dedup)
}

// NewTabCache builds a TabCache over opt. ctx bounds every refresh
// goroutine (nil = never cancelled).
func NewTabCache(ctx context.Context, opt TabCacheOptions) *TabCache {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.TTL <= 0 {
		opt.TTL = DefaultTabTTL
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	if opt.mtime == nil {
		dir := opt.ProfileDir
		opt.mtime = func() time.Time { return RecoveryMTime(dir) }
	}
	if opt.fetch == nil {
		dir := opt.ProfileDir
		opt.fetch = func(ctx context.Context) ([]Tab, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return ReadOpenTabs(dir)
		}
	}
	return &TabCache{
		ctx:   ctx,
		fetch: opt.fetch,
		mtime: opt.mtime,
		ttl:   opt.TTL,
		logf:  opt.Logf,
		now:   opt.now,
	}
}

// Tabs returns a copy of the current snapshot and never blocks: the
// first call (and any call finding a probe due) kicks one background
// refresh, whose result shows up on a later call. A nil TabCache
// returns nothing.
func (c *TabCache) Tabs() []Tab {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	kick := !c.inFlight && !c.now().Before(c.nextCheck) && c.ctx.Err() == nil
	if kick {
		c.inFlight = true
	}
	out := append([]Tab(nil), c.tabs...)
	c.mu.Unlock()
	if kick {
		go c.refresh()
	}
	return out
}

// refresh probes the recovery file's mtime and, when it changed or the
// TTL expired, runs one fetch. Success replaces the snapshot (a
// missing file legitimately yields an EMPTY one: the browser is
// closed); failure keeps the old data, logs once per distinct message
// (never during shutdown), and retries no sooner than failureRetryGap.
func (c *TabCache) refresh() {
	mtime := c.mtime() // zero when the file is missing (browser closed)
	c.mu.Lock()
	if c.haveRead && mtime.Equal(c.lastMTime) && c.now().Sub(c.lastRead) < c.ttl {
		c.inFlight = false
		c.nextCheck = c.now().Add(tabProbeGap)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	tabs, err := c.fetch(c.ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight = false
	if err != nil {
		c.nextCheck = c.now().Add(failureRetryGap)
		if c.ctx.Err() != nil {
			return // shutdown cancelled the fetch: not an error worth noise
		}
		if msg := err.Error(); msg != c.lastErr {
			c.lastErr = msg
			c.logf("firefox: open tabs: %v", err)
		}
		return
	}
	c.lastErr = ""
	c.tabs = tabs
	c.haveRead = true
	c.lastMTime = mtime
	c.lastRead = c.now()
	c.nextCheck = c.now().Add(tabProbeGap)
}
