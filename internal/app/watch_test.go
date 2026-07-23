package app

// The watch-layer wiring tests: buildIndex + startWatch + the backend
// notice / grant-hint plumbing (watch.go in this package), split out
// of app_test.go for the file-length cap (the hotset.go precedent).
// The shared newTestApp constructor and seamRecorder live in
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

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/watch"
)

func TestStartupKicksOffIndexBuildAndEmitsProgress(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world.md"), []byte("x"), 0o644))

	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{})
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return m.LiveCount() == 2 },
		5*time.Second, 10*time.Millisecond, "background build fills the index")

	// The final progress callback (done=true) reaches the frontend.
	require.Eventually(t, func() bool {
		for _, e := range r.emitted(eventIndexProgress) {
			if p, ok := e.payload[0].(indexProgress); ok && p.Done {
				return p.Indexed == 2 && p.Seconds >= 0
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "done progress event carries the totals")

	// A second Startup (e.g. context refresh) must not rebuild.
	a.Startup(context.Background())
	require.Len(t, a.Search("hello"), 1)
}

// watchUp reports whether the live-update layer has been installed.
func watchUp(a *App) bool {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	return a.watcher != nil && a.rescanner != nil && a.sweeper != nil
}

func TestStartupBringsUpWatcherAndAppliesEvents(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond, "watch layer comes up after the initial build")

	// End to end: a file created NOW reaches Search via fsnotify.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "live-created.txt"), []byte("x"), 0o644))
	require.Eventually(t, func() bool { return len(a.Search("live-created")) == 1 },
		20*time.Second, 10*time.Millisecond, "live update flows through to Search")

	a.Shutdown(context.Background())
	a.Shutdown(context.Background()) // idempotent
	require.False(t, watchUp(a), "shutdown tears the watch layer down")
}

func TestShutdownBeforeBuildFinishesSkipsWatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})
	a.Shutdown(context.Background()) // before Startup: sets the flag, stops nothing
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return m.LiveCount() == 1 },
		20*time.Second, 10*time.Millisecond, "build still completes")
	require.Never(t, func() bool { return watchUp(a) },
		400*time.Millisecond, 20*time.Millisecond, "watch layer never starts after Shutdown")
}

func TestStartWatchToleratesBadExcluder(t *testing.T) {
	// A malformed exclude pattern cannot panic startWatch; the watcher
	// runs with a nil Excluder (excludes nothing).
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a, _ := newTestApp(t, m, Options{})
	a.startWatch()
	require.True(t, watchUp(a))
	a.Shutdown(context.Background())
}

func TestStartWatchLogsTierSummary(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)

	// Pin the per-directory backend: auto would pick a wide backend
	// where one is available (fsevents on the darwin CI job), whose
	// summary legitimately reads 0/0. The LABEL is per-OS honest
	// ("inotify" linux, "kqueue" darwin), so build the expectation
	// from the real one.
	a, _ := newTestApp(t, m, Options{RescanEvery: 45 * time.Minute, WatchBackend: "inotify"})
	a.startWatch() // waits for the initial registration, so the numbers are real
	require.True(t, watchUp(a))
	out := buf.String()
	require.Contains(t, out, "watch: backend "+watch.PerDirBackendName()+": 1/1 dirs live-watched (budget ",
		"the summary announces the tier with real registration numbers and the honest per-OS label")
	require.Contains(t, out, "); sweep interval 20m0s; full rescan interval 45m0s")
	a.Shutdown(context.Background())

	// A zero rescan interval is announced as "off", never as 0s.
	buf.Reset()
	m2 := index.NewManager([]string{t.TempDir()}, nil, 0)
	_, _, err = m2.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	a2, _ := newTestApp(t, m2, Options{})
	a2.startWatch()
	require.Contains(t, buf.String(), "full rescan interval off")
	a2.Shutdown(context.Background())
}

