package app

// The pre-build watch registration tests: startEarlyWatch's deferred
// arming, startWatch's adoption of the armed watcher, and the
// teardown paths that stop it (mid-build config apply; the cancelled
// build's cleanup is asserted in watch_test.go beside the other
// cancel pins). Split from watch_test.go for the file-length cap (the
// hotset.go precedent); the shared newTestApp constructor lives in
// app_test.go.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

func TestBuildIndexArmsWatchBeforeWalkAndAdoptsIt(t *testing.T) {
	// The startup-ordering contract: the live-watch backend is armed
	// BEFORE the initial walk (the armed line precedes the build
	// completion line), and the completion path ADOPTS that very
	// watcher instance instead of building a second one.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{WatchBackend: "inotify"})
	t.Cleanup(func() { a.Shutdown(context.Background()) })

	// Arm exactly as buildIndex's first step does, and hold the
	// pointer; buildIndex's own startEarlyWatch is then the no-op
	// re-entry and the completion path must adopt THIS instance.
	a.startEarlyWatch()
	a.watchMu.Lock()
	armed := a.earlyWatcher
	a.watchMu.Unlock()
	require.NotNil(t, armed, "the pre-build watcher is armed and stored")

	a.buildIndex(context.Background())
	require.True(t, watchUp(a), "the trio is up after the build")
	a.watchMu.Lock()
	adopted := a.watcher
	leftover := a.earlyWatcher
	a.watchMu.Unlock()
	require.Same(t, armed, adopted, "startWatch adopts the pre-build watcher, never a second instance")
	require.Nil(t, leftover, "adoption clears the early slot")

	out := buf.String()
	armedAt := strings.Index(out, "armed before the initial index build; changes during indexing are queued")
	builtAt := strings.Index(out, "index: 1 entries in")
	require.GreaterOrEqual(t, armedAt, 0, "the pre-build arming is announced")
	require.GreaterOrEqual(t, builtAt, 0, "the build completion is logged")
	require.Less(t, armedAt, builtAt, "registration is announced before the walk completes")
	require.NotContains(t, out, "live updates unavailable",
		"the adopted watcher is live; the failure wording never fires")

	// The adopted watcher applies events live after the build.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "post-build.txt"), []byte("x"), 0o644))
	require.Eventually(t, func() bool { return len(a.Search("post-build")) == 1 },
		20*time.Second, 10*time.Millisecond, "live updates flow through the adopted watcher")
}

func TestRestartIndexLayerMidBuildStopsEarlyWatcher(t *testing.T) {
	// A config apply while the initial build is walking: the pre-build
	// watcher runs the PREVIOUS configuration, so the applier stops it
	// and lets the completion path build a fresh one from the new
	// values (plus the queued convergence rescan).
	m := index.NewManager([]string{t.TempDir()}, nil, 0)
	a, _ := newTestApp(t, m, Options{WatchBackend: "inotify"})
	a.startEarlyWatch()
	a.watchMu.Lock()
	ew := a.earlyWatcher
	_, cancel := context.WithCancel(context.Background())
	a.buildCancel = cancel
	a.buildFinished = false
	a.watchMu.Unlock()
	require.NotNil(t, ew)
	t.Cleanup(cancel)

	next := config.Default()
	next.Roots = []string{t.TempDir()}
	require.NoError(t, a.restartIndexLayer(&next))

	a.watchMu.Lock()
	cleared := a.earlyWatcher == nil
	flagged := a.rescanOnWatchUp
	a.watchMu.Unlock()
	require.True(t, cleared, "the stale-config early watcher is detached")
	require.True(t, flagged, "the convergence rescan stays armed")
	select {
	case <-ew.InitialRegistration():
		// Stop unwound the held loop; the registration channel closing
		// proves the teardown completed.
	case <-time.After(20 * time.Second):
		t.Fatal("the detached early watcher was not stopped")
	}
}
