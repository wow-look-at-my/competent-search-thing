// Package watch keeps the index live after the initial disk walk: a
// Watcher reconciles filesystem events against the index.Manager, and
// a Rescanner runs full BuildFromDisk rebuilds, both periodically and
// whenever the watcher degrades.
//
// Event model: an event is only a DIRTY PATH. The notifier's op codes
// are advisory -- intake consults them once, to drop Write/Chmod noise
// -- and truth comes from lstat at apply time: a dirty path that is
// gone is removed with its subtree, a dirty file is (re-)added, and a
// dirty directory gets a shallow readdir diff against the index's
// children of that directory, recursing only into children the index
// has never seen. Application is therefore order-independent by
// construction and converges to the on-disk truth no matter how the
// underlying events were ordered, merged, or duplicated. That property
// is load-bearing: notification backends like fanotify merge
// same-object events and lose their order (one record can carry
// CREATE|DELETE), so nothing downstream of intake may depend on an op
// code or on arrival order.
//
// fsnotify is used uniformly on every platform, and an fsnotify watch
// covers exactly ONE directory everywhere -- on Linux that is how
// inotify works, and the package deliberately uses the same
// one-watch-per-directory model on the other backends too, so behavior
// never diverges by OS. The Watcher therefore watches every live
// indexed directory (plus the roots), adds watches as directories
// appear, and drops them as directories vanish.
//
// Degradation model: the watcher NEVER fails hard. When the OS refuses
// a watch (e.g. inotify max_user_watches exhausted) the failure is
// counted, logged once, and skipped; when the kernel event queue
// overflows, events were lost, so the watcher marks itself degraded and
// asks the Rescanner for a reconcile rescan. Degraded() and Stats()
// expose the state for the UI.
package watch

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// Options tunes a Watcher's debouncing. The zero value selects the
// defaults; the knobs exist so tests can run with tiny thresholds.
type Options struct {
	// Quiet flushes a pending batch once no event has arrived for this
	// long (default 250ms).
	Quiet time.Duration
	// MaxAge flushes once the OLDEST pending event has waited this
	// long, bounding staleness under a continuous event drizzle
	// (default 1s).
	MaxAge time.Duration
	// MaxPending flushes immediately at this batch size (default 4096).
	MaxPending int
	// OnDegraded, when set, is called exactly once, from a watcher
	// goroutine, at the moment the watcher first becomes degraded (the
	// flag is sticky, so there is no second transition). The Stats
	// snapshot carries the trigger. Implementations must not call back
	// into the Watcher's Stop.
	OnDegraded func(Stats)
}

// Stats is a snapshot of the watcher's health for logs and the UI.
type Stats struct {
	// WatchedDirs is the number of directories currently watched.
	WatchedDirs int
	// DroppedWatches counts directories the OS refused to watch
	// (typically the inotify watch limit). Changes under those
	// directories are only picked up by rescans.
	DroppedWatches int
	// Overflows counts kernel event queue overflows; each one means
	// events were lost and triggered a reconcile rescan request.
	Overflows int
	// Degraded is true once any watch was dropped or any overflow
	// happened: live updates are incomplete and rescans fill the gaps.
	Degraded bool
}

// Watcher keeps the index in sync with the filesystem. Create it with
// New, wire a Rescanner (optional), then Start. All methods are safe
// for concurrent use.
type Watcher struct {
	mgr   *index.Manager
	roots []string
	ex    *index.Excluder
	opt   Options

	// Seams: unit tests swap these for deterministic fakes.
	newNotifier func() (notifier, error)
	readDir     func(string) ([]os.DirEntry, error)

	lc lifecycle

	mu            sync.Mutex
	n             notifier
	watched       map[string]struct{}
	stats         Stats
	loggedDrop    bool
	loggedOverf   bool
	requestRescan func()

	deb debouncer // owned exclusively by the run loop
}

