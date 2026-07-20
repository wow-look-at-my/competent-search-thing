//go:build darwin

package watch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// The darwin CI job runs this file against REAL FSEvents -- the
// integration twin the linux fanotify backend cannot have in CI
// (fanotify needs CAP_SYS_ADMIN; FSEvents needs no privileges at
// all). t.TempDir() lives under /var/folders with /var symlinked into
// /private, so the resolved-vs-configured path translation is
// exercised by construction.

// collectUntil drains n's channels until an event for want arrives
// (or the deadline fails the test), returning everything seen.
func collectUntil(t *testing.T, n notifier, want string, deadline time.Duration) []fsnotify.Event {
	t.Helper()
	var seen []fsnotify.Event
	timeout := time.After(deadline)
	for {
		select {
		case ev, ok := <-n.Events():
			require.True(t, ok, "events channel closed while waiting for %s", want)
			seen = append(seen, ev)
			if ev.Name == want {
				return seen
			}
		case err := <-n.Errors():
			t.Fatalf("unexpected notifier error: %v", err)
		case <-timeout:
			t.Fatalf("no event for %s within %s (saw %v)", want, deadline, seen)
		}
	}
}

func TestFSEventsRealDelivery(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // a sibling the stream must never report

	nIf, err := newFSEventsNotifier([]string{root})
	require.NoError(t, err)
	n, ok := nIf.(*fseventsNotifier)
	require.True(t, ok)
	t.Cleanup(func() { _ = n.Close() })

	name, wide := n.kind()
	require.Equal(t, "fsevents", name)
	require.True(t, wide, "fsevents is whole-filesystem coverage: no per-directory watch set")
	require.NoError(t, n.Add("/anywhere"), "Add is a wide-backend no-op")
	require.NoError(t, n.Remove("/anywhere"))

	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), nil, 0o644))
	inRoot := filepath.Join(root, "hello.txt")
	require.NoError(t, os.WriteFile(inRoot, nil, 0o644))

	seen := collectUntil(t, n, inRoot, 20*time.Second)
	for _, ev := range seen {
		require.True(t, pathWithin(ev.Name, root),
			"every delivered path carries the CONFIGURED root spelling (got %q, root %q)", ev.Name, root)
		require.NotContains(t, ev.Name, "outside.txt", "out-of-root events never surface")
	}

	// A removal dirties the same path again (reconcile-by-lstat
	// settles the outcome; the notifier only promises the dirty path).
	require.NoError(t, os.Remove(inRoot))
	collectUntil(t, n, inRoot, 20*time.Second)

	require.NoError(t, n.Close())
	require.NoError(t, n.Close(), "Close is idempotent")
	_, open := <-n.Events()
	require.False(t, open, "Close closes the events channel so the run loop exits")
	_, open = <-n.Errors()
	require.False(t, open, "Close closes the errors channel too")
}

func TestFSEventsWatcherConvergence(t *testing.T) {
	// The tier contract end to end on the real backend: identical
	// final index state, wide coverage, zero per-directory watches.
	root := t.TempDir()
	paths := mkTree(t, root, "docs/", "docs/keep.txt", "olddir/", "olddir/deep.txt")
	m := buildManager(t, root, nil)
	ex, err := index.NewExcluder(m.Excludes())
	require.NoError(t, err)
	opt := fastOptions()
	opt.Backend = "fsevents"
	w := New(m, m.Roots(), ex, opt)
	startWatcherRegistered(t, w)

	st := w.Stats()
	require.Equal(t, "fsevents", st.Backend)
	require.Zero(t, st.WatchedDirs, "wide coverage keeps the hot set empty")
	require.Zero(t, st.IndexedDirs, "no desired watch set is even counted")

	// (a) A created file reaches the index.
	created := filepath.Join(root, "docs", "created-live.txt")
	require.NoError(t, os.WriteFile(created, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, created) }, "create picked up")

	// (b) The recursive payoff: a directory chain born AFTER Start
	// delivers from its depths without any per-directory watch adds.
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	leaf := filepath.Join(deep, "leaf.txt")
	require.NoError(t, os.WriteFile(leaf, nil, 0o644))
	waitFor(t, func() bool { return hasPath(m, leaf) }, "born-after-start depths delivered")
	require.Zero(t, w.Stats().WatchedDirs, "still no per-directory watches")

	// (c) A removed subtree leaves the index.
	require.NoError(t, os.RemoveAll(paths["olddir/"]))
	waitFor(t, func() bool { return !hasPath(m, paths["olddir/deep.txt"]) }, "subtree removal converges")

	// (d) A renamed directory converges both spellings.
	renamed := filepath.Join(root, "docs-renamed")
	require.NoError(t, os.Rename(paths["docs/"], renamed))
	movedKeep := filepath.Join(renamed, "keep.txt")
	waitFor(t, func() bool { return hasPath(m, movedKeep) && !hasPath(m, paths["docs/keep.txt"]) },
		"dir rename: old spelling gone, new spelling indexed")

	require.False(t, w.Stats().Degraded)
}

