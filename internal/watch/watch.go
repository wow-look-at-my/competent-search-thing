// Package watch keeps the index live after the initial disk walk: a
// Watcher reconciles filesystem events against the index.Manager, a
// Sweeper walks the indexed directories on a cadence and reconciles
// the ones whose on-disk state moved (the always-on convergence tier),
// and a Rescanner runs full BuildFromDisk rebuilds, both periodically
// and on request.
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
// code or on arrival order -- and the Sweeper feeds the very same
// reconcile with paths that never had an event at all.
//
// fsnotify is used uniformly on every platform, and an fsnotify watch
// covers exactly ONE directory everywhere -- on Linux that is how
// inotify works, and the package deliberately uses the same
// one-watch-per-directory model on the other backends too, so behavior
// never diverges by OS. The Watcher maintains a bounded HOT SET of
// watches (Options.MaxWatches; unlimited resolves to the old
// watch-everything behavior): the configured roots are always watched,
// the remaining budget is filled preferring the user's home subtree,
// recency is tracked LRU-style, and at budget a newly hot directory
// evicts the least-recently-touched one. Directories beyond the budget
// simply stay cold -- no watch syscalls are issued for them -- and the
// Sweeper converges them; tiers differ only in update latency, never
// in final index state.
//
// Degradation model: the watcher NEVER fails hard. When the OS refuses
// a watch (e.g. inotify max_user_watches exhausted) the failure is
// counted, logged once, and skipped; when the kernel event queue
// overflows, events were lost, so the watcher marks itself degraded and
// asks the Sweeper for a reconcile sweep (falling back to a Rescanner
// rescan when no sweeper is wired). Evictions and at-budget cold
// directories are NOT degradation -- they are the hot set working as
// designed. Degraded() and Stats() expose the state for the UI.
package watch

import (
	"container/list"
	"context"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// Options tunes a Watcher. The zero value selects the defaults; the
// knobs exist so tests can run with tiny thresholds.
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
	// MaxWatches bounds the hot set: the number of directories watched
	// concurrently. 0 = auto (linux: min(max_user_watches/2, 65536)
	// read from /proc/sys/fs/inotify/max_user_watches, floor 1024;
	// non-linux, or when the limit cannot be read, there is no
	// effective limit and every indexed directory is watched -- the
	// pre-budget behavior). Negative = explicitly unlimited, the same
	// watch-everything behavior. Directories beyond the budget stay
	// cold: no watch syscalls are issued for them, they are never
	// counted as drops and never degrade the watcher, and the sweep
	// tier converges them.
	MaxWatches int
	// OnDegraded, when set, is called exactly once, from a watcher
	// goroutine, at the moment the watcher first becomes degraded (the
	// flag is sticky, so there is no second transition). The Stats
	// snapshot carries the trigger. Implementations must not call back
	// into the Watcher's Stop.
	OnDegraded func(Stats)
}

// Stats is a snapshot of the watcher's health for logs and the UI.
type Stats struct {
	// Backend names the notification backend feeding the watcher. The
	// constructor sets "inotify" (fsnotify's Linux backend and the
	// uniform one-watch-per-directory model everywhere); a future
	// backend sets its own name.
	Backend string
	// Budget is the resolved MaxWatches cap (math.MaxInt when
	// unlimited); 0 until Start resolved it.
	Budget int
	// WatchedDirs is the number of directories currently watched.
	WatchedDirs int
	// IndexedDirs is the size of the desired watch set at the last
	// registration or resync pass: the configured roots plus every
	// live, non-excluded indexed directory. Under a budget,
	// WatchedDirs stays at or below it while IndexedDirs keeps
	// counting everything.
	IndexedDirs int
	// DroppedWatches counts directories the OS refused to watch
	// (typically the inotify watch limit) -- strictly refusals, never
	// budget decisions. Changes under those directories are picked up
	// by sweeps and rescans.
	DroppedWatches int
	// Evictions counts hot-set evictions: watches released to make
	// room for hotter directories at budget. Evictions are normal
	// operation under a budget, NOT degradation.
	Evictions int
	// Overflows counts kernel event queue overflows; each one means
	// events were lost and triggered a reconcile sweep request (or a
	// rescan request when no sweeper is wired).
	Overflows int
	// Degraded is true once any watch was dropped or any overflow
	// happened: live updates are incomplete and sweeps/rescans fill
	// the gaps.
	Degraded bool
}