// New creates a Watcher over the manager's index. roots are the
// configured walk roots (watched directly; they have no index entry of
// their own). ex MUST be built from the same exclude patterns as the
// walks -- reusing index.Excluder keeps watch filtering byte-identical
// to walk pruning -- and may be nil to exclude nothing.
func New(m *index.Manager, roots []string, ex *index.Excluder, opt Options) *Watcher {
	if opt.Quiet <= 0 {
		opt.Quiet = defaultQuiet
	}
	if opt.MaxAge <= 0 {
		opt.MaxAge = defaultMaxAge
	}
	if opt.MaxPending <= 0 {
		opt.MaxPending = defaultMaxPending
	}
	return &Watcher{
		mgr:         m,
		roots:       roots,
		ex:          ex,
		opt:         opt,
		newNotifier: newFSNotifier,
		readDir:     os.ReadDir,
		watched:     make(map[string]struct{}),
		deb:         debouncer{quiet: opt.Quiet, maxAge: opt.MaxAge, maxPending: opt.MaxPending},
	}
}

// Start creates the notifier and launches the event loop, which first
// watches the roots plus every live indexed directory and then applies
// events until Stop. It fails if the watcher was already started or
// stopped, or if the OS refuses a notifier instance.
func (w *Watcher) Start() error {
	ctx, err := w.lc.begin()
	if err != nil {
		return err
	}
	n, err := w.newNotifier()
	if err != nil {
		close(w.lc.done) // the loop never runs; keep Stop from blocking
		return err
	}
	w.mu.Lock()
	w.n = n
	w.mu.Unlock()
	go w.run(ctx)
	return nil
}

// Stop shuts the watcher down and blocks until the event loop has
// exited, then closes the notifier. Any still-pending debounced events
// are abandoned (a later rescan reconciles). Stop is idempotent and
// safe to call before Start or while Start's initial watch registration
// is still in progress.
func (w *Watcher) Stop() {
	w.lc.end()
	w.mu.Lock()
	n := w.n
	w.mu.Unlock()
	if n != nil {
		_ = n.Close()
	}
}

// Degraded reports whether live updates are known to be incomplete
// (dropped watches or event overflows). Rescans fill the gaps.
func (w *Watcher) Degraded() bool { return w.Stats().Degraded }

// Stats returns a snapshot of the watcher's health.
func (w *Watcher) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// setRescanRequester wires the degradation callback; NewRescanner calls
// it before either side starts.
func (w *Watcher) setRescanRequester(fn func()) {
	w.mu.Lock()
	w.requestRescan = fn
	w.mu.Unlock()
}

// addWatch starts watching dir and records it in the bookkeeping set;
// a directory that is already bookkept is left alone. A notifier
// failure -- typically the inotify watch limit -- degrades the watcher
// instead of stopping it: the drop is counted, the first one is
// logged, and later ones stay silent so an exhausted limit cannot spam
// the log.
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

