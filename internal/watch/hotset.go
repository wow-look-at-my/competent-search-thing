package watch

import (
	"context"
	"errors"
	"log"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// The bounded hot set: which directories hold a live per-directory
// watch, filled in priority order (roots, then the home subtree, then
// everything else), touched LRU-style by activity, and evicted at
// budget. Under a wideCoverage backend (fanotify whole-filesystem
// marks) every entry point here is a cheap no-op and the set stays
// empty -- see Watcher.wide. Split from watch.go, which keeps the
// types, lifecycle, and state helpers.

// watchedCount returns the current watched-set size.
func (w *Watcher) watchedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watched)
}

// addWatch starts watching dir with hot-set semantics: an
// already-watched dir is touched (promoted within the LRU), and a new
// dir gets a watch even at budget by evicting the least-recently-
// touched watched dir. A notifier failure -- typically the inotify
// watch limit -- degrades the watcher instead of stopping it: the
// drop is counted, the first one is logged, and later ones stay
// silent so an exhausted limit cannot spam the log.
func (w *Watcher) addWatch(dir string) { w.watch(dir, false) }

// refreshWatch is addWatch without the already-bookkept short-circuit:
// the notifier Add is re-issued even when dir is recorded as watched.
// reconcileDir uses it on every dirty directory, because a dirty path
// can mean the directory was deleted and recreated within one debounce
// window -- the kernel watch follows the inode and died with the old
// one, while the bookkeeping still lists the path, so only a re-issued
// Add re-arms it (fsnotify re-registers the path and adopts the new
// descriptor; on a still-live watch the re-add is an idempotent mask
// merge). The ordered-batch model got the same effect from applying
// the Remove event before the Create; the dirty-path model has no
// order to lean on.
func (w *Watcher) refreshWatch(dir string) { w.watch(dir, true) }

// promote pulls dir into the hot watched set -- watch with eviction:
// at budget the least-recently-touched watched dir is released to
// make room. The sweep tier promotes directories where a sweep found
// changes (recent change predicts more change); the event path
// reaches the same effect through reconcileDir's refreshWatch.
func (w *Watcher) promote(dir string) { w.watch(dir, true) }