// Watcher keeps the index in sync with the filesystem. Create it with
// New, wire a Sweeper and/or Rescanner (optional), then Start. All
// methods are safe for concurrent use.
type Watcher struct {
	mgr *index.Manager
	ex  *index.Excluder
	opt Options

	// rootList holds the configured roots, normalized (absolute,
	// clean) and exclude-filtered; pinned is the same set for O(1)
	// membership. Pinned roots are always watched -- even beyond the
	// budget -- and never evicted.
	rootList []string
	pinned   map[string]struct{}

	// Seams: unit tests swap these for deterministic fakes.
	newNotifier    func() (notifier, error)
	readDir        func(string) ([]os.DirEntry, error)
	readMaxWatches func() int             // raw kernel watch limit; <= 0 = unknown
	homeDir        func() (string, error) // the priority-fill home subtree

	lc lifecycle

	// initialDone is closed once the initial registration pass has
	// finished or was aborted by Stop (never closed when Start itself
	// failed).
	initialDone chan struct{}

	mu            sync.Mutex
	n             notifier
	budget        int
	watched       map[string]*list.Element // dir -> LRU element; nil element = pinned root
	lru           *list.List               // evictable watched dirs; front = hottest
	stats         Stats
	loggedDrop    bool
	loggedOverf   bool
	requestRescan func()
	requestSweep  func()

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
	w := &Watcher{
		mgr:            m,
		ex:             ex,
		opt:            opt,
		pinned:         make(map[string]struct{}),
		newNotifier:    newFSNotifier,
		readDir:        os.ReadDir,
		readMaxWatches: readInotifyMaxWatches,
		homeDir:        os.UserHomeDir,
		initialDone:    make(chan struct{}),
		watched:        make(map[string]*list.Element),
		lru:            list.New(),
		deb:            debouncer{quiet: opt.Quiet, maxAge: opt.MaxAge, maxPending: opt.MaxPending},
	}
	w.stats.Backend = "inotify"
	for _, r := range roots {
		if a, err := filepath.Abs(r); err == nil {
			r = a
		}
		r = filepath.Clean(r)
		if _, dup := w.pinned[r]; dup || ex.Match(filepath.Base(r), r) {
			continue // an excluded root is never watched (configuration oddity)
		}
		w.pinned[r] = struct{}{}
		w.rootList = append(w.rootList, r)
	}
	return w
}

// resolveBudget turns Options.MaxWatches into the effective hot-set
// cap. Explicit positives are taken as-is; negatives are explicitly
// unlimited; 0 is auto: half the kernel's per-user inotify watch
// allowance capped at 65536 (floor 1024) so one app never hogs the
// whole per-user budget, or unlimited where no limit is readable
// (non-linux -- watch-everything remains the behavior there).
func resolveBudget(maxWatches int, readMax func() int) int {
	switch {
	case maxWatches > 0:
		return maxWatches
	case maxWatches < 0:
		return math.MaxInt
	}
	raw := 0
	if readMax != nil {
		raw = readMax()
	}
	if raw <= 0 {
		return math.MaxInt
	}
	b := raw / 2
	if b > 65536 {
		b = 65536
	}
	if b < 1024 {
		b = 1024
	}
	return b
}

// readInotifyMaxWatches reads the kernel's per-user inotify watch
// limit; <= 0 means unknown (non-linux, or /proc unreadable).
func readInotifyMaxWatches() int {
	if runtime.GOOS != "linux" {
		return 0
	}
	data, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// Start creates the notifier, resolves the watch budget, and launches
// the event loop, which first fills the watch set (roots, then the
// home subtree, then everything else, up to the budget) and then
// applies events until Stop. It fails if the watcher was already
// started or stopped, or if the OS refuses a notifier instance.
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
	w.budget = resolveBudget(w.opt.MaxWatches, w.readMaxWatches)
	w.stats.Budget = w.budget
	w.mu.Unlock()
	go w.run(ctx)
	return nil
}

// Stop shuts the watcher down and blocks until the event loop has
// exited, then closes the notifier. Any still-pending debounced events
// are abandoned (a later sweep or rescan reconciles). Stop is
// idempotent and safe to call before Start or while Start's initial
// watch registration is still in progress.
func (w *Watcher) Stop() {
	w.lc.end()
	w.mu.Lock()
	n := w.n
	w.mu.Unlock()
	if n != nil {
		_ = n.Close()
	}
}

// InitialRegistration returns a channel that is closed once the
// initial watch-registration pass has finished (or was aborted by
// Stop) -- the point where Stats' WatchedDirs and IndexedDirs are
// real. It is never closed when Start itself failed.
func (w *Watcher) InitialRegistration() <-chan struct{} { return w.initialDone }

// Degraded reports whether live updates are known to be incomplete
// (dropped watches or event overflows). Sweeps and rescans fill the
// gaps.
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

// setSweepRequester wires the overflow -> sweep request path;
// NewSweeper calls it before either side starts. When set, it takes
// precedence over the rescan requester for overflow recovery: a
// shallow sweep converges lost events far cheaper than a full
// rebuild.
func (w *Watcher) setSweepRequester(fn func()) {
	w.mu.Lock()
	w.requestSweep = fn
	w.mu.Unlock()
}

// excluder exposes the watcher's exclude filter to the sweep tier
// (nil is a valid Excluder that matches nothing).
func (w *Watcher) excluder() *index.Excluder { return w.ex }

// budgetVal returns the resolved watch budget (0 before Start).
func (w *Watcher) budgetVal() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.budget
}

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
	var notify func()
	defer func() {
		if notify != nil {
			notify()
		}
	}()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n == nil || w.lc.stopping() {
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
	ready := w.n != nil && !w.lc.stopping()
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
// the roots and the entries beyond the caps. The index is enumerated
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
			if w.ex.Match(filepath.Base(p), p) {
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
