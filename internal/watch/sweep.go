package watch

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// Sweep defaults; see SweepOptions.
const (
	defaultSweepInterval    = 20 * time.Minute
	defaultSweepMinGap      = time.Minute
	defaultSweepStatsPerSec = 50000
	// sweepPage is the LiveDirsPage chunk size: the Manager's read
	// lock is released between pages.
	sweepPage = 4096
	// sweepMtimeSlack widens the watermark window: a directory whose
	// mtime is within this slack of the previous pass's start is
	// re-listed anyway, absorbing coarse filesystem timestamp
	// granularity and small skew between the sweep's clock and the
	// filesystem's.
	sweepMtimeSlack = 2 * time.Second
	// defaultCompactRatio and defaultCompactMinEntries gate the
	// compaction rebuild (see Sweeper.maybeCompact). Removals only set
	// a tombstone bit -- the name bytes, the offset/parent/flag columns
	// and the children slot are reclaimed only by a rebuild into a
	// fresh store -- so without this the index grows forever on a
	// machine that churns DISTINCT file names (build artifacts,
	// package installs, temp files). Re-creating the SAME name
	// resurrects its entry, so steady-state churn over a stable name
	// set costs nothing and never trips this.
	//
	// The gates are deliberately high: a rebuild re-walks the disk, and
	// after one the ratio resets to zero, so reaching the threshold
	// again takes another 30% of the index being deleted. That makes
	// the trigger self-limiting rather than periodic.
	defaultCompactRatio      = 0.30
	defaultCompactMinEntries = 20000
)

// SweepOptions tunes a Sweeper. The zero value selects all defaults.
type SweepOptions struct {
	// Interval between periodic sweep passes (default 20m; 0 selects
	// the default -- the sweep tier is always on; an explicit disable
	// knob arrives with the config wiring in a later stage).
	Interval time.Duration
	// MinGap is the minimum spacing between REQUESTED sweeps, measured
	// from the previous pass's end (default 1m), exactly like the
	// Rescanner's. Periodic passes are already spaced by Interval.
	MinGap time.Duration
	// InitialWatermark seeds the mtime cutoff of the first pass. The
	// zero value makes the first pass re-list EVERY indexed directory
	// -- correct but expensive; the app passes the initial build's
	// completion time, which the just-finished walk vouches for.
	InitialWatermark time.Time
	// StatsPerSec caps the per-directory lstat rate of a pass (default
	// 50000): the sweep is a background janitor and must never
	// monopolize the disk.
	StatsPerSec int

	// CompactRatio is the tombstone fraction past which a completed
	// pass asks the Rescanner for a compaction rebuild, and
	// CompactMinEntries the store size below which it never bothers
	// (defaults 0.30 and 20000; <= 0 selects the default). A negative
	// CompactRatio disables the check entirely. Nothing happens
	// without a Rescanner wired.
	CompactRatio      float64
	CompactMinEntries int

	// mounts lists the current mount table's mountpoints (real,
	// walkable filesystems only -- virtual and network types never
	// belong in the index). The default reads /proc/self/mounts on
	// linux, scoped to the configured roots via index.RealMountpoints,
	// and returns nil elsewhere; tests script it.
	mounts func() []string
	// mountSkips lists unsafe virtual, network, and FUSE mountpoints
	// below the roots. Tracking them separately lets a transition from
	// a local mount to an unsafe one remove stale indexed content
	// without ever reading the unsafe filesystem; its later departure
	// force-reconciles the local directory exposed underneath.
	mountSkips func() []string
}

// SweepStats is a snapshot of the Sweeper for logs and the UI.
type SweepStats struct {
	// Completed counts passes that ran to the end; Cancelled counts
	// passes cut short (Stop).
	Completed int
	Cancelled int
	// Running is true while a pass is in flight.
	Running bool
	// LastStart is when the most recent pass began; LastDuration how
	// long it ran.
	LastStart    time.Time
	LastDuration time.Duration
	// Swept counts the directories the last pass examined (lstat);
	// Relisted the subset that triggered a shallow reconcile (mtime
	// past the watermark, vanished on disk, or mount-diff
	// force-dirty).
	Swept    int
	Relisted int
}

