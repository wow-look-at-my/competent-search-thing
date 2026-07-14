package watch

import (
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce defaults; see Options.
const (
	defaultQuiet      = 250 * time.Millisecond
	defaultMaxAge     = time.Second
	defaultMaxPending = 4096
)

// debouncer coalesces bursts of filesystem events into ordered batches.
// Events accumulate in arrival order; a batch becomes due when the
// filesystem has been quiet for `quiet`, or when the OLDEST pending
// event has waited `maxAge` (so a steady drizzle of events cannot defer
// the flush forever), and it must flush immediately once `maxPending`
// events pile up.
//
// The debouncer is pure bookkeeping: it is driven only by the Watcher's
// run loop (never concurrently), and the current time is passed in by
// the caller, so tests exercise the thresholds synthetically.
type debouncer struct {
	quiet      time.Duration
	maxAge     time.Duration
	maxPending int

	pending []fsnotify.Event
	first   time.Time // arrival time of pending[0]
	last    time.Time // arrival time of pending[len-1]
}

// add appends one event and reports whether the batch must flush
// immediately because the size cap was reached.
func (d *debouncer) add(ev fsnotify.Event, now time.Time) bool {
	if len(d.pending) == 0 {
		d.first = now
	}
	d.pending = append(d.pending, ev)
	d.last = now
	return len(d.pending) >= d.maxPending
}

// deadline returns the moment the pending batch becomes due, and
// whether anything is pending at all.
func (d *debouncer) deadline() (time.Time, bool) {
	if len(d.pending) == 0 {
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

// take returns the pending batch in arrival order and resets the
// debouncer for the next burst.
func (d *debouncer) take() []fsnotify.Event {
	batch := d.pending
	d.pending = nil
	return batch
}
