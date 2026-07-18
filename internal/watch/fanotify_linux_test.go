package watch

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// fsidA is the harness default filesystem id.
var fsidA = fanoFsid{7, 7}

// fanoHarness bundles a seam-scripted fanotifyNotifier: readFn is fed
// whole batches through bufs (io.EOF once closed; the notifier's stop
// channel always unblocks it, so Close never hangs on a quiet script),
// resolveFn decodes the handle bytes as the parent directory path and
// mimics the real resolver's ESTALE for vanished parents, fsidFn
// answers fsidA unless scripted otherwise, and markFn records calls
// with optional scripted failures.
type fanoHarness struct {
	n    *fanotifyNotifier
	bufs chan []byte

	mu      sync.Mutex
	marked  []string
	markErr map[string]error
	fsids   map[string]fanoFsid
}

func newFanoHarness(t *testing.T) *fanoHarness {
	t.Helper()
	h := &fanoHarness{
		bufs:    make(chan []byte, 64),
		markErr: make(map[string]error),
		fsids:   make(map[string]fanoFsid),
	}
	h.n = &fanotifyNotifier{
		initFn: func() (int, error) {
			// A real fd the notifier owns and may close (raw open: an
			// os.File finalizer would double-close a reused fd).
			return unix.Open(os.DevNull, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		},
		markFn: func(_ int, path string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.marked = append(h.marked, path)
			return h.markErr[path]
		},
		readFn: func(_ int, b []byte) (int, error) {
			select {
			case <-h.n.stop:
				return 0, errFanoClosed
			case buf, ok := <-h.bufs:
				if !ok {
					return 0, io.EOF
				}
				return copy(b, buf), nil
			}
		},
		resolveFn: func(_ int, _ int32, handle []byte) (string, error) {
			p := string(handle)
			if _, err := os.Lstat(p); err != nil {
				return "", err // the real resolver's ESTALE shape
			}
			return p, nil
		},
		fsidFn: func(path string) (fanoFsid, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if id, ok := h.fsids[path]; ok {
				return id, nil
			}
			return fsidA, nil
		},
		mountsFn: func([]string) []string { return nil },
	}
	return h
}

func (h *fanoHarness) setFsid(path string, id fanoFsid) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fsids[path] = id
}

func (h *fanoHarness) setMarkErr(path string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.markErr[path] = err
}

func (h *fanoHarness) markedPaths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.marked...)
}

// feed pushes one batch of synthetic DFID_NAME events, one per dirty
// path: parent-directory handle = the path's directory (as bytes),
// entry name = the base name -- exactly the (dir, name) shape the
// kernel reports.
func (h *fanoHarness) feed(mask uint64, paths ...string) {
	var payload []byte
	for _, p := range paths {
		payload = append(payload, fanoRec(mask, fsidA, []byte(filepath.Dir(p)), filepath.Base(p))...)
	}
	h.bufs <- payload
}

// startFanoWatcher wires a Watcher to the harness notifier through the
// production seam and starts it registered.
func startFanoWatcher(t *testing.T, m *index.Manager, h *fanoHarness) *Watcher {
	t.Helper()
	w := newTestWatcher(t, m, nil)
	w.newNotifier = func() (notifier, error) {
		if err := h.n.start(w.rootList); err != nil {
			return nil, err
		}
		return h.n, nil
	}
	startWatcherRegistered(t, w)
	return w
}

// fanoSettle is settle for the fanotify harness: a unique marker on
// disk, then its synthetic event; once the marker is indexed, every
// earlier event has been applied too.
func fanoSettle(t *testing.T, m *index.Manager, h *fanoHarness, dir string) {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprintf("marker-%d.settle", time.Now().UnixNano()))
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	h.feed(unix.FAN_CREATE, p)
	waitFor(t, func() bool { return hasPath(m, p) }, "settle marker must reach the index")
}