func (w *Watcher) watch(dir string, refresh bool) {
	var notify func()
	defer func() { // runs AFTER the unlock below; never under w.mu
		if notify != nil {
			notify()
		}
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil || w.lc.stopping() {
		return
	}
	_, have := w.watched[dir]
	if have && !refresh {
		return
	}
	if err := w.n.Add(dir); err != nil {
		if errors.Is(err, fsnotify.ErrClosed) {
			return // racing Stop, not a degradation
		}
		if have {
			// The refresh proved the recorded watch dead and no new
			// one could be armed: stop claiming it.
			delete(w.watched, dir)
			w.stats.WatchedDirs = len(w.watched)
		}
		notify = w.degradeLocked()
		w.stats.DroppedWatches++
		if !w.loggedDrop {
			w.loggedDrop = true
			log.Printf("watch: adding watch for %s failed: %v (degraded; further drops are counted silently)", dir, err)
		}
		return
	}
	w.watched[dir] = struct{}{}
	w.stats.WatchedDirs = len(w.watched)
}

// degradeLocked flips the sticky Degraded flag and, on the first flip
// only, returns a closure that reports the transition to the
// OnDegraded callback. Callers hold w.mu, mutate the stats counters
// while still holding it, and invoke the closure only after unlocking.
func (w *Watcher) degradeLocked() func() {
	first := !w.stats.Degraded
	w.stats.Degraded = true
	if !first || w.opt.OnDegraded == nil {
		return nil
	}
	return func() { w.opt.OnDegraded(w.Stats()) }
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
	for d := range w.watched {
		if pathWithin(d, path) {
			delete(w.watched, d)
			if w.n != nil {
				_ = w.n.Remove(d)
			}
		}
	}
	w.stats.WatchedDirs = len(w.watched)
}

// syncWatches reconciles the watch set with the CURRENT index contents.
// The Rescanner calls it after every successful BuildFromDisk swap:
// directories that vanished lose their watch, directories that appeared
// gain one. Safe to run concurrently with the event loop.
//
// The watch set can be huge (one entry per indexed directory), so the
// reconcile checks ctx between directories: cancelling it -- the
// Rescanner passes its loop context, which Stop cancels -- abandons the
// rest of the pass promptly instead of holding shutdown hostage to
// millions of notifier calls. A later rescan reconciles whatever was
// left undone.
func (w *Watcher) syncWatches(ctx context.Context) {
	w.mu.Lock()
	ready := w.n != nil && !w.lc.stopping()
	w.mu.Unlock()
	if !ready || ctx.Err() != nil {
		return
	}
	desired := w.desiredDirs(ctx)
	want := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		want[d] = struct{}{}
	}
	w.mu.Lock()
	for d := range w.watched {
		if ctx.Err() != nil {
			break // partial `want` never drops watches: this loop is dead on cancel
		}
		if _, ok := want[d]; !ok {
			delete(w.watched, d)
			_ = w.n.Remove(d)
		}
	}
	w.stats.WatchedDirs = len(w.watched)
	w.mu.Unlock()
	for _, d := range desired {
		if ctx.Err() != nil {
			return
		}
		w.addWatch(d)
	}
}

// desiredDirs returns the watch set implied by the current index: the
// configured roots plus every live directory entry, with exclusions
// filtered out (an excluded directory is never watched -- a root
// matching its own exclude list is a configuration oddity, but the rule
// still holds). Directory paths are collected first and watched after,
// so no syscalls run while the Manager's read lock is held -- and the
// index is enumerated in LiveDirsPage chunks, so the read lock is
// released between pages instead of being held across a full scan of a
// huge index. Cancelling ctx stops the enumeration between pages;
// callers already treat the result as best-effort and re-check ctx
// before acting on it.
func (w *Watcher) desiredDirs(ctx context.Context) []string {
	seen := make(map[string]struct{})
	var dirs []string
	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		dirs = append(dirs, p)
	}
	for _, r := range w.roots {
		if a, err := filepath.Abs(r); err == nil {
			r = a
		}
		r = filepath.Clean(r)
		if !w.ex.Match(filepath.Base(r), r) {
			add(r)
		}
	}
	for start := int32(0); ; {
		page, next := w.mgr.LiveDirsPage(start, index.DefaultLiveDirsPage)
		for _, p := range page {
			if !w.ex.Match(filepath.Base(p), p) {
				add(p)
			}
		}
		if next < 0 || ctx.Err() != nil {
			break
		}
		start = next
	}
	return dirs
}

// pathWithin reports whether path equals base or lies beneath it. Both
// must be clean absolute paths.
func pathWithin(path, base string) bool {
	if len(path) < len(base) || path[:len(base)] != base {
		return false
	}
	return len(path) == len(base) ||
		path[len(base)] == filepath.Separator ||
		strings.HasSuffix(base, string(filepath.Separator))
}
