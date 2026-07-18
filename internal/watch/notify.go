package watch

import (
	"sync"

	"github.com/fsnotify/fsnotify"
)

// notifier is the minimal seam between the Watcher and fsnotify. The
// real implementation wraps *fsnotify.Watcher; unit tests inject a fake
// that returns scripted Add errors (the watch-limit path), pushes
// overflow errors, and delivers synthetic event sequences
// deterministically.
type notifier interface {
	// Add starts watching one directory. Watches are NOT recursive on
	// any platform (see the package comment), so callers add one watch
	// per directory.
	Add(path string) error
	// Remove stops watching one directory. Removing a watch that is
	// already gone (e.g. the kernel dropped it when the directory was
	// deleted) returns an error, which callers ignore.
	Remove(path string) error
	// Events delivers filesystem events until Close.
	Events() <-chan fsnotify.Event
	// Errors delivers watcher-level errors, e.g. the kernel event queue
	// overflowing (fsnotify.ErrEventOverflow).
	Errors() <-chan error
	// Close releases all watches; the Events and Errors channels close.
	Close() error
}

// backendInfo is an optional notifier extension: a backend that is
// not the default per-directory model reports its name (surfaced as
// Stats.Backend) and whether it covers whole filesystems without
// per-directory watches. wideCoverage makes the Watcher skip the
// hot-set fill and all watch bookkeeping -- there is nothing to add,
// evict, or budget when the kernel already reports every directory.
// Notifiers without the method default to "inotify" + per-directory
// semantics.
type backendInfo interface {
	kind() (name string, wideCoverage bool)
}

// fsnotifier adapts *fsnotify.Watcher to the notifier seam.
type fsnotifier struct {
	w *fsnotify.Watcher
}

// newFSNotifier creates the production notifier. It can fail when the
// OS refuses another watcher instance (e.g. inotify
// max_user_instances).
func newFSNotifier() (notifier, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifier{w: w}, nil
}

func (f *fsnotifier) Add(path string) error         { return f.w.Add(path) }
func (f *fsnotifier) Remove(path string) error      { return f.w.Remove(path) }
func (f *fsnotifier) Events() <-chan fsnotify.Event { return f.w.Events }
func (f *fsnotifier) Errors() <-chan error          { return f.w.Errors }
func (f *fsnotifier) Close() error                  { return f.w.Close() }

// noopNotifier is the "none" backend: it accepts everything and
// delivers nothing. The strict fanotify mode (Options.Backend =
// "fanotify") installs it when the required backend cannot start, so
// the Watcher still runs -- keeping the sweep and rescan wiring alive
// -- while live watching is plainly OFF: Stats().Backend reports
// "none", and wideCoverage keeps the hot-set fill and every
// per-directory watch call a no-op, so WatchedDirs stays 0 and nothing
// pretends to be covered. The index converges through sweeps only.
type noopNotifier struct {
	events chan fsnotify.Event
	errs   chan error
	once   sync.Once
}

func newNoopNotifier() notifier {
	return &noopNotifier{
		events: make(chan fsnotify.Event),
		errs:   make(chan error),
	}
}

func (n *noopNotifier) Add(string) error    { return nil }
func (n *noopNotifier) Remove(string) error { return nil }

// Events and Errors return open channels nothing ever writes to; Close
// closes them so the Watcher's run loop exits like any other backend.
func (n *noopNotifier) Events() <-chan fsnotify.Event { return n.events }
func (n *noopNotifier) Errors() <-chan error          { return n.errs }

func (n *noopNotifier) Close() error {
	n.once.Do(func() {
		close(n.events)
		close(n.errs)
	})
	return nil
}

// kind implements backendInfo: "none" is wide in the only sense that
// matters to the Watcher -- there is no per-directory watch set to
// fill, count, or budget (here because live watching is off, not
// because the kernel covers everything; addInitialWatches keys its log
// line off the name).
func (n *noopNotifier) kind() (string, bool) { return "none", true }

// newBackendNotifier resolves Options.Backend to the production
// notifier constructor: "inotify" pins the per-directory fsnotify
// backend (the fanotify probe is skipped entirely); "fanotify" is
// STRICT -- fanotify or the no-op "none" notifier, never a silent
// inotify fallback (newStrictFanotifyNotifier, per-OS); anything else
// ("", "auto", unrecognized values -- config normalization canonicalizes
// upstream) is the automatic pick with its clean fanotify-to-fsnotify
// fallback.
func newBackendNotifier(backend string, roots []string) func() (notifier, error) {
	switch backend {
	case "inotify":
		return func() (notifier, error) { return newFSNotifier() }
	case "fanotify":
		return newStrictFanotifyNotifier(roots)
	default:
		return newAutoNotifier(roots)
	}
}