func TestStartWatchWiresWatcherConfig(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skipme"), 0o755))
	m := index.NewManager([]string{dir}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)

	a, _ := newTestApp(t, m, Options{
		WatchMaxWatches: 7,
		SweepInterval:   45 * time.Minute,
		WatchExcludes:   []string{"skipme"},
		// Pin the per-directory backend so the 1/1 assertion below
		// holds on the darwin CI job too (auto would pick the wide
		// fsevents backend there, which watches 0/0 by design).
		WatchBackend: "inotify",
	})
	a.startWatch()
	require.True(t, watchUp(a))
	out := buf.String()
	require.Contains(t, out, "(budget 7)", "watcher.maxWatches reaches the watch layer")
	require.Contains(t, out, "sweep interval 45m0s", "watcher.sweepMinutes reaches the sweeper")
	require.Contains(t, out, ": 1/1 dirs live-watched",
		"the watch-excluded dir is neither watched nor part of the desired set (root only)")
	a.Shutdown(context.Background())
}

func TestStartWatchToleratesBadWatchExcludes(t *testing.T) {
	// A malformed watcher.watchExcludes pattern costs the feature, not
	// the watch layer: logged, then everything watches as usual.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	m := index.NewManager([]string{t.TempDir()}, nil, 0)
	a, _ := newTestApp(t, m, Options{WatchExcludes: []string{"["}})
	a.startWatch()
	require.True(t, watchUp(a))
	require.Contains(t, buf.String(), "bad watcher.watchExcludes patterns")
	a.Shutdown(context.Background())
}

func TestStartWatchSweepDisabledLogsLoudly(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	m := index.NewManager([]string{t.TempDir()}, nil, 0)
	_, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	a, _ := newTestApp(t, m, Options{SweepDisabled: true})
	a.startWatch()

	a.watchMu.Lock()
	w, r, s := a.watcher, a.rescanner, a.sweeper
	a.watchMu.Unlock()
	require.NotNil(t, w, "the watcher still runs")
	require.NotNil(t, r, "the rescanner still runs")
	require.Nil(t, s, "no sweeper is created when sweeps are disabled")
	out := buf.String()
	require.Contains(t, out,
		"watch: sweeps disabled in config; directories without live watches converge only at full rescans (!rescan or rescanIntervalMinutes)",
		"disabling the convergence tier is announced loudly")
	require.Contains(t, out, "sweep interval disabled", "the summary reflects the sweep state")
	a.Shutdown(context.Background())
}

func TestBuildIndexLogsAndSurvivesFailure(t *testing.T) {
	// A malformed exclude pattern makes BuildFromDisk fail; buildIndex
	// must swallow it (log only), never panic.
	m := index.NewManager([]string{t.TempDir()}, []string{"["}, 0)
	a, _ := newTestApp(t, m, Options{})
	a.buildIndex(context.Background())
	require.Equal(t, 0, m.LiveCount())
}

func TestBuildIndexCancelledDiscardsPartialAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "never-indexed.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.buildIndex(ctx)

	require.Contains(t, buf.String(), "index: initial build cancelled")
	require.NotContains(t, buf.String(), "initial build failed")
	require.NotContains(t, buf.String(), "index: startup complete:",
		"the startup summary never fires on the cancelled path")
	require.Equal(t, 0, m.LiveCount(), "the partial store is discarded, never swapped in")
	require.False(t, watchUp(a), "a cancelled build never starts the watch layer")
	a.watchMu.Lock()
	early := a.earlyWatcher
	a.watchMu.Unlock()
	require.Nil(t, early, "the cancelled path stops and detaches the pre-build watcher")
}

