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
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// WatchEx, when non-nil, excludes directories from LIVE WATCHING
	// only: a matching directory -- and everything beneath it, the
	// same subtree coverage the walk excluder gets from pruning --
	// never gets a per-directory watch: not at the initial fill, not
	// via reconcile/sweep promotion, not at a resync refill. The
	// subtree stays fully indexed and fully swept, so changes inside
	// it converge within one sweep interval instead of ~1s. Distinct
	// from the walk Excluder passed to New, which keeps paths out of
	// the INDEX entirely. Under a wideCoverage backend (fanotify
	// whole-filesystem marks) there are no per-directory watches to
	// withhold, so events from matching directories still flow --
	// watch excludes shed watch cost, they never add staleness beyond
	// the sweep bound.
	WatchEx *index.Excluder
	// OnDegraded, when set, is called exactly once, from a watcher
	// goroutine, at the moment the watcher first becomes degraded (the
	// flag is sticky, so there is no second transition). The Stats
	// snapshot carries the trigger. Implementations must not call back
	// into the Watcher's Stop.
	OnDegraded func(Stats)
}

// Stats is a snapshot of the watcher's health for logs and the UI.
type Stats struct {
	// Backend names the notification backend feeding the watcher:
	// "inotify" for the per-directory fsnotify model (the uniform
	// default everywhere), "fanotify" when Start detected the
	// whole-filesystem backend (linux with CAP_SYS_ADMIN and
	// markable filesystems; see backendInfo).
	Backend string
	// Budget is the resolved MaxWatches cap (math.MaxInt when
	// unlimited); 0 until Start resolved it.
	Budget int
	// WatchedDirs is the number of directories currently watched.
	WatchedDirs int
	// IndexedDirs is the size of the desired watch set at the last
	// registration or resync pass: the configured roots plus every
	// live indexed directory that is neither walk-excluded nor
	// watch-excluded (Options.WatchEx). Under a budget, WatchedDirs
	// stays at or below it while IndexedDirs keeps counting
	// everything desired.
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

	mu     sync.Mutex
	n      notifier
	budget int
	// wide is true when the notifier reported wideCoverage (fanotify
	// whole-filesystem marks): the hot set is not filled, watch
	// bookkeeping stays empty, and every per-directory watch call is
	// a cheap no-op. Set once in Start, before the event loop runs.
	wide          bool
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
		if _, dup := w.pinned[r]; dup || ex.Match(filepath.Base(r), r) || w.watchExcluded(r) {
			continue // an excluded root is never watched (configuration oddity)
		}
		w.pinned[r] = struct{}{}
		w.rootList = append(w.rootList, r)
	}
	// The production constructor auto-detects the backend for these
	// normalized roots (fanotify whole-filesystem marks with a clean
	// fallback to per-directory fsnotify); it needs the roots, so it
	// is bound after the loop above. Unit tests swap the seam.
	w.newNotifier = newAutoNotifier(w.rootList)
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
	if bi, ok := n.(backendInfo); ok {
		name, wide := bi.kind()
		w.stats.Backend = name
		w.wide = wide
	}
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

// watchExcluded reports whether path is excluded from live watching
// (Options.WatchEx; nil matches nothing). A match on the path OR any
// ancestor counts: the walk excluder gets subtree coverage for free
// by pruning, while watch-excluded subtrees remain fully indexed, so
// the subtree semantics must be reproduced by walking up here. It
// gates only the watch-issuing paths -- indexing, reconcile, and
// sweeps never consult it.
func (w *Watcher) watchExcluded(path string) bool {
	if w.opt.WatchEx == nil {
		return false // the common case costs one nil check
	}
	for p := path; ; {
		if w.opt.WatchEx.Match(filepath.Base(p), p) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// budgetVal returns the resolved watch budget (0 before Start).
func (w *Watcher) budgetVal() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.budget
}

// isWide reports whether the notifier covers whole filesystems
// (fanotify), making per-directory watch bookkeeping moot.
func (w *Watcher) isWide() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wide
}

// markMount forwards a newly-appeared mountpoint to a notifier that
// can extend whole-filesystem coverage (fanotify's MarkMount); the
// sweeper's mount-diff calls it before force-reconciling the
// mountpoint, so events flow from the new filesystem by the time its
// content is indexed. Per-directory backends have no such method and
// need nothing: their watches attach as reconcile descends. A failed
// mark costs latency, never coverage (the reconcile still runs and
// sweeps keep converging the subtree), so it is logged and tolerated.
func (w *Watcher) markMount(path string) {
	w.mu.Lock()
	n := w.n
	w.mu.Unlock()
	mm, ok := n.(interface{ MarkMount(string) error })
	if !ok {
		return
	}
	if err := mm.MarkMount(path); err != nil {
		log.Printf("watch: fanotify cannot cover %s (%v); sweeps cover it", path, err)
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