// runTierFanotify drives the fanotify notifier end-to-end through
// scripted seams -- hand-built wire buffers whose parent handles
// encode the on-disk parent paths -- into a real Watcher, real index,
// and real temp tree. Privilege-free via the seams; everything from
// the byte parsing to reconcile application is the production path.
func runTierFanotify(t *testing.T) map[string]bool {
	root := t.TempDir()
	equivBase(t, root)
	m := buildManager(t, root, nil)
	h := newFanoHarness(t)
	w := startFanoWatcher(t, m, h)
	require.Equal(t, "fanotify", w.Stats().Backend)

	h.feed(unix.FAN_CREATE, applyMutationScript(t, root)...)
	fanoSettle(t, m, h, root)
	return requireConverged(t, m, root)
}

// TestTierEquivalenceFanotify extends the tier-equivalence invariant
// to the fanotify backend: the same mutation script converges to the
// IDENTICAL final index state the fsnotify-driven full-watch tier
// reaches. (The shared TestTierEquivalence lives in a portable file
// and cannot reference the linux-only harness, hence the separate
// pairing here.)
func TestTierEquivalenceFanotify(t *testing.T) {
	var full, fano map[string]bool
	t.Run("FullWatch", func(t *testing.T) { full = runTierFullWatch(t) })
	t.Run("Fanotify", func(t *testing.T) { fano = runTierFanotify(t) })
	require.NotEmpty(t, full)
	require.Equal(t, full, fano, "the fanotify tier must converge to the full-watch state")
}

func TestFanotifyRootMarkFailureFailsConstruction(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	h.setMarkErr(root, unix.EPERM)
	err := h.n.start([]string{root})
	require.Error(t, err)
	require.ErrorIs(t, err, unix.EPERM)
	require.Contains(t, err.Error(), "fanotify mark", "the error names the failing stage for the fallback log line")
}

func TestFanotifyInitFailureFailsConstruction(t *testing.T) {
	h := newFanoHarness(t)
	h.n.initFn = func() (int, error) { return -1, unix.ENOSYS }
	err := h.n.start([]string{t.TempDir()})
	require.ErrorIs(t, err, unix.ENOSYS)
	require.Empty(t, h.markedPaths(), "no marks are attempted without a group")
}

func TestFanotifyPerMountFailureIsSkipped(t *testing.T) {
	root := t.TempDir()
	tree := mkTree(t, root, "bad/", "good/")
	badMount, goodMount := tree["bad/"], tree["good/"]
	h := newFanoHarness(t)
	h.setFsid(badMount, fanoFsid{2, 2})
	h.setFsid(goodMount, fanoFsid{3, 3})
	h.setMarkErr(badMount, unix.ENODEV)
	h.n.mountsFn = func([]string) []string { return []string{badMount, goodMount} }

	require.NoError(t, h.n.start([]string{root}), "a per-mount failure never fails the backend")
	t.Cleanup(func() { _ = h.n.Close() })

	require.Equal(t, []string{root, badMount, goodMount}, h.markedPaths(),
		"roots first, then every extra mountpoint attempted")
	_, ok := h.n.mountFor(fanoFsid{2, 2})
	require.False(t, ok, "the unmarkable filesystem stays unrouted (sweeps cover it)")
	_, ok = h.n.mountFor(fanoFsid{3, 3})
	require.True(t, ok, "the markable mount is routed")
	_, ok = h.n.mountFor(fsidA)
	require.True(t, ok, "the root filesystem is routed")
}

func TestFanotifySharedFsidCoveredOnce(t *testing.T) {
	// Two roots on one filesystem cost one mark: the second cover is
	// an fsid-table no-op.
	r1, r2 := t.TempDir(), t.TempDir()
	h := newFanoHarness(t)
	require.NoError(t, h.n.start([]string{r1, r2}))
	t.Cleanup(func() { _ = h.n.Close() })
	require.Equal(t, []string{r1}, h.markedPaths(), "the shared superblock is marked exactly once")
}

