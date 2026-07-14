package watch

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// defaultMinGap spaces out degradation-requested rescans so an overflow
// storm cannot cause back-to-back full disk walks.
const defaultMinGap = 30 * time.Second

// RescanOptions tunes a Rescanner. The zero value disables the periodic
// ticker and uses the default request spacing.
type RescanOptions struct {
	// Interval between periodic full rescans; 0 disables the ticker
	// (requested rescans still run). Wire config.RescanIntervalMinutes
	// here.
	Interval time.Duration
	// MinGap is the minimum spacing between REQUESTED rescans, measured
	// from the previous rescan's end (default 30s). Periodic rescans
	// are already spaced by Interval.
	MinGap time.Duration
}

// RescanStats is a snapshot of the Rescanner for logs and the UI.
type RescanStats struct {
	// Completed counts successful rescans (fresh store swapped in).
	Completed int
	// Failed counts rescans that returned an error; the previous index
	// is kept on failure.
	Failed int
	// Running is true while a rescan is in flight.
	Running bool
}

// Rescanner owns "the index may be stale" recovery: a full
// Manager.BuildFromDisk -- which walks into a fresh store and swaps it
// in, so queries never block -- followed by a resync of the Watcher's
// watch set. It fires from an optional periodic ticker and from
// one-shot requests (the Watcher requests one when it degrades).
//
// Rescans are serialized by construction: the single loop goroutine
// runs them one at a time, and the 1-slot request channel coalesces any
// triggers that arrive mid-rescan into at most one follow-up.
type Rescanner struct {
	mgr *index.Manager
	w   *Watcher
	opt RescanOptions

	requests chan struct{}
	lc       lifecycle

	statsMu sync.Mutex
	stats   RescanStats
	lastEnd time.Time
}

// NewRescanner wires a Rescanner to the Manager and, optionally, a
// Watcher: the Watcher's degradation requests are routed to Request,
// and every successful rescan resyncs the Watcher's watch set. Create
// the Watcher first, then the Rescanner, then Start both.
func NewRescanner(m *index.Manager, w *Watcher, opt RescanOptions) *Rescanner {
	if opt.MinGap <= 0 {
		opt.MinGap = defaultMinGap
	}
	r := &Rescanner{
		mgr:      m,
		w:        w,
		opt:      opt,
		requests: make(chan struct{}, 1),
	}
	if w != nil {
		w.setRescanRequester(r.Request)
	}
	return r
}

// Request asks for one reconcile rescan as soon as the spacing rules
// allow. It never blocks; requests arriving while one is already queued
// or running coalesce into a single follow-up rescan.
func (r *Rescanner) Request() {
	select {
	case r.requests <- struct{}{}:
	default:
	}
}

// Start launches the rescan loop. It fails if the Rescanner was already
// started or stopped.
func (r *Rescanner) Start() error {
	ctx, err := r.lc.begin()
	if err != nil {
		return err
	}
	go r.run(ctx)
	return nil
}

// Stop cancels the loop -- aborting any in-flight BuildFromDisk, whose
// error path keeps the previous store -- and blocks until it exits.
// Idempotent and safe before Start.
func (r *Rescanner) Stop() { r.lc.end() }

// Stats returns a snapshot of the Rescanner's activity.
func (r *Rescanner) Stats() RescanStats {
	r.statsMu.Lock()
	defer r.statsMu.Unlock()
	return r.stats
}

func (r *Rescanner) run(ctx context.Context) {
	defer close(r.lc.done)
	var tick <-chan time.Time
	if r.opt.Interval > 0 {
		t := time.NewTicker(r.opt.Interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			r.rescan(ctx)
		case <-r.requests:
			if !r.waitMinGap(ctx) {
				return
			}
			r.rescan(ctx)
		}
	}
}

// waitMinGap sleeps until MinGap has passed since the previous rescan
// finished. It returns false when the context is cancelled while
// waiting.
func (r *Rescanner) waitMinGap(ctx context.Context) bool {
	r.statsMu.Lock()
	last := r.lastEnd
	r.statsMu.Unlock()
	if last.IsZero() {
		return true
	}
	wait := time.Until(last.Add(r.opt.MinGap))
	if wait <= 0 {
		return true
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// rescan runs one full rebuild and, on success, resyncs the watch set
// to the fresh store's live directories.
func (r *Rescanner) rescan(ctx context.Context) {
	r.statsMu.Lock()
	r.stats.Running = true
	r.statsMu.Unlock()

	count, dur, err := r.mgr.BuildFromDisk(ctx, nil)

	r.statsMu.Lock()
	r.stats.Running = false
	r.lastEnd = time.Now()
	if err != nil {
		r.stats.Failed++
	} else {
		r.stats.Completed++
	}
	r.statsMu.Unlock()

	if err != nil {
		log.Printf("watch: rescan failed (previous index kept): %v", err)
		return
	}
	if r.w != nil {
		r.w.syncWatches()
	}
	log.Printf("watch: rescan complete: %d entries in %s", count, dur.Round(time.Millisecond))
}
