package firefox

import (
	"context"
	"log"
	"sync"
	"time"
)

// Default cache tuning, mirrored by internal/config's firefox
// defaults.
const (
	// DefaultTTL is how old a successful snapshot may get before a
	// query kicks a background re-read.
	DefaultTTL = 10 * time.Minute
	// failureRetryGap spaces refresh attempts after a failure, so a
	// broken profile (missing db, corrupt copy) is not re-copied on
	// every keystroke.
	failureRetryGap = time.Minute
)

// CacheOptions configures NewCache.
type CacheOptions struct {
	// ProfileDir is the profile directory holding places.sqlite.
	ProfileDir string
	// MinMonth / MinWeek are the frequency thresholds (QueryOptions).
	MinMonth int
	MinWeek  int
	// TTL is the snapshot's freshness window (non-positive =
	// DefaultTTL).
	TTL time.Duration
	// Logf receives refresh failures (default log.Printf); repeats of
	// the same message are logged once, not per refresh.
	Logf func(format string, args ...any)

	// fetch and now are test seams: nil means FrequentSites over
	// ProfileDir and time.Now.
	fetch func(ctx context.Context) ([]Site, error)
	now   func() time.Time
}

// Cache holds a goroutine-safe frequent-sites snapshot so plugin
// queries never block on the history database (internal/appctx.Cache
// is the model). Sites returns the current snapshot immediately; when
// it is stale, ONE background refresh is kicked (single-flight), a
// failed refresh keeps the previous data, and every refresh goroutine
// is bounded by the constructor's context -- the app cancels it in
// Shutdown, so quit never waits on a history copy.
type Cache struct {
	ctx   context.Context
	fetch func(ctx context.Context) ([]Site, error)
	ttl   time.Duration
	logf  func(format string, args ...any)
	now   func() time.Time

	mu          sync.Mutex
	sites       []Site
	inFlight    bool
	nextAttempt time.Time // zero = refresh on the next Sites call
	lastErr     string    // last logged failure (dedup)
}

// NewCache builds a Cache over opt. ctx bounds every refresh
// goroutine (nil = never cancelled).
func NewCache(ctx context.Context, opt CacheOptions) *Cache {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.TTL <= 0 {
		opt.TTL = DefaultTTL
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	if opt.fetch == nil {
		dir, minMonth, minWeek := opt.ProfileDir, opt.MinMonth, opt.MinWeek
		clock := opt.now
		opt.fetch = func(ctx context.Context) ([]Site, error) {
			return FrequentSites(ctx, dir, QueryOptions{
				MinMonth: minMonth, MinWeek: minWeek, Now: clock(),
			})
		}
	}
	return &Cache{
		ctx:   ctx,
		fetch: opt.fetch,
		ttl:   opt.TTL,
		logf:  opt.Logf,
		now:   opt.now,
	}
}

// Sites returns a copy of the current snapshot and never blocks: the
// first call (and any call finding the snapshot stale) kicks one
// background refresh, whose result shows up on a later call. A nil
// Cache returns nothing.
func (c *Cache) Sites() []Site {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	kick := !c.inFlight && !c.now().Before(c.nextAttempt) && c.ctx.Err() == nil
	if kick {
		c.inFlight = true
	}
	out := append([]Site(nil), c.sites...)
	c.mu.Unlock()
	if kick {
		go c.refresh()
	}
	return out
}

// refresh runs one fetch and stores the outcome: success replaces the
// snapshot and schedules the next refresh a TTL away; failure keeps
// the old data, logs once per distinct message (never during
// shutdown), and retries no sooner than failureRetryGap.
func (c *Cache) refresh() {
	sites, err := c.fetch(c.ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight = false
	if err != nil {
		c.nextAttempt = c.now().Add(failureRetryGap)
		if c.ctx.Err() != nil {
			return // shutdown cancelled the fetch: not an error worth noise
		}
		if msg := err.Error(); msg != c.lastErr {
			c.lastErr = msg
			c.logf("firefox: frequent sites: %v", err)
		}
		return
	}
	c.lastErr = ""
	c.sites = sites
	c.nextAttempt = c.now().Add(c.ttl)
}
