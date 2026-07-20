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
// watch set, then feeds dirty paths through the debouncer and
// reconciles each flushed path against the on-disk truth until the
// context is cancelled or the notifier closes. A deferred start
// (StartDeferred) prepends the hold phase: events are collected
// without being applied until Release, the registration pass runs
// only then (against the index the initial build just swapped in),
// and the collected paths are applied right after it.
func (w *Watcher) run(ctx context.Context) {
	defer close(w.lc.done)
	if w.releaseCh != nil {
		w.collectUntilRelease(ctx)
	}
	w.addInitialWatches(ctx)
	if w.releaseCh != nil {
		// Everything held during the initial build applies now, through
		// the ordinary reconcile path against the freshly built index;
		// paths lost to the hold cap degrade like an overflow and are
		// converged by the sweep requested here (the requesters are
		// wired by the time Release runs).
		w.flush(ctx)
		w.reportHoldLoss(ctx)
	}

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
			path, ok := w.wantEvent(ev)
			if !ok {
				continue
			}
			if w.deb.add(path, time.Now()) {
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

// collectUntilRelease is the deferred start's hold phase: notifier
// events are drained into the debouncer's dirty-path set -- deduped,
// never applied, bounded by holdCap -- until Release closes releaseCh
// (or Stop cancels, or the notifier dies). Draining continuously keeps
// the notifier's event channel from backing up, so a long initial
// build never pushes the backend itself into overflow just because
// application is held. Notifier errors (kernel-queue overflows
// included) go through the ordinary handleError; a requester that is
// not wired yet costs nothing, because reportHoldLoss re-kicks the
// sweep at release whenever anything was lost while held.
func (w *Watcher) collectUntilRelease(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.releaseCh:
			return
		case ev, ok := <-w.n.Events():
			if !ok {
				return
			}
			if path, ok := w.wantEvent(ev); ok {
				w.holdAdd(path)
			}
		case err, ok := <-w.n.Errors():
			if !ok {
				return
			}
			w.handleError(err)
		}
	}
}

// holdAdd records one dirty path while held. Re-marking a pending path
// is free at any size; a NEW path beyond holdCap is dropped and the
// loss latched (heldDropped), to be reported and swept at release.
func (w *Watcher) holdAdd(path string) {
	if w.deb.size() >= w.holdCap && !w.deb.has(path) {
		w.heldDropped = true
		return
	}
	w.deb.add(path, time.Now())
}

// reportHoldLoss converges anything lost during the hold phase: paths
// dropped beyond holdCap, and kernel-queue overflows that fired while
// no sweeper/rescanner was wired yet (they are wired between the
// hold and Release). Any loss means the index is stale in unknown
// ways, so one reconcile sweep is requested -- exactly the overflow
// recovery path -- and a cap loss additionally counts and logs as an
// overflow of its own.
func (w *Watcher) reportHoldLoss(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var notify func()
	w.mu.Lock()
	kick := w.stats.Overflows > 0
	if w.heldDropped {
		kick = true
		notify = w.degradeLocked()
		w.stats.Overflows++
	}
	sweep, rescan := w.requestSweep, w.requestRescan
	w.mu.Unlock()
	if w.heldDropped {
		log.Printf("watch: more than %d paths changed during the initial index build; extras were dropped (degraded), requesting reconcile sweep", w.holdCap)
		w.heldDropped = false
	}
	if notify != nil {
		notify()
	}
	if !kick {
		return
	}
	if sweep != nil {
		sweep()
	} else if rescan != nil {
		rescan()
	}
}

// addInitialWatches fills the watch set up to the budget: the roots
// first (always watched), then live indexed dirs under the user's
// home, then everything else. Beyond-budget dirs are simply never
// issued a watch syscall -- they stay cold and sweeps converge them.
// It runs inside the loop goroutine and checks the context between
// adds, so Stop can interrupt a long registration pass at any point;
// either way it closes initialDone so waiters unblock.
func (w *Watcher) addInitialWatches(ctx context.Context) {
	defer close(w.initialDone)
	if w.isWide() {
		// Whole-filesystem marks already cover every directory: no
		// fill, no enumeration, no bookkeeping. Stats keep zero
		// watched/indexed counts -- there is no watch set to count.
		// The no-op "none" backend is wide for the opposite reason
		// (live watching is OFF, so there is still no watch set); its
		// refusal was already logged loudly by the strict selection,
		// so nothing here may claim marks are active.
		if name := w.Stats().Backend; name != "none" {
			log.Printf("watch: backend %s: whole-filesystem coverage active; per-directory watches not needed",
				name)
		}
		return
	}
	home, rest, total := w.desiredSplit(ctx, w.budgetVal())
	w.fill(ctx, home, rest)
	w.mu.Lock()
	if ctx.Err() == nil {
		w.stats.IndexedDirs = total
	}
	s := w.stats
	w.mu.Unlock()
	log.Printf("watch: live watches on %d of %d indexed dirs (budget %s, %d dropped); unwatched dirs converge via sweeps",
		s.WatchedDirs, total, FormatBudget(s.Budget), s.DroppedWatches)
}

