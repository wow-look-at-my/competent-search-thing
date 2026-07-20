package watch

import "time"

// Debounce defaults; see Options.
const (
	defaultQuiet      = 250 * time.Millisecond
	defaultMaxAge     = time.Second
	defaultMaxPending = 4096
)

// defaultHoldCap bounds the dirty-path set a deferred-start watcher
// collects while held (StartDeferred; the debouncer's maxPending
// flush trigger is ignored during the hold, since flushing IS what is
// held). 64k unique paths bound memory to a few MB over even a long
// initial walk; paths dropped beyond the cap degrade the watcher and
// request one reconcile sweep at release, so the loss converges like
// any kernel-queue overflow.
const defaultHoldCap = 65536

// debouncer coalesces bursts of filesystem events into an
// insertion-ordered set of dirty paths. An event carries no operation
// by the time it gets here: it only marks its (already cleaned) path
// dirty, and re-marking a path that is still pending refreshes the
// quiet window without growing the set, so an event storm on one path
// costs one pending entry and one reconcile. A batch becomes due when
// the filesystem has been quiet for `quiet`, or when the OLDEST
// pending path has waited `maxAge` (so a steady drizzle of events
// cannot defer the flush forever), and it must flush immediately once
// `maxPending` UNIQUE paths pile up.
//
// The debouncer is pure bookkeeping: it is driven only by the Watcher's
// run loop (never concurrently), and the current time is passed in by
// the caller, so tests exercise the thresholds synthetically.
type debouncer struct {
	quiet      time.Duration
	maxAge     time.Duration
	maxPending int

	pending map[string]struct{} // membership; keys are clean paths
	order   []string            // the same paths in first-arrival order
	first   time.Time           // arrival time of order[0]
	last    time.Time           // arrival time of the newest add call
}

// size returns the number of unique pending paths (the hold phase's
// bound check; see Watcher.holdAdd).
func (d *debouncer) size() int { return len(d.order) }

// has reports whether path is already pending.
func (d *debouncer) has(path string) bool {
	_, ok := d.pending[path]
	return ok
}

// add marks one cleaned path dirty and reports whether the batch must
// flush immediately because the size cap was reached. A path that is
// already pending is not stored again -- only the quiet window resets.
func (d *debouncer) add(path string, now time.Time) bool {
	if _, ok := d.pending[path]; !ok {
		if d.pending == nil {
			d.pending = make(map[string]struct{})
		}
		if len(d.order) == 0 {
			d.first = now
		}
		d.pending[path] = struct{}{}
		d.order = append(d.order, path)
	}
	d.last = now
	return len(d.order) >= d.maxPending
}

// deadline returns the moment the pending batch becomes due, and
// whether anything is pending at all.
func (d *debouncer) deadline() (time.Time, bool) {
	if len(d.order) == 0 {
		return time.Time{}, false
	}
	dl := d.last.Add(d.quiet)
	if oldest := d.first.Add(d.maxAge); oldest.Before(dl) {
		dl = oldest
	}
	return dl, true
}

// due reports whether the pending batch should flush at time now.
func (d *debouncer) due(now time.Time) bool {
	dl, ok := d.deadline()
	return ok && !now.Before(dl)
}

// take returns the pending unique paths in first-arrival order and
// resets the debouncer for the next burst.
func (d *debouncer) take() []string {
	batch := d.order
	d.order = nil
	d.pending = nil
	return batch
}