// Sweeper is the always-on convergence tier: on a cadence (and on
// request, e.g. after an event-queue overflow) it walks every live
// indexed directory in pages, lstats each one, and shallow-reconciles
// -- through the Watcher's reconcile engine -- the ones whose mtime
// moved past the watermark of the previous completed pass, plus the
// ones that vanished. Mountpoints appearing, vanishing, or changing
// between walkable and unsafe under the roots since the previous pass
// are force-converged regardless of mtime (a mount onto an existing
// directory moves no mtime, and an unmount restores content the index
// never saw change). Unsafe arrivals are tombstoned without filesystem
// reads. Directories the hot set does not watch therefore converge
// within one sweep interval; tiers differ only in latency, never in
// final state.
//
// Known documented limit: an mtime-BACKDATED mutation (e.g. tar
// --preserve into an existing directory) hides from the incremental
// watermark check and converges only at a full re-list (a fresh
// zero-watermark sweeper), a Rescanner rebuild, or a manual !rescan.
type Sweeper struct {
	mgr *index.Manager
	w   *Watcher
	opt SweepOptions

	roots   []string            // normalized configured roots (swept first; mount-diff scope)
	rootSet map[string]struct{} // the same roots for O(1) membership

	requests chan struct{}
	lc       lifecycle

	statsMu sync.Mutex
	stats   SweepStats
	lastEnd time.Time

	// Run-loop-owned state (single goroutine, no lock): the mtime
	// watermark -- advanced to a pass's start time ONLY when that pass
	// ran to completion, so a cancelled/partial pass leaves the window
	// untouched and the next pass redoes it -- and the previous pass's
	// classified mount snapshot for the change diff.
	watermark  time.Time
	prevMounts map[string]mountState
}

type mountState uint8

const (
	mountWalkable mountState = iota + 1
	mountUnsafe
)

// NewSweeper wires a Sweeper to the Manager and the Watcher. w must
// NOT be nil: the Watcher's reconcile is the engine every sweep
// finding is applied through (it works fine even when the Watcher's
// notifier failed -- reconciling needs no watches, and reconcileDir's
// refreshWatch doubles as the promotion of freshly-changed dirs into
// the hot set). The Watcher's overflow handling is rewired to Request
// (see handleError). Create the Watcher first, then the Sweeper, then
// Start both; stop the Sweeper BEFORE the Watcher.
func NewSweeper(m *index.Manager, w *Watcher, opt SweepOptions) *Sweeper {
	if w == nil {
		panic("watch: NewSweeper requires a Watcher (the reconcile engine)")
	}
	if opt.Interval <= 0 {
		opt.Interval = defaultSweepInterval
	}
	if opt.MinGap <= 0 {
		opt.MinGap = defaultSweepMinGap
	}
	if opt.StatsPerSec <= 0 {
		opt.StatsPerSec = defaultSweepStatsPerSec
	}
	if opt.CompactRatio == 0 {
		opt.CompactRatio = defaultCompactRatio
	}
	if opt.CompactMinEntries <= 0 {
		opt.CompactMinEntries = defaultCompactMinEntries
	}
	s := &Sweeper{
		mgr:       m,
		w:         w,
		opt:       opt,
		rootSet:   make(map[string]struct{}),
		requests:  make(chan struct{}, 1),
		watermark: opt.InitialWatermark,
	}
	for _, r := range m.Roots() {
		if a, err := filepath.Abs(r); err == nil {
			r = a
		}
		r = filepath.Clean(r)
		if _, dup := s.rootSet[r]; dup {
			continue
		}
		s.rootSet[r] = struct{}{}
		s.roots = append(s.roots, r)
	}
	if s.opt.mounts == nil {
		// The default needs the normalized roots, so it is bound here
		// rather than with the other option defaults above.
		s.opt.mounts = func() []string { return index.RealMountpoints(s.roots) }
	}
	if s.opt.mountSkips == nil {
		s.opt.mountSkips = func() []string { return index.SystemMountSkips(s.roots) }
	}
	w.setSweepRequester(s.Request)
	return s
}

// Request asks for one sweep pass as soon as the spacing rules allow.
// It never blocks; requests arriving while one is already queued or
// running coalesce into a single follow-up pass.
func (s *Sweeper) Request() {
	select {
	case s.requests <- struct{}{}:
	default:
	}
}

// Start launches the sweep loop. It fails if the Sweeper was already
// started or stopped.
func (s *Sweeper) Start() error {
	ctx, err := s.lc.begin()
	if err != nil {
		return err
	}
	go s.run(ctx)
	return nil
}

// Stop cancels the loop and blocks until it exits, which is prompt at
// every point of the sweep cycle: an in-flight pass aborts between
// directories (and inside a throttle sleep), leaving the watermark
// untouched so the next pass redoes the window; a MinGap wait is cut
// short; and any still-queued request is dropped. Idempotent and safe
// before Start.
func (s *Sweeper) Stop() { s.lc.end() }

// Stats returns a snapshot of the Sweeper's activity.
func (s *Sweeper) Stats() SweepStats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.stats
}