func (w *Watcher) watch(dir string, refresh bool) {
	if w.watchExcluded(dir) {
		// Watch-excluded dirs never get (or refresh) a watch, from any
		// path -- event promotion, sweep promotion, or the fill. They
		// stay indexed and swept; the sweep interval is their bound.
		return
	}
	var notify func()
	defer func() { // runs AFTER the unlock below; never under w.mu
		if notify != nil {
			notify()
		}
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil || w.lc.stopping() || w.wide {
		// Under wideCoverage the whole filesystem is already marked:
		// per-directory watches (and their bookkeeping) do not exist,
		// so reconcileDir's refreshWatch on every dirty directory is
		// this cheap early return.
		return
	}
	el, have := w.watched[dir]
	if have && el != nil {
		w.lru.MoveToFront(el) // touch: activity keeps the dir hot
	}
	if have && !refresh {
		return
	}
	_, pin := w.pinned[dir]
	if !have && !pin && !w.makeRoomLocked() {
		// At budget with nothing evictable (every slot pinned): the
		// dir stays cold. Not a drop, not degradation -- sweeps cover
		// it.
		return
	}
	if err := w.n.Add(dir); err != nil {
		if errors.Is(err, fsnotify.ErrClosed) {
			return // racing Stop, not a degradation
		}
		if have {
			// The refresh proved the recorded watch dead and no new
			// one could be armed: stop claiming it.
			w.forgetLocked(dir)
		}
		notify = w.degradeLocked()
		w.stats.DroppedWatches++
		if !w.loggedDrop {
			w.loggedDrop = true
			log.Printf("watch: adding watch for %s failed: %v (degraded; further drops are counted silently)", dir, err)
		}
		return
	}
	if !have {
		if pin {
			w.watched[dir] = nil // roots: always watched, never evicted
		} else {
			w.watched[dir] = w.lru.PushFront(dir)
		}
		w.stats.WatchedDirs = len(w.watched)
	}
}

// addWatchCold is the fill-phase add (initial registration and rescan
// resync): at budget it does NOTHING -- no watch syscall, no
// eviction, no drop -- because a fill must not churn the hot set or
// storm the kernel with adds it cannot keep. Beyond-budget dirs stay
// cold and the sweep tier converges them; that stance is what kills
// the failing-syscall storm of the old watch-everything registration.
// Cold adds join the LRU at the BACK in call order, so the fill's
// priority order doubles as the initial eviction order (the
// first-filled, highest-priority dirs are evicted last), and an
// already-watched dir is left untouched (a fill is not activity; it
// must not reshuffle recency). Reports whether the budget still has
// room, so fill loops can stop enumerating early.
func (w *Watcher) addWatchCold(dir string) bool {
	if w.watchExcluded(dir) {
		// Never watched, but the budget keeps its room: later dirs may
		// fit. (Defense in depth -- desiredSplit already filters these
		// out of the fill lists.)
		return true
	}
	var notify func()
	defer func() {
		if notify != nil {
			notify()
		}
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil || w.lc.stopping() || w.wide {
		return false
	}
	if len(w.watched) >= w.budget {
		return false
	}
	if _, have := w.watched[dir]; have {
		return true
	}
	if err := w.n.Add(dir); err != nil {
		if errors.Is(err, fsnotify.ErrClosed) {
			return false
		}
		notify = w.degradeLocked()
		w.stats.DroppedWatches++
		if !w.loggedDrop {
			w.loggedDrop = true
			log.Printf("watch: adding watch for %s failed: %v (degraded; further drops are counted silently)", dir, err)
		}
		return true // the budget still has room; later dirs may fit
	}
	w.watched[dir] = w.lru.PushBack(dir)
	w.stats.WatchedDirs = len(w.watched)
	return len(w.watched) < w.budget
}

// makeRoomLocked ensures a free budget slot for one more watch,
// evicting the least-recently-touched evictable (non-root) dir when
// the set is full. It reports false when the set is at budget and
// nothing is evictable. Callers hold w.mu.
func (w *Watcher) makeRoomLocked() bool {
	if len(w.watched) < w.budget {
		return true
	}
	back := w.lru.Back()
	if back == nil {
		return false
	}
	d := back.Value.(string)
	w.lru.Remove(back)
	delete(w.watched, d)
	_ = w.n.Remove(d)
	w.stats.Evictions++
	w.stats.WatchedDirs = len(w.watched)
	return true
}

// forgetLocked drops dir from the watch bookkeeping (map and LRU).
// Callers hold w.mu.
func (w *Watcher) forgetLocked(dir string) {
	el, ok := w.watched[dir]
	if !ok {
		return
	}
	if el != nil {
		w.lru.Remove(el)
	}
	delete(w.watched, dir)
	w.stats.WatchedDirs = len(w.watched)
}

// forgetWatch releases the watch bookkeeping (and the notifier watch)
// for a single path, if any. reconcile uses it when a path the
// bookkeeping may know as a watched directory turns out to be a file
// on disk -- a childless dir-to-file flip within one dirty window --
// where the O(watched) dropWatchesUnder sweep would be waste: this is
// one map hit.
func (w *Watcher) forgetWatch(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.watched[dir]; !ok {
		return
	}
	if w.n != nil {
		_ = w.n.Remove(dir)
	}
	w.forgetLocked(dir)
}

// touchIfWatched promotes dir within the LRU when it is ALREADY
// watched -- a cheap map hit on the parent of every reconciled path,
// so event activity inside a watched dir keeps that dir hot. It never
// watches anything: file events must not promote cold parents into
// the hot set (that is reconcileDir's and the sweeper's job).
func (w *Watcher) touchIfWatched(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if el, ok := w.watched[dir]; ok && el != nil {
		w.lru.MoveToFront(el)
	}
}

// dropWatchesUnder forgets the watch on path and on every watched
// directory beneath it. Notifier errors are ignored on purpose: when a
// directory tree is deleted, inotify has already dropped its watches
// (the notifier no longer knows the paths), while after a rename this
// explicit Remove is exactly what detaches the moved inode's stale
// watch.
func (w *Watcher) dropWatchesUnder(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for d, el := range w.watched {
		if pathWithin(d, path) {
			if el != nil {
				w.lru.Remove(el)
			}
			delete(w.watched, d)
			if w.n != nil {
				_ = w.n.Remove(d)
			}
		}
	}
	w.stats.WatchedDirs = len(w.watched)
}

// syncWatches reconciles the watch set with the CURRENT index contents,
// budget-aware. The Rescanner calls it after every successful
// BuildFromDisk swap: directories that vanished lose their watch, then
// the set is refilled to the budget in the same priority order the
// initial registration uses (roots, home subtree, everything else).
// Watches on still-desired directories are kept -- a resync tops up,
// it never resets the hot set's accumulated recency -- and the refill
// uses cold adds only, so it can never evict or exceed the budget.
// Safe to run concurrently with the event loop.
//
// The watch set can be huge, so the reconcile checks ctx between
// directories: cancelling it -- the Rescanner passes its loop context,
// which Stop cancels -- abandons the rest of the pass promptly instead
// of holding shutdown hostage to millions of notifier calls. A later
// sweep or rescan reconciles whatever was left undone.
func (w *Watcher) syncWatches(ctx context.Context) {
	w.mu.Lock()
	// Under wideCoverage there is no watch set to reconcile: the
	// bookkeeping is empty by design and stays that way.
	ready := w.n != nil && !w.lc.stopping() && !w.wide
	w.mu.Unlock()
	if !ready || ctx.Err() != nil {
		return
	}
	home, rest, total := w.desiredSplit(ctx, -1)
	want := make(map[string]struct{}, len(w.rootList)+len(home)+len(rest))
	for _, d := range w.rootList {
		want[d] = struct{}{}
	}
	for _, d := range home {
		want[d] = struct{}{}
	}
	for _, d := range rest {
		want[d] = struct{}{}
	}
	w.mu.Lock()
	for d, el := range w.watched {
		if ctx.Err() != nil {
			break // partial `want` never drops watches: this loop is dead on cancel
		}
		if _, ok := want[d]; !ok {
			if el != nil {
				w.lru.Remove(el)
			}
			delete(w.watched, d)
			_ = w.n.Remove(d)
		}
	}
	w.stats.WatchedDirs = len(w.watched)
	if ctx.Err() == nil {
		w.stats.IndexedDirs = total
	}
	w.mu.Unlock()
	w.fill(ctx, home, rest)
}

// desiredSplit enumerates the desired watch set in fill-priority
// order. The configured roots are not returned (w.rootList already
// holds them, normalized and exclude-filtered); home collects live
// indexed dirs under the user's home directory and rest everything
// else, each capped at bound entries when bound >= 0 (a budgeted fill
// never needs more than budget candidates, which keeps the buffers
// small on huge indexes); total counts EVERY desired dir including
// the roots and the entries beyond the caps. Watch-excluded dirs
// (Options.WatchEx) are not desired: they appear in neither list nor
// the total. The index is enumerated
// in LiveDirsPage chunks, so the Manager's read lock is released
// between pages, and cancelling ctx stops the enumeration between
// pages; callers already treat the result as best-effort.
func (w *Watcher) desiredSplit(ctx context.Context, bound int) (home, rest []string, total int) {
	total = len(w.rootList)
	homeBase := ""
	if w.homeDir != nil {
		if h, err := w.homeDir(); err == nil && h != "" {
			if a, err := filepath.Abs(h); err == nil {
				h = a
			}
			homeBase = filepath.Clean(h)
		}
	}
	for start := int32(0); ; {
		page, next := w.mgr.LiveDirsPage(start, index.DefaultLiveDirsPage)
		for _, p := range page {
			if _, isRoot := w.pinned[p]; isRoot {
				continue // already counted with the roots
			}
			if w.ex.Match(filepath.Base(p), p) || w.watchExcluded(p) {
				continue
			}
			total++
			if homeBase != "" && pathWithin(p, homeBase) {
				if bound < 0 || len(home) < bound {
					home = append(home, p)
				}
			} else if bound < 0 || len(rest) < bound {
				rest = append(rest, p)
			}
		}
		if next < 0 || ctx.Err() != nil {
			break
		}
		start = next
	}
	return home, rest, total
}

// fill tops the watch set up to the budget in priority order: roots
// first (pinned: always watched, even beyond the budget), then the
// home subtree until 75% of the budget is in use, then everything
// else until the budget is full. Cold adds only -- a fill never
// evicts -- and ctx aborts between adds, so Stop can interrupt a long
// registration pass at any point.
func (w *Watcher) fill(ctx context.Context, home, rest []string) {
	for _, d := range w.rootList {
		if ctx.Err() != nil {
			return
		}
		w.addWatch(d)
	}
	budget := w.budgetVal()
	homeCap := budget - budget/4 // 75%, overflow-safe at MaxInt
	for _, d := range home {
		if ctx.Err() != nil {
			return
		}
		if w.watchedCount() >= homeCap || !w.addWatchCold(d) {
			break
		}
	}
	for _, d := range rest {
		if ctx.Err() != nil {
			return
		}
		if !w.addWatchCold(d) {
			return
		}
	}
}
