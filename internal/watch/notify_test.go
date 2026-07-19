package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNoopNotifierIsInert(t *testing.T) {
	n := newNoopNotifier()
	bi, ok := n.(backendInfo)
	require.True(t, ok, "the none backend must report its name to Stats")
	name, wide := bi.kind()
	require.Equal(t, "none", name)
	require.True(t, wide, "no per-directory watch set exists when live watching is off")

	require.NoError(t, n.Add("/anywhere"))
	require.NoError(t, n.Remove("/anywhere"))
	select {
	case ev := <-n.Events():
		t.Fatalf("the none backend delivered an event: %v", ev)
	case err := <-n.Errors():
		t.Fatalf("the none backend delivered an error: %v", err)
	default:
	}

	require.NoError(t, n.Close())
	require.NoError(t, n.Close(), "Close is idempotent")
	_, open := <-n.Events()
	require.False(t, open, "Close closes the events channel so the run loop exits")
	_, open = <-n.Errors()
	require.False(t, open, "Close closes the errors channel too")
}

func TestNewBackendNotifierInotifyPinsFSNotify(t *testing.T) {
	n, err := newBackendNotifier("inotify", []string{t.TempDir()})()
	require.NoError(t, err)
	require.NotNil(t, n)
	_, isInfo := n.(backendInfo)
	require.False(t, isInfo, "the pinned inotify backend is plain per-directory fsnotify")
	require.NoError(t, n.Close())
}

func TestNewBackendNotifierDefaultsToAuto(t *testing.T) {
	// "", "auto", and unrecognized values (config normalization
	// canonicalizes upstream; the watch layer stays lenient) all take
	// the automatic path, which always yields a working notifier.
	for _, backend := range []string{"", "auto", "bogus"} {
		n, err := newBackendNotifier(backend, []string{t.TempDir()})()
		require.NoError(t, err, "backend %q", backend)
		require.NotNil(t, n, "backend %q", backend)
		require.NoError(t, n.Close())
	}
}

func TestWatcherNoneBackendWatchesNothingAndSweepsConverge(t *testing.T) {
	// The strict-mode outcome end to end: a watcher running on the
	// no-op "none" notifier reports backend "none" with zero
	// watched/indexed counts, live events never flow -- and the sweep
	// tier still converges the index to the on-disk truth, so the tier
	// contract (identical final state, latency only) holds with live
	// watching off.
	root := t.TempDir()
	mkTree(t, root, "docs/", "docs/a.txt")
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, nil)
	w.newNotifier = func() (notifier, error) { return newNoopNotifier(), nil }
	startWatcherRegistered(t, w)

	st := w.Stats()
	require.Equal(t, "none", st.Backend)
	require.Zero(t, st.WatchedDirs, "nothing is watched")
	require.Zero(t, st.IndexedDirs, "no desired watch set is even counted")
	require.False(t, st.Degraded, "off-by-config is not degradation")

	// A change on disk: no live event can deliver it...
	created := filepath.Join(root, "docs", "later.txt")
	require.NoError(t, os.WriteFile(created, []byte("x"), 0o644))
	require.Never(t, func() bool { return hasPath(m, created) },
		300*time.Millisecond, 20*time.Millisecond, "no live path exists to pick the file up")

	// ...but one sweep pass reconciles it into the index.
	s := newTestSweeper(t, m, w, SweepOptions{})
	startSweeper(t, s)
	sweepOnce(t, s)
	require.True(t, hasPath(m, created), "sweeps converge the index with live watching off")
}
