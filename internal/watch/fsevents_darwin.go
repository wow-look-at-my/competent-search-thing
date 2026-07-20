//go:build darwin

package watch

/*
#cgo LDFLAGS: -framework CoreServices
#include <stdint.h>
#include <stdlib.h>
#include "fsevents_darwin.h"
*/
import "C"

import (
	"errors"
	"log"
	"path/filepath"
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/fsnotify/fsnotify"
)

// fseventsNotifier implements the notifier seam over ONE FSEvents
// stream covering the configured roots: recursive by design, a
// handful of file descriptors total, every directory under the roots
// covered -- the darwin counterpart of the linux fanotify backend
// (and the fix for the per-directory kqueue model's
// one-fd-per-dir-plus-one-per-child-file cost, which pinned real
// machines at their fd ceiling). Requires no privileges.
//
// Event flow: the stream's callback (fsevents_darwin.c) forwards each
// batch to the exported goFSEventsCallback trampoline, keyed by a
// cgo.Handle; handleBatch runs every record through the pure
// fseDecide (fsevents_events.go) -- overflow flags degrade the
// watcher via the fsnotify overflow sentinel, surviving paths are
// translated back to the configured root spelling and emitted as
// advisory fsnotify.Create events (the op is advisory by the package
// event model: reconcile lstats the path and decides).
//
// Teardown ordering (Close): mark closed under the mutex (straggler
// callbacks turn into no-ops), stop+invalidate+release the stream (no
// NEW callbacks), drain the serial queue (in-flight callbacks
// finish), release the queue, and only then delete the cgo.Handle --
// so a callback can never dereference a dead handle -- and close the
// channels so the watcher's run loop exits.
type fseventsNotifier struct {
	roots []string
	tr    fsePathTranslator

	events chan fsnotify.Event
	errs   chan error

	mu     sync.Mutex
	closed bool

	stream C.FSEventStreamRef
	queue  C.dispatch_queue_t
	handle cgo.Handle
}

// fseLatencySec is the FSEvents latency parameter: how long the OS
// may coalesce events per path before delivering. 0.3s keeps
// callback volume low on busy systems while staying well inside the
// watcher's own debounce window (~250ms quiet / 1s max), so
// user-visible latency stays ~1s like the other live backends.
const fseLatencySec = 0.3

// newFSEventsNotifier builds the production FSEvents notifier: the
// stream watches each root's SYMLINK-RESOLVED spelling (FSEvents
// reports resolved paths; /tmp, /var, /etc are symlinks into
// /private) while the translator maps event paths back to the
// configured spelling so the index never forks. Construction fails
// only when the stream cannot be created or started; the auto
// selection then falls back to per-directory kqueue watches.
func newFSEventsNotifier(roots []string) (notifier, error) {
	if len(roots) == 0 {
		return nil, errors.New("fsevents: no roots configured")
	}
	n := &fseventsNotifier{
		roots:  append([]string(nil), roots...),
		events: make(chan fsnotify.Event, fseEventBuf),
		errs:   make(chan error, fseErrBuf),
	}
	streamPaths := make([]string, len(roots))
	for i, r := range roots {
		streamPaths[i] = r
		if resolved, err := filepath.EvalSymlinks(r); err == nil && resolved != r {
			streamPaths[i] = resolved
			n.tr.add(resolved, r)
		}
		// EvalSymlinks failure: stream the root verbatim; FSEvents
		// tolerates not-yet-existing paths (events start once it
		// exists).
	}

	cpaths := make([]*C.char, len(streamPaths))
	for i, p := range streamPaths {
		cpaths[i] = C.CString(p)
	}
	defer func() {
		for _, cp := range cpaths {
			C.free(unsafe.Pointer(cp))
		}
	}()

	queue := C.csFSEQueueCreate()
	if queue == nil {
		return nil, errors.New("fsevents: cannot create the dispatch queue")
	}
	n.handle = cgo.NewHandle(n)
	stream := C.csFSEStart(C.uintptr_t(n.handle), &cpaths[0],
		C.int(len(cpaths)), C.double(fseLatencySec), queue)
	if stream == nil {
		n.handle.Delete()
		C.csFSEQueueRelease(queue)
		return nil, errors.New("fsevents: stream create/start failed")
	}
	n.stream = stream
	n.queue = queue
	return n, nil
}

// goFSEventsCallback is the exported trampoline the C callback
// invokes on the stream's dispatch queue: it copies the batch into Go
// memory and hands it to handleBatch. The cgo.Handle is valid for the
// whole callback lifetime by Close's ordering (delete only after
// stop + drain).
//
//export goFSEventsCallback
func goFSEventsCallback(h C.uintptr_t, numEvents C.size_t, cpaths **C.char, cflags *C.uint) {
	n, ok := cgo.Handle(h).Value().(*fseventsNotifier)
	if !ok {
		return
	}
	count := int(numEvents)
	if count <= 0 || cpaths == nil {
		return
	}
	paths := make([]string, count)
	flags := make([]uint32, count)
	pathSlice := unsafe.Slice(cpaths, count)
	for i := 0; i < count; i++ {
		paths[i] = C.GoString(pathSlice[i])
	}
	if cflags != nil {
		flagSlice := unsafe.Slice(cflags, count)
		for i := 0; i < count; i++ {
			flags[i] = uint32(flagSlice[i])
		}
	}
	n.handleBatch(paths, flags)
}