// wantEvent filters events at intake and reduces each surviving event
// to its cleaned path -- the only thing the reconcile pipeline
// consumes. The op codes are advisory: they are consulted here once,
// to drop Write/Chmod (on the fsnotify wire those can never change the
// set of indexed NAMES), and never again -- so a future notifier that
// merges ops or reports bare paths plugs into the same pipeline.
// Excluded paths are dropped here too, before they can ever touch the
// index (an excluded directory is never watched, so this only fires
// for excluded names appearing directly inside watched directories).
func (w *Watcher) wantEvent(ev fsnotify.Event) (string, bool) {
	if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Remove) && !ev.Has(fsnotify.Rename) {
		return "", false
	}
	path := filepath.Clean(ev.Name)
	if w.ex.Match(filepath.Base(path), path) {
		return "", false
	}
	return path, true
}

// flush reconciles every dirty path in first-arrival order. The order
// is fairness, not correctness: each path is reconciled against the
// CURRENT on-disk state, so any arrival order of the underlying events
// converges to the same index.
func (w *Watcher) flush(ctx context.Context) {
	for _, path := range w.deb.take() {
		if ctx.Err() != nil {
			return
		}
		w.reconcile(ctx, path)
	}
}

// reconcile makes the index agree with the on-disk state of one dirty
// path. The event op that dirtied the path plays no role: lstat at
// apply time decides. That is required by notification backends that
// merge same-object events and lose their order (one fanotify record
// can carry CREATE|DELETE), and it makes create-then-delete vs
// delete-then-create indistinguishable by construction -- both are one
// dirty path whose on-disk state settles the outcome. Lstat (never
// Stat) keeps symlink handling identical to the walker: the link
// itself is indexed as a non-directory and never followed.
func (w *Watcher) reconcile(ctx context.Context, path string) {
	// Activity inside a watched directory keeps that directory hot: a
	// cheap map hit that promotes an already-watched parent within the
	// LRU. Unwatched parents are deliberately NOT pulled into the hot
	// set by file events (reconcileDir and the sweeper promote dirs).
	w.touchIfWatched(filepath.Dir(path))
	fi, err := os.Lstat(path)
	if err != nil {
		// Gone: tombstone it -- Manager.Remove covers the whole subtree
		// when the index knew a directory here, and is a safe no-op
		// when it never knew the path -- and drop any watches at or
		// below it.
		w.mgr.Remove(path)
		w.dropWatchesUnder(path)
		return
	}
	// The name comes from the OS, so AddEntry's validation (NUL bytes,
	// separators, relative parents) cannot fail here; errors are
	// deliberately dropped rather than crashing the loop.
	if !fi.IsDir() {
		// Files AND symlinks: Lstat reports a symlink-to-dir as a
		// non-directory, exactly like the walker's DirEntry check, so
		// links are indexed but never watched or descended.
		//
		// A path the index knew as a DIRECTORY may have flipped to a
		// file within one dirty window (dir deleted, file recreated:
		// one merged dirty path, and the parent may never be dirtied
		// -- both events carry THIS path). AddEntry only refreshes the
		// entry's dir bit, so the old subtree must be tombstoned first
		// or its descendants would stay live under a path that is now
		// a file. ChildrenOf is a cheap map hit for ordinary files.
		if len(w.mgr.ChildrenOf(path)) > 0 {
			w.mgr.Remove(path)
			w.dropWatchesUnder(path)
		} else {
			// Childless flip: no subtree to tombstone, but the
			// bookkeeping may still claim a (dead) watch here.
			w.forgetWatch(path)
		}
		_ = w.mgr.Add(filepath.Dir(path), filepath.Base(path), false)
		return
	}
	_ = w.mgr.Add(filepath.Dir(path), filepath.Base(path), true)
	w.reconcileDir(ctx, path)
}

