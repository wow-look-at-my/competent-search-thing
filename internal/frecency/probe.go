package frecency

import (
	"context"
	"os"
	"sync"
	"time"
)

// Probe defaults.
const (
	// DefaultProbeTTL is how long a statted recency stays fresh:
	// repeat queries over the same top results re-stat nothing for 5
	// minutes.
	DefaultProbeTTL = 5 * time.Minute
	// DefaultProbeWorkers bounds the concurrent stats of one batch.
	DefaultProbeWorkers = 8
	// probeCacheCap bounds the TTL cache; past it expired entries are
	// swept, and a still-full cache is reset (it is only a cache --
	// refilling costs a few stats).
	probeCacheCap = 4096
)

// ProbeOptions configures a Probe. Zero values mean: TTL
// DefaultProbeTTL, Lstat os.Lstat, Now time.Now, Workers
// DefaultProbeWorkers.
type ProbeOptions struct {
	TTL     time.Duration
	Lstat   func(string) (os.FileInfo, error)
	Now     func() time.Time
	Workers int
}

// Probe answers "how recently was this path touched?" (max of atime
// and mtime on Linux; plain mtime elsewhere -- see the package doc's
// relatime caveat) with a TTL cache in front of the stats, bounded
// concurrency, and a hard per-batch time budget. Safe for concurrent
// use; a nil *Probe no-ops.
type Probe struct {
	mu    sync.Mutex
	opts  ProbeOptions
	cache map[string]probeCacheEntry
}

type probeCacheEntry struct {
	recency time.Time // zero = the stat failed (cached negatively)
	checked time.Time
}

// NewProbe creates a probe; ProbeOptions zero values are replaced by
// the documented defaults.
func NewProbe(opts ProbeOptions) *Probe {
	if opts.TTL <= 0 {
		opts.TTL = DefaultProbeTTL
	}
	if opts.Lstat == nil {
		opts.Lstat = os.Lstat
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Workers <= 0 {
		opts.Workers = DefaultProbeWorkers
	}
	return &Probe{opts: opts, cache: map[string]probeCacheEntry{}}
}

type statResult struct {
	path    string
	recency time.Time
}

// BatchRecency returns each path's recency (max atime/mtime; see
// above). Paths fresh in the TTL cache cost nothing; the rest are
// statted by at most Workers goroutines, and the call NEVER blocks
// past budget (wall clock) or ctx cancellation: paths not statted in
// time are simply absent from the map -- a lookup yields the zero
// time, meaning "no signal". Stragglers keep running (each finishes
// its single stat) and land in the cache, so the next query gets them
// free. Failed stats are cached negatively for a TTL and also come
// back as the zero time. A nil probe returns nil.
func (p *Probe) BatchRecency(ctx context.Context, paths []string, budget time.Duration) map[string]time.Time {
	if p == nil || len(paths) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(paths))
	toStat := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	p.mu.Lock()
	now := p.opts.Now()
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		if e, ok := p.cache[path]; ok && now.Sub(e.checked) < p.opts.TTL {
			if !e.recency.IsZero() {
				out[path] = e.recency
			}
			continue
		}
		toStat = append(toStat, path)
	}
	p.mu.Unlock()
	if len(toStat) == 0 {
		return out
	}

	// Buffered channels sized to the whole batch: an abandoned
	// straggler's send never blocks, so every worker goroutine is
	// guaranteed to finish one stat after its batch returned.
	work := make(chan string, len(toStat))
	for _, path := range toStat {
		work <- path
	}
	close(work)
	results := make(chan statResult, len(toStat))
	workers := p.opts.Workers
	if workers > len(toStat) {
		workers = len(toStat)
	}
	for i := 0; i < workers; i++ {
		go func() {
			for path := range work {
				var rec time.Time
				if fi, err := p.opts.Lstat(path); err == nil {
					rec = fileRecency(fi)
				}
				p.storeCached(path, rec)
				results <- statResult{path: path, recency: rec}
			}
		}()
	}

	if budget < 0 {
		budget = 0
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for received := 0; received < len(toStat); {
		select {
		case r := <-results:
			received++
			if !r.recency.IsZero() {
				out[r.path] = r.recency
			}
		case <-timer.C:
			return out
		case <-ctx.Done():
			return out
		}
	}
	return out
}

// storeCached records one stat outcome, keeping the cache bounded:
// past probeCacheCap the expired entries are swept first, and a cache
// that is somehow still full is reset outright.
func (p *Probe) storeCached(path string, rec time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.opts.Now()
	if len(p.cache) >= probeCacheCap {
		for k, e := range p.cache {
			if now.Sub(e.checked) >= p.opts.TTL {
				delete(p.cache, k)
			}
		}
		if len(p.cache) >= probeCacheCap {
			p.cache = map[string]probeCacheEntry{}
		}
	}
	p.cache[path] = probeCacheEntry{recency: rec, checked: now}
}