func (s *Sweeper) run(ctx context.Context) {
	defer close(s.lc.done)
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		case <-s.requests:
			if !s.waitMinGap(ctx) {
				return
			}
			s.sweep(ctx)
		}
	}
}

// waitMinGap sleeps until MinGap has passed since the previous pass
// finished. It returns false when the context is cancelled while
// waiting.
func (s *Sweeper) waitMinGap(ctx context.Context) bool {
	s.statsMu.Lock()
	last := s.lastEnd
	s.statsMu.Unlock()
	if last.IsZero() {
		return true
	}
	wait := time.Until(last.Add(s.opt.MinGap))
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

// sweep runs one pass and applies the watermark rule: the watermark
// advances to this pass's start only when the pass ran to completion.
func (s *Sweeper) sweep(ctx context.Context) {
	start := time.Now()
	s.statsMu.Lock()
	s.stats.Running = true
	s.stats.LastStart = start
	s.statsMu.Unlock()

	swept, relisted, complete := s.pass(ctx)

	s.statsMu.Lock()
	s.stats.Running = false
	s.stats.LastDuration = time.Since(start)
	s.stats.Swept = swept
	s.stats.Relisted = relisted
	if complete {
		s.stats.Completed++
	} else {
		s.stats.Cancelled++
	}
	s.lastEnd = time.Now()
	s.statsMu.Unlock()

	if !complete {
		log.Printf("watch: sweep cancelled (%d dirs checked; watermark kept, the next pass redoes the window)", swept)
		return
	}
	s.watermark = start
	log.Printf("watch: sweep complete: %d dirs checked, %d reconciled in %s",
		swept, relisted, time.Since(start).Round(time.Millisecond))
	s.maybeCompact()
}

// maybeCompact asks the Rescanner for a full rebuild once tombstones
// dominate the store. Removals only set a bit, so the name bytes and
// the per-entry columns of every deleted file stay resident until a
// rebuild walks into a FRESH store and swaps it in; a machine that
// churns distinct names would otherwise grow the index for as long as
// the app runs. Requests coalesce and are spaced by the Rescanner's
// own MinGap, and queries keep answering from the old store for the
// whole rebuild, so the only cost is one background walk.
//
// Nothing happens without a Rescanner wired, which is also what keeps
// a Sweeper-only test setup inert.
func (s *Sweeper) maybeCompact() {
	if s.opt.CompactRatio < 0 {
		return
	}
	total := s.mgr.Len()
	if total < s.opt.CompactMinEntries {
		return
	}
	ratio := s.mgr.TombstoneRatio()
	if ratio < s.opt.CompactRatio {
		return
	}
	request := s.w.rescanRequester()
	if request == nil {
		return
	}
	log.Printf("watch: %.0f%% of the index is deleted entries (%d of %d); requesting a rebuild to reclaim them",
		ratio*100, total-s.mgr.LiveCount(), total)
	request()
}

// pass is one sweep: the mount-table diff first, then the roots, then
// every live indexed directory in pages. A first pass with the zero
// watermark re-lists every directory (correct, expensive) and diffs
// the mount table against an empty set (every current mountpoint under
// the roots is reconciled once) -- the same correct-but-expensive
// stance. Directories indexed DURING the pass are naturally covered by
// later pages or by the next pass.
func (s *Sweeper) pass(ctx context.Context) (swept, relisted int, complete bool) {
	ex := s.w.excluder()

	// Mount phase: mountpoints entering, leaving, or changing safety
	// class under the roots are force-converged regardless of mtime.
	cur := s.mountSnapshot()
	unsafe := make([]string, 0, len(cur))
	for mp, state := range cur {
		if state == mountUnsafe {
			unsafe = append(unsafe, mp)
		}
	}
	s.w.setMountSkips(unsafe)
	for _, mp := range changedMounts(s.prevMounts, cur) {
		if ctx.Err() != nil {
			return swept, relisted, false
		}
		if state, present := cur[mp]; present && state == mountUnsafe {
			// Never lstat or readdir a newly unsafe mount. Remove the
			// mountpoint entry and its indexed local subtree, matching
			// BuildFromDisk's prune semantics, and release any watches
			// that still point at the filesystem hidden underneath.
			s.mgr.Remove(mp)
			s.w.dropWatchesUnder(mp)
			relisted++
			continue
		}
		if ex.Match(filepath.Base(mp), mp) {
			continue // excluded paths never touch the index
		}
		if state, present := cur[mp]; present && state == mountWalkable {
			// A mountpoint that APPEARED gets notifier coverage first
			// (fanotify marks the new filesystem when the backend
			// supports it), so events flow before the forced
			// reconcile below indexes its content.
			s.w.markMount(mp)
		}
		s.reconcilePath(ctx, mp)
		relisted++
	}
	s.prevMounts = cur // only after the whole diff was applied

	// Directory phase. The roots come first: they have no index entry
	// of their own, so the paged enumeration below never yields them,
	// yet a file created directly in a root moves only the root's
	// mtime.
	limiter := rateLimiter{perSec: s.opt.StatsPerSec}
	cutoff := s.watermark.Add(-sweepMtimeSlack)
	for _, r := range s.roots {
		if ctx.Err() != nil {
			return swept, relisted, false
		}
		if ex.Match(filepath.Base(r), r) {
			continue
		}
		limiter.wait(ctx)
		n, ok := s.sweepDir(ctx, r, cutoff)
		relisted += n
		swept++
		if !ok {
			return swept, relisted, false
		}
	}
	for start := int32(0); ; {
		page, next := s.mgr.LiveDirsPage(start, sweepPage)
		for _, dir := range page {
			if ctx.Err() != nil {
				return swept, relisted, false
			}
			if ex.Match(filepath.Base(dir), dir) {
				continue
			}
			limiter.wait(ctx)
			n, ok := s.sweepDir(ctx, dir, cutoff)
			relisted += n
			swept++
			if !ok {
				return swept, relisted, false
			}
		}
		if next < 0 {
			break
		}
		start = next
	}
	return swept, relisted, true
}

// sweepDir examines one directory: gone from disk -> reconcile (the
// subtree tombstones), mtime at or past the cutoff -> reconcile (the
// shallow diff; reconcileDir's refreshWatch promotes the dir into the
// hot watched set), else skip. Returns how many reconciles it issued
// (0 or 1) and false when the pass should abort (ctx cancelled).
func (s *Sweeper) sweepDir(ctx context.Context, dir string, cutoff time.Time) (int, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.ModTime().Before(cutoff) {
		s.reconcilePath(ctx, dir)
		return 1, ctx.Err() == nil
	}
	return 0, true
}

// reconcilePath routes one dirty path into the Watcher's reconcile
// engine. Configured roots are special: they deliberately have no
// index entry of their own, and the full reconcile would invent one
// via Manager.Add -- so a live root goes straight to the shallow diff
// (reconcileDir), and a vanished root only tombstones its former
// content (never a refreshWatch on a dead path, which would count a
// bogus drop).
func (s *Sweeper) reconcilePath(ctx context.Context, path string) {
	if _, isRoot := s.rootSet[path]; !isRoot {
		s.w.reconcile(ctx, path)
		return
	}
	if _, err := os.Lstat(path); err != nil {
		s.mgr.Remove(path)
		s.w.dropWatchesUnder(path)
		return
	}
	s.w.reconcileDir(ctx, path)
}

// mountSnapshot classifies every current mountpoint under the roots.
// Unsafe wins defensively if a scripted or malformed source reports a
// path in both lists.
func (s *Sweeper) mountSnapshot() map[string]mountState {
	out := make(map[string]mountState)
	s.addMountsUnderRoots(out, s.opt.mounts(), mountWalkable)
	s.addMountsUnderRoots(out, s.opt.mountSkips(), mountUnsafe)
	return out
}

func (s *Sweeper) addMountsUnderRoots(out map[string]mountState, mounts []string, state mountState) {
	for _, mp := range mounts {
		if !filepath.IsAbs(mp) {
			continue
		}
		mp = filepath.Clean(mp)
		for _, r := range s.roots {
			if pathWithin(mp, r) {
				out[mp] = state
				break
			}
		}
	}
}

// changedMounts returns paths whose presence or classification changed,
// sorted for deterministic application order.
func changedMounts(prev, cur map[string]mountState) []string {
	var out []string
	for k, oldState := range prev {
		if state, ok := cur[k]; !ok || state != oldState {
			out = append(out, k)
		}
	}
	for k := range cur {
		if _, ok := prev[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// rateLimiter is the sweep's sleep-based lstat rate cap: it tracks how
// many operations happened since the first and sleeps -- ctx-abortable,
// in coarse slices -- whenever the pass runs ahead of perSec. perSec
// <= 0 disables it.
type rateLimiter struct {
	perSec int
	start  time.Time
	done   int
}

func (r *rateLimiter) wait(ctx context.Context) {
	if r.perSec <= 0 {
		return
	}
	if r.start.IsZero() {
		r.start = time.Now()
	}
	r.done++
	ahead := time.Duration(r.done)*time.Second/time.Duration(r.perSec) - time.Since(r.start)
	if ahead < 10*time.Millisecond {
		return // within tolerance: no sleep, near-zero overhead
	}
	t := time.NewTimer(ahead)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