func TestFSEventsSelectionScripted(t *testing.T) {
	orig := newFSEventsFn
	t.Cleanup(func() { newFSEventsFn = orig })

	// auto: a constructor failure falls back to per-directory kqueue,
	// labeled honestly.
	newFSEventsFn = func([]string) (notifier, error) { return nil, errors.New("scripted failure") }
	n, err := newAutoNotifier([]string{t.TempDir()})()
	require.NoError(t, err)
	bi, ok := n.(backendInfo)
	require.True(t, ok)
	name, wide := bi.kind()
	require.Equal(t, "kqueue", name, "the per-directory fallback is labeled by its real OS backend")
	require.False(t, wide)
	require.NoError(t, n.Close())

	// strict fsevents: failure means the loud no-op "none", never a
	// kqueue fallback.
	n, err = newStrictFSEventsNotifier([]string{t.TempDir()})()
	require.NoError(t, err)
	name, wide = n.(backendInfo).kind()
	require.Equal(t, "none", name)
	require.True(t, wide)
	require.NoError(t, n.Close())

	// strict fanotify on darwin: always the no-op "none" -- and it
	// never even probes fsevents (the config demanded fanotify).
	probed := false
	newFSEventsFn = func([]string) (notifier, error) { probed = true; return nil, errors.New("x") }
	n, err = newStrictFanotifyNotifier([]string{t.TempDir()})()
	require.NoError(t, err)
	name, wide = n.(backendInfo).kind()
	require.Equal(t, "none", name)
	require.True(t, wide)
	require.False(t, probed, "strict fanotify never probes fsevents")
	require.NoError(t, n.Close())
}

func TestFSEventsHandleBatch(t *testing.T) {
	// The Go-side batch path, scripted (no stream): scope filtering,
	// translation, noise drop, and the full-channel overflow synth.
	n := &fseventsNotifier{
		roots:  []string{"/r"},
		events: make(chan fsnotify.Event, 2),
		errs:   make(chan error, 1),
	}
	n.tr.add("/private/r", "/r")

	n.handleBatch(
		[]string{"/r/a", "/r/noise.log", "/outside/x", "/private/r/b", "/r/c"},
		[]uint32{fseItemCreated, fseItemModified, fseItemCreated, fseItemCreated, fseItemCreated},
	)
	require.Equal(t, "/r/a", (<-n.events).Name)
	require.Equal(t, "/r/b", (<-n.events).Name, "the translated spelling is delivered")
	select {
	case err := <-n.errs:
		require.ErrorIs(t, err, fsnotify.ErrEventOverflow, "a full events channel synthesizes one overflow")
	default:
		t.Fatal("expected an overflow signal for the dropped event")
	}
	require.Empty(t, n.events, "the modified-only, out-of-root, and overflowed events never queue")

	// Overflow FLAGS degrade too (and still emit their path), and a
	// record beyond the flags slice fails open into a dirty path.
	n2 := &fseventsNotifier{roots: []string{"/r"}, events: make(chan fsnotify.Event, 8), errs: make(chan error, 1)}
	n2.handleBatch([]string{"/r/deep", "/r/bare"}, []uint32{fseMustScanSubDirs})
	require.ErrorIs(t, <-n2.errs, fsnotify.ErrEventOverflow)
	require.Equal(t, "/r/deep", (<-n2.events).Name, "MustScanSubDirs still reconciles its subtree root shallowly")
	require.Equal(t, "/r/bare", (<-n2.events).Name, "missing flags fail open")

	// A closed notifier ignores straggler batches entirely.
	n2.closed = true
	n2.handleBatch([]string{"/r/late"}, []uint32{fseItemCreated})
	require.Empty(t, n2.events)
}

func TestReadFDLimit(t *testing.T) {
	require.Positive(t, readFDLimit(), "the soft RLIMIT_NOFILE is always readable on darwin")
}
