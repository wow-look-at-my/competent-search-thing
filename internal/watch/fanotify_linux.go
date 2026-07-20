package watch

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// fanotifyNotifier implements the notifier seam over one fanotify
// group with FAN_MARK_FILESYSTEM marks: a single mark per superblock
// delivers named CREATE/DELETE/MOVED_FROM/MOVED_TO (+ONDIR) for EVERY
// directory of that filesystem -- no per-directory watches, no
// max_user_watches ceiling, microsecond registration (proven in
// docs/dev/watcher-redesign/spike-results.md). Requirements:
// CAP_SYS_ADMIN in the initial user namespace for the marks,
// CAP_DAC_READ_SEARCH for open_by_handle_at, and a non-null
// filesystem fsid; the constructor fails cleanly otherwise and
// newAutoNotifier falls back to per-directory fsnotify.
//
// Event flow: the kernel reports (parent-directory file handle, entry
// name); the read loop routes the handle to the right superblock by
// fsid, resolves it to the parent's CURRENT absolute path
// (open_by_handle_at + readlink /proc/self/fd/N), joins the name, and
// emits an advisory fsnotify.Create event -- the Watcher's reconcile
// lstats the path and decides, so merged masks (one record can carry
// CREATE|DELETE) and lost ordering cost nothing. A resolution failure
// (ESTALE: the parent is already gone) drops the event; the subtree's
// own delete events or a sweep converge it. Whole-filesystem marks see
// paths OUTSIDE the configured roots too; those are dropped here so
// the index's scope never widens.
//
// All syscall touchpoints sit behind the seam fields, so the routing,
// dedup, overflow, and shutdown logic is unit-tested without
// privileges; the production closures live at the bottom of the file.
type fanotifyNotifier struct {
	// Seams (production defaults set by newFanotifyNotifier; tests
	// construct the struct with fakes and call start directly).
	initFn    func() (int, error)
	markFn    func(fd int, path string) error
	readFn    func(fd int, buf []byte) (int, error)
	resolveFn func(mountFD int, handleType int32, handle []byte) (string, error)
	fsidFn    func(path string) (fanoFsid, error)
	mountsFn  func(roots []string) []string

	// roots is the index scope: events resolving outside every root
	// are dropped (normalized by the Watcher before construction).
	roots []string

	fd     int
	events chan fsnotify.Event
	errs   chan error
	// stop is closed by Close before waiting for the read loop, so
	// scripted readFns can unblock; the production readFn is woken
	// through the stop pipe instead (a poll cannot select on a
	// channel).
	stop         chan struct{}
	done         chan struct{}
	stopR, stopW *os.File

	mu     sync.Mutex
	closed bool
	mounts map[fanoFsid]fanoMount
}

// fanoMount is one covered superblock: an O_PATH fd on its mountpoint
// for open_by_handle_at (a handle only resolves against a mount fd of
// its OWN filesystem -- a wrong-fs fd is ESTALE every time).
type fanoMount struct {
	fd   int
	path string
}

const (
	// fanoReadBufSize fits thousands of events per read (the spike
	// drained 1000 creates, 64 bytes each, in one such read).
	fanoReadBufSize = 256 * 1024
	// fanoEventBuf/fanoErrBuf bound the delivery channels: a full
	// events channel drops the event and synthesizes one overflow
	// error -- kernel-queue semantics in miniature; better an overflow
	// signal driving a sweep than an unbounded queue or a blocked
	// read loop.
	fanoEventBuf = 1024
	fanoErrBuf   = 16
	// fanoMarkMask is the dirent mask: the four parent-side entry
	// events plus FAN_ONDIR (without it mkdir/rmdir are silently
	// absent). FAN_RENAME is deliberately unused: MOVED_FROM/TO as
	// independent dirty paths converge identically under
	// reconcile-by-lstat, and the index has no move primitive.
	fanoMarkMask = unix.FAN_CREATE | unix.FAN_DELETE | unix.FAN_MOVED_FROM |
		unix.FAN_MOVED_TO | unix.FAN_ONDIR
)

// errFanoClosed is the quiet-shutdown sentinel the readFn returns once
// Close asked the loop to exit.
var errFanoClosed = errors.New("fanotify notifier closed")

// newFanotifyNotifier builds the production whole-filesystem notifier:
// one fanotify group, the filesystem of every configured root marked
// (ANY root-mark failure -- EPERM without CAP_SYS_ADMIN, ENODEV on
// null-fsid filesystems, EXDEV on btrfs subvolume mixes -- fails the
// constructor so the caller falls back cleanly), then a best-effort
// mark per additional real mountpoint under the roots (a mount that
// cannot be marked is logged once and left to sweeps: coverage holds,
// only latency differs).
func newFanotifyNotifier(roots []string) (notifier, error) {
	stopR, stopW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	n := &fanotifyNotifier{
		initFn:    fanoInit,
		markFn:    fanoMark,
		readFn:    fanoReadFn(stopR),
		resolveFn: fanoResolve,
		fsidFn:    statfsFsid,
		mountsFn:  index.RealMountpoints,
		stopR:     stopR,
		stopW:     stopW,
	}
	if err := n.start(roots); err != nil {
		stopR.Close()
		stopW.Close()
		return nil, err
	}
	return n, nil
}