func TestShutdownCancelsInitialBuild(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "one.txt"), []byte("x"), 0o644))
	a, _ := newTestApp(t, index.NewManager([]string{dir}, nil, 0), Options{})
	a.Startup(context.Background())

	// Startup stored the build walk's cancel func; wrap it to observe
	// the call (the tiny build may already have finished -- cancelling
	// a finished build's context is a harmless no-op, the point is that
	// Shutdown pulls the trigger at all).
	called := make(chan struct{})
	a.watchMu.Lock()
	orig := a.buildCancel
	a.buildCancel = func() { orig(); close(called) }
	a.watchMu.Unlock()
	require.NotNil(t, orig, "Startup wires a cancellable context into the initial build")

	a.Shutdown(context.Background())
	select {
	case <-called:
	default:
		t.Fatal("Shutdown did not cancel the initial build context")
	}

	a.watchMu.Lock()
	cleared := a.buildCancel == nil
	a.watchMu.Unlock()
	require.True(t, cleared, "Shutdown clears the stored cancel func")
}

func TestEmitDegradedPayload(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.emitDegraded(watch.Stats{WatchedDirs: 7, DroppedWatches: 3, Overflows: 2, Degraded: true})

	events := r.emitted(eventWatchDegraded)
	require.Len(t, events, 1)
	require.Equal(t, watchDegraded{Watched: 7, Dropped: 3, Overflows: 2}, events[0].payload[0])
}

func TestWatchBackendForPayloads(t *testing.T) {
	require.Equal(t, watchBackend{Backend: "fanotify", Full: true, Hint: ""},
		watchBackendFor("fanotify"), "full coverage carries no hint")
	require.Equal(t, watchBackend{Backend: "fsevents", Full: true, Hint: ""},
		watchBackendFor("fsevents"), "the darwin wide backend is full coverage too")
	require.Equal(t, watchBackend{
		Backend: "inotify",
		Full:    false,
		Hint:    "Partial file watching: changes outside the hot set appear within the sweep interval. Enable full coverage: see README (fanotify).",
	}, watchBackendFor("inotify"))
	require.Equal(t, watchBackend{
		Backend: "kqueue",
		Full:    false,
		Hint:    "Partial file watching: changes outside the hot set appear within the sweep interval. The fsevents backend provides full coverage on macOS: check the startup log for why it is not active (watcher.backend in config.json).",
	}, watchBackendFor("kqueue"), "the darwin per-directory fallback points at fsevents, not setcap")
	require.Equal(t, watchBackend{
		Backend: "windows",
		Full:    false,
		Hint:    hintPartialWatch,
	}, watchBackendFor("windows"), "unknown per-directory labels take the generic partial hint")
	require.Equal(t, watchBackend{
		Backend: "none",
		Full:    false,
		Hint:    "Live file watching is off (the configured backend is required but unavailable). The index refreshes on sweeps only.",
	}, watchBackendFor("none"))
}

func TestStartWatchEmitsBackendNoticeAndGrantHint(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{WatchBackend: "inotify"})
	// Pin the OS: the grant hint is linux-only and this suite also
	// runs on the darwin CI job.
	a.plat.goos = "linux"
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return len(r.emitted(eventWatchBackend)) == 1 },
		20*time.Second, 10*time.Millisecond, "the backend notice is emitted once the watch layer is up")
	// The pinned per-directory backend reports the honest per-OS
	// label ("inotify" on the linux job, "kqueue" on the darwin one),
	// each with its matching hint.
	require.Equal(t, watchBackendFor(watch.PerDirBackendName()),
		r.emitted(eventWatchBackend)[0].payload[0])
	// The seam path does not exist on disk, so symlink resolution fails
	// and the hint falls back to the stable spelling -- the pre-fix
	// behavior, pinned as the fallback contract.
	require.Contains(t, buf.String(),
		"watch: enable full-filesystem watching by running 'competent-search-thing setup-watch' (or manually: sudo setcap cap_sys_admin,cap_dac_read_search+ep /test/bin/competent-search-thing)",
		"an unresolvable executable path falls back to the stable spelling")
	require.Contains(t, buf.String(),
		"watch: file capabilities stick to that exact file -- re-run the setcap command after any upgrade that replaces the binary (e.g. brew upgrade)",
		"the grant hint carries the persistence caveat")
	require.Contains(t, buf.String(),
		"watch: note: file capabilities force secure-exec (GOTRACEBACK=none, non-dumpable) -- crashes report as one line; ambient caps keep full crash reports (see README / issue #58)",
		"the grant hint carries the crash-visibility tradeoff (issue #58)")
	a.Shutdown(context.Background())
}