// reconcileDir is the shallow diff primitive: it compares one
// directory's on-disk children against the index's view and applies
// the difference. Children new to the index are added, and new
// SUBDIRECTORIES get the full scanNewDir walk (they are new to the
// index, so everything beneath them is too); children whose kind
// flipped (file<->dir) are tombstoned and re-added with the new bit;
// index children missing from disk are removed with their subtrees.
// Children present with the same kind are left alone -- the diff never
// descends into subtrees the index already tracks.
//
// Cost: one ChildrenOf plus one ReadDir per dirty directory. The
// debouncer's dedup collapses an event storm on one directory into a
// single reconcile, and a dirty FILE reconciles without ever touching
// its siblings (reconcile stops before this function), so the shallow
// diff runs only for paths that are directories on disk.
func (w *Watcher) reconcileDir(ctx context.Context, dir string) {
	// Watch before read, so nothing slips through: anything created
	// after ReadDir raises its own event, anything created before is
	// in the listing, and overlaps dedup in AddEntry. refreshWatch
	// rather than addWatch, because a dirty directory may have been
	// deleted and recreated within one debounce window: the kernel
	// watch died with the old inode while the bookkeeping still lists
	// the path, and only a re-issued notifier Add re-arms it
	// (idempotent on a still-live watch).
	w.refreshWatch(dir)
	entries, err := w.readDir(dir)
	if err != nil {
		if _, lerr := os.Lstat(dir); lerr != nil {
			// Vanished between the caller's lstat and the read: the
			// same outcome as reconciling a gone path.
			w.mgr.Remove(dir)
			w.dropWatchesUnder(dir)
		}
		// Otherwise unreadable: skipped, like the walker.
		return
	}
	have := make(map[string]bool) // the index's children: name -> isDir
	for _, c := range w.mgr.ChildrenOf(dir) {
		have[c.Name] = c.IsDir
	}
	onDisk := make(map[string]struct{}, len(entries))
	for _, de := range entries {
		name := de.Name()
		full := filepath.Join(dir, name)
		if w.ex.Match(name, full) {
			continue
		}
		onDisk[name] = struct{}{}
		// DirEntry.IsDir is false for symlinks (even to directories),
		// matching the walker: links are indexed, never descended.
		isDir := de.IsDir()
		haveDir, known := have[name]
		if known && haveDir == isDir {
			continue // present with the same kind: nothing to do
		}
		if known {
			// Kind flip: tombstone first -- for a dir-to-file flip
			// that covers the old subtree, which a plain re-add with
			// the new bit would leave live -- and forget any watches
			// that pointed into the vanished tree.
			w.mgr.Remove(full)
			if haveDir {
				w.dropWatchesUnder(full)
			}
		}
		_ = w.mgr.Add(dir, name, isDir)
		if isDir {
			w.scanNewDir(ctx, full)
		}
	}
	for name := range have {
		if _, ok := onDisk[name]; ok {
			continue
		}
		full := filepath.Join(dir, name)
		if w.ex.Match(name, full) {
			// Excluded names should never be in the index; filter
			// defensively so reconcile never acts on one that is.
			continue
		}
		w.mgr.Remove(full)
		w.dropWatchesUnder(full)
	}
}

// scanNewDir indexes everything under a directory that is NEW to the
// index (reconcileDir recurses only into such children, so nothing
// beneath it can be indexed yet), watching the directory and each
// nested subdirectory. Entries go through Manager.Add --
// duplicate-safe, unlike the fresh-store Walk -- because events and
// this scan can both report the same path. The watch is added BEFORE
// the directory is read so nothing slips through: anything created
// after ReadDir raises its own event, anything created before is in
// the listing, and overlaps dedup in AddEntry.
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

// handleError reacts to notifier-level errors. An event queue overflow
// means events were LOST and the index is stale in unknown ways: the
// watcher marks itself degraded and asks the Sweeper for a reconcile
// sweep -- a shallow pass converges the loss far cheaper than a full
// rebuild -- falling back to a Rescanner rescan request when no
// sweeper is wired (standalone watcher+rescanner setups keep working).
// Both sides space storms out via their MinGap. Only the first
// overflow is logged; anything else is logged as-is.
func (w *Watcher) handleError(err error) {
	if !errors.Is(err, fsnotify.ErrEventOverflow) {
		log.Printf("watch: notifier error: %v", err)
		return
	}
	w.mu.Lock()
	notify := w.degradeLocked()
	w.stats.Overflows++
	sweep := w.requestSweep
	rescan := w.requestRescan
	first := !w.loggedOverf
	w.loggedOverf = true
	w.mu.Unlock()
	if first {
		log.Printf("watch: event queue overflow, events lost (degraded); requesting reconcile sweep")
	}
	if notify != nil {
		notify()
	}
	if sweep != nil {
		sweep()
	} else if rescan != nil {
		rescan()
	}
}