// start initializes the group, marks the roots (all-or-nothing) and
// the extra mountpoints (best-effort), and launches the read loop.
func (n *fanotifyNotifier) start(roots []string) error {
	n.events = make(chan fsnotify.Event, fanoEventBuf)
	n.errs = make(chan error, fanoErrBuf)
	n.stop = make(chan struct{})
	n.done = make(chan struct{})
	n.mounts = make(map[fanoFsid]fanoMount)
	n.roots = append([]string(nil), roots...)
	fd, err := n.initFn()
	if err != nil {
		return fmt.Errorf("fanotify init: %w", err)
	}
	n.fd = fd
	for _, r := range roots {
		if err := n.cover(r); err != nil {
			n.destroy()
			return fmt.Errorf("fanotify mark %s: %w", r, err)
		}
	}
	if n.mountsFn != nil {
		for _, mp := range n.mountsFn(roots) {
			if err := n.cover(mp); err != nil {
				log.Printf("watch: fanotify cannot cover %s (%v); sweeps cover it", mp, err)
			}
		}
	}
	go n.readLoop()
	return nil
}

// destroy releases the fds of a constructor that failed before the
// read loop started (Close handles every later teardown).
func (n *fanotifyNotifier) destroy() {
	unix.Close(n.fd)
	for _, mt := range n.mounts {
		unix.Close(mt.fd)
	}
	n.mounts = nil
}

// cover marks the filesystem containing path and registers an O_PATH
// mount fd plus its fsid for event routing. A filesystem whose fsid is
// already covered is a no-op (bind mounts and multiple roots on one
// superblock cost one mark).
func (n *fanotifyNotifier) cover(path string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.coverLocked(path)
}

func (n *fanotifyNotifier) coverLocked(path string) error {
	fsid, err := n.fsidFn(path)
	if err != nil {
		return err
	}
	if _, ok := n.mounts[fsid]; ok {
		return nil
	}
	if err := n.markFn(n.fd, path); err != nil {
		return err
	}
	mfd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err // marked but unroutable: its events drop, sweeps cover
	}
	n.mounts[fsid] = fanoMount{fd: mfd, path: path}
	return nil
}

// MarkMount extends coverage to a mountpoint that appeared after
// construction. The sweeper's mount-diff calls it (through the
// Watcher) for mountpoints newly under the roots, before
// force-reconciling them -- so events flow from the new filesystem by
// the time its content is indexed. Not part of the notifier interface;
// the Watcher reaches it via a type assertion.
func (n *fanotifyNotifier) MarkMount(path string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return errFanoClosed
	}
	return n.coverLocked(path)
}

// kind implements backendInfo: fanotify covers whole filesystems, so
// the Watcher skips per-directory watch bookkeeping entirely.
func (n *fanotifyNotifier) kind() (string, bool) { return "fanotify", true }

// Add is a no-op: FAN_MARK_FILESYSTEM already covers every directory
// of the marked superblocks, so there is nothing to add per directory.
// (The Watcher does not call it under wideCoverage; the no-op keeps
// the seam honest anyway.) Remove likewise.
func (n *fanotifyNotifier) Add(string) error    { return nil }
func (n *fanotifyNotifier) Remove(string) error { return nil }

func (n *fanotifyNotifier) Events() <-chan fsnotify.Event { return n.events }
func (n *fanotifyNotifier) Errors() <-chan error          { return n.errs }

// Close is idempotent: it wakes the read loop (the stop channel for
// scripted readFns, the pipe for the production poll), waits for it to
// exit, then releases every fd and closes the channels.
func (n *fanotifyNotifier) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	close(n.stop)
	if n.stopW != nil {
		n.stopW.Close()
	}
	n.mu.Unlock()
	<-n.done
	unix.Close(n.fd)
	if n.stopR != nil {
		n.stopR.Close()
	}
	n.mu.Lock()
	for _, mt := range n.mounts {
		unix.Close(mt.fd)
	}
	n.mounts = nil
	n.mu.Unlock()
	close(n.events)
	close(n.errs)
	return nil
}

func (n *fanotifyNotifier) isClosed() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.closed
}

// mountFor routes an event's fsid to the mount fd its handle resolves
// against.
func (n *fanotifyNotifier) mountFor(fsid fanoFsid) (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	mt, ok := n.mounts[fsid]
	return mt.fd, ok
}

// withinRoots reports whether the resolved path lies inside the index
// scope. Whole-filesystem marks see everything on the superblock;
// nothing outside the configured roots may reach the Watcher, whose
// reconcile would otherwise index it.
func (n *fanotifyNotifier) withinRoots(path string) bool {
	for _, r := range n.roots {
		if pathWithin(path, r) {
			return true
		}
	}
	return false
}