func TestFanotifyWideCoverageSkipsHotSet(t *testing.T) {
	root := t.TempDir()
	mkTree(t, root, "d1/", "d2/", "d1/f.txt")
	m := buildManager(t, root, nil)
	h := newFanoHarness(t)
	w := startFanoWatcher(t, m, h)

	st := w.Stats()
	require.Equal(t, "fanotify", st.Backend)
	require.True(t, w.isWide())
	require.Zero(t, st.WatchedDirs, "no per-directory watches under whole-filesystem marks")
	require.Zero(t, st.IndexedDirs, "no desired-set enumeration ran")

	// A dirty directory reconciles -- refreshWatch and scanNewDir's
	// addWatch no-op -- without creating bookkeeping.
	nd := filepath.Join(root, "d3")
	require.NoError(t, os.Mkdir(nd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nd, "inner.txt"), nil, 0o644))
	h.feed(unix.FAN_CREATE|unix.FAN_ONDIR, nd)
	waitFor(t, func() bool { return hasPath(m, filepath.Join(nd, "inner.txt")) }, "new dir scanned into the index")
	require.Zero(t, w.watchedCount(), "reconcile added no watches")

	// syncWatches (the rescan resync path) stays a no-op.
	w.syncWatches(context.Background())
	require.Zero(t, w.watchedCount())
	require.Zero(t, w.Stats().IndexedDirs)
}

func TestFanotifyEventsOutsideRootsAreDropped(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	m := buildManager(t, root, nil)
	h := newFanoHarness(t)
	startFanoWatcher(t, m, h)

	stray := filepath.Join(outside, "stray.txt")
	require.NoError(t, os.WriteFile(stray, nil, 0o644))
	h.feed(unix.FAN_CREATE, stray)
	fanoSettle(t, m, h, root) // ordered after the stray event
	require.False(t, hasPath(m, stray),
		"whole-filesystem marks see everything; only the roots may reach the index")
}

func TestFanotifyOverflowRecordRequestsSweep(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	h := newFanoHarness(t)
	w := newTestWatcher(t, m, nil)
	w.newNotifier = func() (notifier, error) {
		if err := h.n.start(w.rootList); err != nil {
			return nil, err
		}
		return h.n, nil
	}
	requests := make(chan struct{}, 4)
	w.setSweepRequester(func() { requests <- struct{}{} })
	startWatcherRegistered(t, w)

	h.bufs <- fanoMeta(unix.FAN_Q_OVERFLOW, 3, nil)
	waitFor(t, func() bool { return w.Stats().Overflows == 1 }, "the kernel overflow surfaces in stats")
	waitFor(t, func() bool { return len(requests) > 0 }, "an overflow requests a reconcile sweep")
	require.True(t, w.Degraded(), "lost events degrade the watcher")
}

func TestFanotifyEventsChannelFullSynthesizesOverflow(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	require.NoError(t, h.n.start([]string{root}))
	t.Cleanup(func() { _ = h.n.Close() })

	// No consumer drains the events channel: overfill it in one batch.
	var payload []byte
	for i := 0; i < fanoEventBuf+50; i++ {
		payload = append(payload, fanoRec(unix.FAN_CREATE, fsidA, []byte(root), fmt.Sprintf("f%04d", i))...)
	}
	h.bufs <- payload

	waitFor(t, func() bool { return len(h.n.errs) > 0 }, "the full channel synthesizes an overflow")
	require.ErrorIs(t, <-h.n.errs, fsnotify.ErrEventOverflow)
	waitFor(t, func() bool { return len(h.n.events) == fanoEventBuf },
		"the channel holds a full buffer; the excess was dropped")
	require.Empty(t, h.n.errs, "exactly one synthesized overflow per batch")
}

