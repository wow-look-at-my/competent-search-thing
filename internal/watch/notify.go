package watch

import "github.com/fsnotify/fsnotify"

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