// handleBatch runs one callback batch through fseDecide and delivers
// the survivors, mirroring the fanotify deliver() semantics: at most
// one synthesized overflow signal per batch (from overflow flags OR a
// full events channel), advisory Create ops, non-blocking sends.
func (n *fseventsNotifier) handleBatch(paths []string, flags []uint32) {
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return
	}
	overflowSent := false
	for i, p := range paths {
		var fl uint32
		if i < len(flags) {
			fl = flags[i]
		}
		emit, overflow, ok := fseDecide(p, fl, n.roots, n.tr)
		if overflow && !overflowSent {
			overflowSent = true
			n.sendOverflow()
		}
		if !ok {
			continue
		}
		select {
		case n.events <- fsnotify.Event{Name: emit, Op: fsnotify.Create}:
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
func (n *fseventsNotifier) sendOverflow() {
	select {
	case n.errs <- fsnotify.ErrEventOverflow:
	default:
	}
}

// kind implements backendInfo: fsevents covers every directory under
// the roots recursively, so the Watcher skips per-directory watch
// bookkeeping entirely (the fanotify wideCoverage semantics).
func (n *fseventsNotifier) kind() (string, bool) { return "fsevents", true }

// Add is a no-op: the stream already covers every directory under the
// roots. (The Watcher does not call it under wideCoverage; the no-op
// keeps the seam honest anyway.) Remove likewise.
func (n *fseventsNotifier) Add(string) error    { return nil }
func (n *fseventsNotifier) Remove(string) error { return nil }

func (n *fseventsNotifier) Events() <-chan fsnotify.Event { return n.events }
func (n *fseventsNotifier) Errors() <-chan error          { return n.errs }

// Close is idempotent; see the type comment for the load-bearing
// teardown order.
func (n *fseventsNotifier) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	stream, queue := n.stream, n.queue
	n.stream = nil
	n.queue = nil
	n.mu.Unlock()
	if stream != nil {
		C.csFSEStop(stream)
	}
	if queue != nil {
		C.csFSEQueueDrain(queue)
		C.csFSEQueueRelease(queue)
	}
	if n.handle != 0 {
		n.handle.Delete()
		n.handle = 0
	}
	close(n.events)
	close(n.errs)
	return nil
}

// newFSEventsFn is the FSEvents constructor the auto and strict
// selections probe -- a package seam so unit tests can script
// success/failure without touching FSEvents (the newFanotifyFn
// pattern). Production never swaps it.
var newFSEventsFn = newFSEventsNotifier

// newAutoNotifier picks the darwin production notifier: the FSEvents
// whole-filesystem stream, else the per-directory kqueue backend --
// behavior is identical either way (the contract every tier shares),
// only latency and file-descriptor cost differ. This is the
// watcher.backend="auto" (and unset) behavior; see newBackendNotifier.
func newAutoNotifier(roots []string) func() (notifier, error) {
	return func() (notifier, error) {
		n, err := newFSEventsFn(roots)
		if err == nil {
			return n, nil
		}
		log.Printf("watch: fsevents unavailable (%v); falling back to per-directory kqueue watches", err)
		return newFSNotifier()
	}
}

// newStrictFSEventsNotifier is the watcher.backend="fsevents" path on
// darwin: FSEvents or NOTHING. When the stream cannot start the
// watcher gets the no-op "none" notifier instead of a kqueue
// fallback -- the config demanded whole-filesystem coverage, so live
// watching is plainly DISABLED, announced loudly here, and the sweep
// tier keeps the index converging.
func newStrictFSEventsNotifier(roots []string) func() (notifier, error) {
	return func() (notifier, error) {
		n, err := newFSEventsFn(roots)
		if err == nil {
			return n, nil
		}
		log.Printf("watch: backend \"fsevents\" required by config but unavailable (%v); live watching DISABLED, sweeps keep the index converging", err)
		return newNoopNotifier(), nil
	}
}

// newStrictFanotifyNotifier on darwin: fanotify does not exist here,
// so the strict watcher.backend="fanotify" mode always resolves to
// the no-op "none" notifier -- announced loudly, never a silent
// fallback (text identical to the generic non-linux twin in
// fanotify_other.go); the sweep tier keeps the index converging.
func newStrictFanotifyNotifier([]string) func() (notifier, error) {
	return func() (notifier, error) {
		log.Printf("watch: backend \"fanotify\" required by config but unavailable (fanotify is linux-only); live watching DISABLED, sweeps keep the index converging")
		return newNoopNotifier(), nil
	}
}
