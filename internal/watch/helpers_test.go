package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// fakeNotifier is the deterministic notifier for unit tests: Add
// failures are scripted per path, and the test pushes events and errors
// through buffered channels. Configure addErr/addDelay BEFORE the
// watcher starts.
type fakeNotifier struct {
	events   chan fsnotify.Event
	errs     chan error
	addErr   func(path string) error
	addDelay time.Duration

	mu      sync.Mutex
	watched map[string]bool
	removed []string
	closed  bool
}

func newFakeNotifier() *fakeNotifier {
	return &fakeNotifier{
		events:  make(chan fsnotify.Event, 512),
		errs:    make(chan error, 16),
		watched: make(map[string]bool),
	}
}

func (f *fakeNotifier) Add(path string) error {
	if f.addDelay > 0 {
		time.Sleep(f.addDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return fsnotify.ErrClosed
	}
	if f.addErr != nil {
		if err := f.addErr(path); err != nil {
			return err
		}
	}
	f.watched[path] = true
	return nil
}

func (f *fakeNotifier) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, path)
	if !f.watched[path] {
		return fsnotify.ErrNonExistentWatch
	}
	delete(f.watched, path)
	return nil
}

func (f *fakeNotifier) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeNotifier) Errors() <-chan error          { return f.errs }

func (f *fakeNotifier) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.events)
		close(f.errs)
	}
	return nil
}

// has reports whether path currently holds a fake watch.
func (f *fakeNotifier) has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watched[path]
}

// unwatch silently forgets a watch, simulating the kernel auto-dropping
// it when the directory is deleted; the next Remove for it errors.
func (f *fakeNotifier) unwatch(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watched, path)
}

// send pushes one synthetic event into the watcher.
func (f *fakeNotifier) send(op fsnotify.Op, path string) {
	f.events <- fsnotify.Event{Name: path, Op: op}
}

// fastOptions are debounce thresholds small enough to keep tests quick
// while still exercising real batching.
func fastOptions() Options {
	return Options{Quiet: 20 * time.Millisecond, MaxAge: 200 * time.Millisecond, MaxPending: 4096}
}

// buildManager creates a Manager over root and runs the initial walk.
func buildManager(t *testing.T, root string, excludes []string) *index.Manager {
	t.Helper()
	m := index.NewManager([]string{root}, excludes, 500)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	return m
}

// newTestWatcher builds a Watcher over the manager's configuration with
// fast debounce thresholds; a non-nil notifier n is injected through
// the seam.
func newTestWatcher(t *testing.T, m *index.Manager, n notifier) *Watcher {
	t.Helper()
	ex, err := index.NewExcluder(m.Excludes())
	require.NoError(t, err)
	w := New(m, m.Roots(), ex, fastOptions())
	if n != nil {
		w.newNotifier = func() (notifier, error) { return n, nil }
	}
	return w
}

// startWatcher starts w and registers its Stop as cleanup.
func startWatcher(t *testing.T, w *Watcher) {
	t.Helper()
	require.NoError(t, w.Start())
	t.Cleanup(w.Stop)
}

// waitFor polls cond with a CI-generous deadline.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 20*time.Second, 5*time.Millisecond, msg)
}

// hasPath reports whether the index currently answers a query for the
// path's base name with exactly this path.
func hasPath(m *index.Manager, path string) bool {
	for _, r := range m.Query(filepath.Base(path), 0) {
		if r.Path == path {
			return true
		}
	}
	return false
}

// settle proves the watcher has drained everything sent so far: it
// creates a unique marker file in dir and waits for it to reach the
// index. Dirty paths reconcile in first-arrival order and the marker's
// path is unique (never already pending), so once the marker is
// visible every earlier event has been applied too. With a fake
// notifier the marker event is pushed through it; with f == nil the
// real notifier is expected to pick the file up on its own.
func settle(t *testing.T, m *index.Manager, f *fakeNotifier, dir string) {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprintf("marker-%d.settle", time.Now().UnixNano()))
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	if f != nil {
		f.send(fsnotify.Create, p)
	}
	waitFor(t, func() bool { return hasPath(m, p) }, "settle marker must reach the index")
}

// mkTree creates a nested directory tree under root: every entry in
// layout is either "dir/" (trailing separator) or a file path, relative
// to root. Returns the absolute paths keyed by the relative ones.
func mkTree(t *testing.T, root string, layout ...string) map[string]string {
	t.Helper()
	abs := make(map[string]string, len(layout))
	for _, rel := range layout {
		p := filepath.Join(root, rel)
		if len(rel) > 0 && rel[len(rel)-1] == '/' {
			require.NoError(t, os.MkdirAll(p, 0o755))
		} else {
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		}
		abs[rel] = p
	}
	return abs
}