func TestLogFanotifyGrantResolvesSymlinkedExecutable(t *testing.T) {
	// The field failure this pins: the hint printed the Homebrew bin/
	// SYMLINK and setcap refused it ("not a regular (non-symlink)
	// file"). The printed path must be the resolved real file.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar", "competent-search-thing", "0.1.0", "bin", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(real), 0o755))
	require.NoError(t, os.WriteFile(real, []byte("#!"), 0o755))
	link := filepath.Join(dir, "bin", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(real, link))
	// t.TempDir may itself sit behind a symlink (darwin /var ->
	// /private/var), so canonicalize the expectation the same way.
	wantReal, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "linux"
	a.plat.executable = func() (string, error) { return link, nil }
	a.logFanotifyGrant()

	out := buf.String()
	require.Contains(t, out,
		"watch: enable full-filesystem watching by running 'competent-search-thing setup-watch' (or manually: sudo setcap cap_sys_admin,cap_dac_read_search+ep "+wantReal+")",
		"the grant command names the resolved real file, not the symlink")
	require.NotContains(t, out, "+ep "+link+"\n",
		"the symlink spelling setcap refuses must not be printed")
	require.Contains(t, out, "re-run the setcap command after any upgrade",
		"the persistence caveat is logged with the command")
	require.Contains(t, out, "ambient caps keep full crash reports",
		"the secure-exec tradeoff note is logged with the command")
}

func TestStartWatchStrictFanotifyNeverFallsBackToInotify(t *testing.T) {
	// watcher.backend="fanotify" is fanotify or NOTHING: on an
	// unprivileged run (CI) the backend resolves to "none" with the
	// watching-off hint; on a privileged run it is real fanotify with
	// full coverage. It must never report the inotify fallback.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{WatchBackend: "fanotify"})
	a.plat.goos = "linux"
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return len(r.emitted(eventWatchBackend)) == 1 },
		20*time.Second, 10*time.Millisecond, "the backend notice is emitted")
	wb, ok := r.emitted(eventWatchBackend)[0].payload[0].(watchBackend)
	require.True(t, ok)
	require.NotEqual(t, "inotify", wb.Backend, "strict mode must never fall back to inotify")
	switch wb.Backend {
	case "none":
		require.Equal(t, watchBackend{Backend: "none", Full: false, Hint: hintWatchOff}, wb)
	case "fanotify":
		require.Equal(t, watchBackend{Backend: "fanotify", Full: true, Hint: ""}, wb)
	default:
		t.Fatalf("unexpected backend %q", wb.Backend)
	}
	a.Shutdown(context.Background())
}

func TestLogFanotifyGrantSkippedOffLinuxAndOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Off linux there is no fanotify, so no setcap hint -- ever.
	a, _ := newTestApp(t, nil, Options{})
	a.plat.goos = "darwin"
	a.logFanotifyGrant()
	require.NotContains(t, buf.String(), "sudo setcap")

	// On linux the hint logs exactly once per app.
	b, _ := newTestApp(t, nil, Options{})
	b.plat.goos = "linux"
	b.logFanotifyGrant()
	b.logFanotifyGrant()
	require.Equal(t, 1, strings.Count(buf.String(), "sudo setcap cap_sys_admin,cap_dac_read_search+ep"),
		"the grant hint is logged once")
}

func TestEmitEventBeforeStartupIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.emitDegraded(watch.Stats{})
	require.Empty(t, r.emits, "no context yet: nothing emitted, nothing crashed")
}

// The pre-build (deferred) watch registration tests --
// startEarlyWatch / adoption / mid-build teardown -- live in
// watch_early_test.go (the hotset.go file-length-cap precedent).
