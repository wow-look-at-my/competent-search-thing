package watch

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// run is the watcher's single event loop: it registers the initial
// watch set, then feeds events through the debouncer and applies each
// flushed batch in arrival order until the context is cancelled or the
// notifier closes.
func (w *Watcher) run(ctx context.Context) {
	defer close(w.lc.done)
	w.addInitialWatches(ctx)

	// The timer tracks the debouncer's deadline. go.mod requires a Go
	// version with the >=1.23 timer semantics, so Reset/Stop need no
	// channel-drain dance and the timer never delivers stale fires.
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.n.Events():
			if !ok {
				return
			}
			if !w.wantEvent(ev) {
				continue
			}
			if w.deb.add(ev, time.Now()) {
				timer.Stop()
				w.flush(ctx)
				continue
			}
			if dl, pending := w.deb.deadline(); pending {
				timer.Reset(time.Until(dl))
			}
		case err, ok := <-w.n.Errors():
			if !ok {
				return
			}
			w.handleError(err)
		case <-timer.C:
			if w.deb.due(time.Now()) {
				w.flush(ctx)
			} else if dl, pending := w.deb.deadline(); pending {
				timer.Reset(time.Until(dl))
			}
		}
	}
}

// addInitialWatches watches the roots plus every directory that is
// currently live in the index. It runs inside the loop goroutine and
// checks the context between adds, so Stop can interrupt a long
// registration pass at any point.
func (w *Watcher) addInitialWatches(ctx context.Context) {
	for _, d := range w.desiredDirs() {
		if ctx.Err() != nil {
			return
		}
		w.addWatch(d)
	}
	s := w.Stats()
	log.Printf("watch: watching %d directories (%d dropped)", s.WatchedDirs, s.DroppedWatches)
}

// wantEvent filters events at intake. Only Create/Remove/Rename can
// change the set of indexed NAMES -- Write and Chmod never do, so they
// are ignored -- and excluded paths are dropped here, before they can
// ever touch the index (an excluded directory is never watched, so this
// only fires for excluded names appearing directly inside watched
// directories).
func (w *Watcher) wantEvent(ev fsnotify.Event) bool {
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Remove) && !ev.Has(fsnotify.Rename) {
		return false
	}
	path := filepath.Clean(ev.Name)
	return !w.ex.Match(filepath.Base(path), path)
}

// flush applies the pending batch in arrival order. Ordered application
// makes interleaved bursts converge on the on-disk truth:
// create-then-delete ends deleted (the Create's Lstat already fails and
// the trailing Remove tombstones any leftover), delete-then-create ends
// live (AddEntry resurrects tombstones).
func (w *Watcher) flush(ctx context.Context) {
	for _, ev := range w.deb.take() {
		if ctx.Err() != nil {
			return
		}
		path := filepath.Clean(ev.Name)
		if ev.Has(fsnotify.Create) {
			w.applyCreate(ctx, path)
		} else {
			// Remove, or Rename reporting the OLD name; the new name
			// arrives as its own Create event.
			w.applyRemove(path)
		}
	}
}

// applyCreate stats the created path and indexes it. Lstat (never Stat)
// keeps symlink handling identical to the walker: the link itself is
// indexed as a non-directory and never followed. A path that is already
// gone again -- created and deleted within one debounce window -- is
// simply skipped; the trailing Remove event in the same batch makes
// that final.
func (w *Watcher) applyCreate(ctx context.Context, path string) {
	fi, err := os.Lstat(path)
	if err != nil {
		return
	}
	// The name comes from the OS, so AddEntry's validation (NUL bytes,
	// separators, relative parents) cannot fail here; errors are
	// deliberately dropped rather than crashing the loop.
	_ = w.mgr.Add(filepath.Dir(path), filepath.Base(path), fi.IsDir())
	if fi.IsDir() {
		w.scanNewDir(ctx, path)
	}
}

// scanNewDir indexes everything under a directory that just appeared,
// watching the directory and each nested subdirectory. Entries go
// through Manager.Add -- duplicate-safe, unlike the fresh-store Walk --
// because events and this scan can both report the same path. The watch
// is added BEFORE the directory is read so nothing slips through:
// anything created after ReadDir raises its own event, anything created
// before is in the listing, and overlaps dedup in AddEntry.
func (w *Watcher) scanNewDir(ctx context.Context, dir string) {
	stack := []string{dir}
	for len(stack) > 0 {
		if ctx.Err() != nil {
			return
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		w.addWatch(d)
		entries, err := w.readDir(d)
		if err != nil {
			continue // vanished or unreadable: skipped, like the walker
		}
		for _, de := range entries {
			name := de.Name()
			full := filepath.Join(d, name)
			if w.ex.Match(name, full) {
				continue
			}
			// DirEntry.IsDir is false for symlinks (even to
			// directories), matching the walker: links are indexed,
			// never descended.
			isDir := de.IsDir()
			_ = w.mgr.Add(d, name, isDir)
			if isDir {
				stack = append(stack, full)
			}
		}
	}
}

// applyRemove tombstones path -- Manager.Remove covers the whole
// subtree when path is a directory -- and drops any watches at or below
// it.
func (w *Watcher) applyRemove(path string) {
	w.mgr.Remove(path)
	w.dropWatchesUnder(path)
}

// handleError reacts to notifier-level errors. An event queue overflow
// means events were LOST and the index is stale in unknown ways: the
// watcher marks itself degraded and asks the Rescanner for a reconcile
// rescan (the Rescanner spaces storms out via MinGap). Only the first
// overflow is logged; anything else is logged as-is.
func (w *Watcher) handleError(err error) {
	if !errors.Is(err, fsnotify.ErrEventOverflow) {
		log.Printf("watch: notifier error: %v", err)
		return
	}
	w.mu.Lock()
	w.stats.Overflows++
	w.stats.Degraded = true
	fn := w.requestRescan
	first := !w.loggedOverf
	w.loggedOverf = true
	w.mu.Unlock()
	if first {
		log.Printf("watch: event queue overflow, events lost (degraded); requesting reconcile rescan")
	}
	if fn != nil {
		fn()
	}
}