// readLoop reads, parses, and delivers until Close (or the event
// source dying, which is surfaced as an overflow: live updates
// stopped, exactly what that signal means to the Watcher -- degrade
// and sweep).
func (n *fanotifyNotifier) readLoop() {
	defer close(n.done)
	buf := make([]byte, fanoReadBufSize)
	for {
		nr, err := n.readFn(n.fd, buf)
		if err != nil || nr <= 0 {
			if err != nil && !errors.Is(err, errFanoClosed) && !errors.Is(err, io.EOF) && !n.isClosed() {
				log.Printf("watch: fanotify read failed: %v; live events stopped (sweeps keep converging)", err)
				n.sendOverflow()
			}
			return
		}
		evs, overflow := parseFanotifyBuf(buf[:nr])
		if overflow {
			n.sendOverflow()
		}
		n.deliver(evs)
	}
}

// fanoResolveKey dedupes handle resolution within one batch: many
// events share a parent directory (a burst of creates), and one
// open_by_handle_at per unique (fsid, handle) covers them all. The
// cache is deliberately per-batch only -- handles are inode identity,
// and a cross-batch cache needs rename/delete invalidation to stay
// truthful (noted as a follow-up; correctness first).
type fanoResolveKey struct {
	fsid   fanoFsid
	handle string
}

// deliver resolves each unique parent handle once, joins the entry
// names, and emits one advisory Create event per surviving entry (the
// op is advisory by the package's event model: reconcile lstats the
// path and decides).
func (n *fanotifyNotifier) deliver(evs []fanoEvent) {
	resolved := make(map[fanoResolveKey]*string, 4)
	overflowSent := false
	for _, ev := range evs {
		k := fanoResolveKey{ev.Fsid, string(ev.Handle)}
		path, seen := resolved[k]
		if !seen {
			if mfd, ok := n.mountFor(ev.Fsid); ok {
				if p, err := n.resolveFn(mfd, ev.HandleType, ev.Handle); err == nil {
					path = &p
				}
				// Resolution failure (ESTALE: the parent is already
				// gone) drops this handle's events; the subtree's own
				// delete events or a sweep converge it.
			}
			resolved[k] = path
		}
		if path == nil {
			continue
		}
		name := ev.Name
		if strings.ContainsRune(name, os.PathSeparator) || name == ".." {
			continue // the kernel never reports these; guard crafted input
		}
		full := filepath.Join(*path, name)
		if !n.withinRoots(full) {
			continue // superblock-wide events; the index scope is the roots
		}
		select {
		case n.events <- fsnotify.Event{Name: full, Op: fsnotify.Create}:
		default:
			if !overflowSent {
				overflowSent = true
				n.sendOverflow()
			}
		}
	}
}

// sendOverflow reuses the fsnotify overflow sentinel so the Watcher's
// handleError works unchanged; a full errs channel drops the signal
// (one queued overflow already triggers the sweep).
func (n *fanotifyNotifier) sendOverflow() {
	select {
	case n.errs <- fsnotify.ErrEventOverflow:
	default:
	}
}

// newFanotifyFn is the fanotify constructor the auto and strict
// selections probe -- a package seam so unit tests can script
// success/failure without CAP_SYS_ADMIN. Production never swaps it.
var newFanotifyFn = newFanotifyNotifier

// newAutoNotifier picks the production notifier for the configured
// roots: fanotify whole-filesystem marks when the kernel, privileges,
// and filesystems allow, else the per-directory fsnotify backend --
// behavior is identical either way (the contract every tier shares),
// only event latency and syscall count differ. This is the
// watcher.backend="auto" (and unset) behavior; see newBackendNotifier.
func newAutoNotifier(roots []string) func() (notifier, error) {
	return func() (notifier, error) {
		n, err := newFanotifyFn(roots)
		if err == nil {
			return n, nil
		}
		log.Printf("watch: fanotify unavailable (%v); falling back to per-directory inotify watches", err)
		return newFSNotifier()
	}
}

// newStrictFanotifyNotifier is the watcher.backend="fanotify" path:
// fanotify or NOTHING. When the whole-filesystem backend cannot start
// (a missing CAP_SYS_ADMIN grant being the usual reason) the watcher
// gets the no-op "none" notifier instead of an inotify fallback -- the
// config demanded no per-directory watches, so live watching is
// plainly DISABLED, announced loudly here, and the sweep tier keeps
// the index converging on its interval (the tier contract holds:
// identical final index state, sweep-bounded latency).
func newStrictFanotifyNotifier(roots []string) func() (notifier, error) {
	return func() (notifier, error) {
		n, err := newFanotifyFn(roots)
		if err == nil {
			return n, nil
		}
		log.Printf("watch: backend \"fanotify\" required by config but unavailable (%v); live watching DISABLED, sweeps keep the index converging", err)
		return newNoopNotifier(), nil
	}
}