func TestFanotifySweepMarksNewMounts(t *testing.T) {
	root := t.TempDir()
	m := buildManager(t, root, nil)
	h := newFanoHarness(t)
	w := startFanoWatcher(t, m, h)

	sub := filepath.Join(root, "newmount")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "on-mount.txt"), nil, 0o644))
	h.setFsid(sub, fanoFsid{5, 5})

	var mu sync.Mutex
	var table []string
	s := newTestSweeper(t, m, w, SweepOptions{mounts: func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), table...)
	}})
	startSweeper(t, s)

	sweepOnce(t, s)
	require.NotContains(t, h.markedPaths(), sub, "an unchanged mount table marks nothing")

	mu.Lock()
	table = []string{sub}
	mu.Unlock()
	sweepOnce(t, s)
	require.Contains(t, h.markedPaths(), sub, "an appearing mountpoint is marked before its reconcile")
	_, ok := h.n.mountFor(fanoFsid{5, 5})
	require.True(t, ok, "the new filesystem is routed")
	waitFor(t, func() bool { return hasPath(m, filepath.Join(sub, "on-mount.txt")) },
		"the forced reconcile indexed the mount's content")

	marks := len(h.markedPaths())
	sweepOnce(t, s)
	require.Len(t, h.markedPaths(), marks, "a stable mount table re-marks nothing")
}

func TestFanotifyMarkMountAfterCloseErrors(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	require.NoError(t, h.n.start([]string{root}))
	require.NoError(t, h.n.Close())
	require.ErrorIs(t, h.n.MarkMount(root), errFanoClosed)
}

func TestFanotifyCloseIdempotent(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	require.NoError(t, h.n.start([]string{root}))
	require.NoError(t, h.n.Close())
	require.NoError(t, h.n.Close())
	_, ok := <-h.n.Events()
	require.False(t, ok, "events channel closed")
	_, ok = <-h.n.Errors()
	require.False(t, ok, "errors channel closed")
	require.Nil(t, h.n.Add("x"), "Add stays a nil no-op")
	require.Nil(t, h.n.Remove("x"), "Remove stays a nil no-op")
}

func TestFanotifyProductionSeamErrorPaths(t *testing.T) {
	// fanoInit is allowed unprivileged since 5.13 in fid mode;
	// tolerate both outcomes and close the group on success.
	if fd, err := fanoInit(); err == nil {
		require.NoError(t, unix.Close(fd))
	}
	// fanoMark on a bad fd exercises the wrapper without ever
	// touching a real mark.
	require.Error(t, fanoMark(-1, t.TempDir()))
	// statfsFsid on a live path and a missing one.
	_, err := statfsFsid(t.TempDir())
	require.NoError(t, err)
	_, err = statfsFsid(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	// fanoResolve with an all-zero FILEID_INO32_GEN handle errors on
	// every privilege level (EPERM unprivileged, ESTALE otherwise:
	// inode 0 never exists) -- never a bogus path.
	dfd, err := unix.Open(t.TempDir(), unix.O_PATH|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(dfd)
	_, err = fanoResolve(dfd, 1, make([]byte, 8))
	require.Error(t, err)
}

func TestFanotifyProductionReadFn(t *testing.T) {
	// The poll+read production readFn against a plain pipe: data
	// flows, and closing the stop pipe wakes a blocked wait with the
	// shutdown sentinel.
	stopR, stopW, err := os.Pipe()
	require.NoError(t, err)
	defer stopR.Close()
	read := fanoReadFn(stopR)

	var p [2]int
	require.NoError(t, unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC))
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	_, err = unix.Write(p[1], []byte("abc"))
	require.NoError(t, err)
	buf := make([]byte, 16)
	n, err := read(p[0], buf)
	require.NoError(t, err)
	require.Equal(t, "abc", string(buf[:n]))

	done := make(chan error, 1)
	go func() {
		_, rerr := read(p[0], buf)
		done <- rerr
	}()
	time.Sleep(20 * time.Millisecond) // let the goroutine reach the poll (best-effort)
	require.NoError(t, stopW.Close())
	require.ErrorIs(t, <-done, errFanoClosed)
}
