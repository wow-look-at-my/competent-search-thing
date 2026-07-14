package watch

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// degradedRecorder collects OnDegraded callback invocations.
type degradedRecorder struct {
	mu    sync.Mutex
	calls []Stats
}

func (r *degradedRecorder) record(s Stats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *degradedRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *degradedRecorder) last() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

// newDegradedWatcher wires a Watcher whose Options carry the recorder's
// OnDegraded callback and whose notifier is the fake.
func newDegradedWatcher(t *testing.T, m *index.Manager, n notifier, rec *degradedRecorder) *Watcher {
	t.Helper()
	ex, err := index.NewExcluder(m.Excludes())
	require.NoError(t, err)
	opt := fastOptions()
	opt.OnDegraded = rec.record
	w := New(m, m.Roots(), ex, opt)
	w.newNotifier = func() (notifier, error) { return n, nil }
	return w
}

func TestOnDegradedFiresOnceForDroppedWatch(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "a/", "b/", "c/")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	f.addErr = func(path string) error {
		if strings.HasSuffix(path, "a") || strings.HasSuffix(path, "b") {
			return errors.New("no space left on watch table")
		}
		return nil
	}
	rec := &degradedRecorder{}
	w := newDegradedWatcher(t, m, f, rec)
	startWatcher(t, w)

	waitFor(t, func() bool { return w.Stats().DroppedWatches == 2 },
		"both scripted Add failures are counted")
	require.Equal(t, 1, rec.count(), "OnDegraded fires exactly once, not per drop")
	require.True(t, rec.last().Degraded)
	require.GreaterOrEqual(t, rec.last().DroppedWatches, 1, "snapshot carries at least the triggering drop")
}

func TestOnDegradedFiresOnceForOverflow(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	rec := &degradedRecorder{}
	w := newDegradedWatcher(t, m, f, rec)
	startWatcher(t, w)

	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool { return w.Stats().Overflows == 1 }, "first overflow lands")
	waitFor(t, func() bool { return rec.count() == 1 }, "OnDegraded fires for the first overflow")
	require.Equal(t, 1, rec.last().Overflows, "snapshot taken after the counter moved")

	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool { return w.Stats().Overflows == 2 }, "second overflow lands")
	require.Equal(t, 1, rec.count(), "degraded is sticky: no second callback")
}

func TestOnDegradedNotCalledWhenHealthy(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "a/", "a/f.txt")
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	rec := &degradedRecorder{}
	w := newDegradedWatcher(t, m, f, rec)
	startWatcher(t, w)

	settle(t, m, f, root)
	require.Zero(t, rec.count(), "healthy watcher never reports degradation")
	require.False(t, w.Degraded())
}

func TestNilOnDegradedIsSafe(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)

	f := newFakeNotifier()
	f.addErr = func(string) error { return errors.New("refused") }
	w := newTestWatcher(t, m, f) // no OnDegraded configured
	startWatcher(t, w)

	f.errs <- fsnotify.ErrEventOverflow
	waitFor(t, func() bool {
		s := w.Stats()
		return s.Degraded && s.Overflows == 1
	}, "degradation with a nil callback must not panic")
}
