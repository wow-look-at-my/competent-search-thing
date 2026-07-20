package watch

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

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

	// A mount that appears but cannot be marked is logged and still
	// force-reconciled: coverage holds through sweeps, latency only.
	badsub := filepath.Join(root, "badmount")
	require.NoError(t, os.Mkdir(badsub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(badsub, "still-indexed.txt"), nil, 0o644))
	h.setFsid(badsub, fanoFsid{6, 6})
	h.setMarkErr(badsub, unix.ENODEV)
	mu.Lock()
	table = []string{sub, badsub}
	mu.Unlock()
	sweepOnce(t, s)
	require.Contains(t, h.markedPaths(), badsub, "the mark was attempted")
	_, ok = h.n.mountFor(fanoFsid{6, 6})
	require.False(t, ok, "the unmarkable filesystem stays unrouted")
	waitFor(t, func() bool { return hasPath(m, filepath.Join(badsub, "still-indexed.txt")) },
		"the reconcile still indexed the unmarkable mount's content")
}

func TestNewAutoNotifierAlwaysYieldsANotifier(t *testing.T) {
	// Backend auto-detection never fails overall: the real fanotify
	// backend when the environment allows it, else the per-directory
	// fsnotify fallback (the common unprivileged-CI outcome). Either
	// way the caller gets a working notifier.
	n, err := newAutoNotifier([]string{t.TempDir()})()
	require.NoError(t, err)
	require.NotNil(t, n)
	require.NoError(t, n.Close())
}

func TestFanotifyReadErrorSignalsOverflow(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	h.n.readFn = func(int, []byte) (int, error) { return 0, errors.New("boom") }
	require.NoError(t, h.n.start([]string{root}))
	t.Cleanup(func() { _ = h.n.Close() })
	require.ErrorIs(t, <-h.n.Errors(), fsnotify.ErrEventOverflow,
		"a dead event source degrades exactly like an overflow: sweeps take over")
}

func TestFanotifyDeliverGuardsCraftedNames(t *testing.T) {
	root := t.TempDir()
	h := newFanoHarness(t)
	require.NoError(t, h.n.start([]string{root}))
	t.Cleanup(func() { _ = h.n.Close() })
	h.bufs <- concat(
		fanoRec(unix.FAN_CREATE, fsidA, []byte(root), ".."),
		fanoRec(unix.FAN_CREATE, fsidA, []byte(root), "ok.txt"),
	)
	ev := <-h.n.Events()
	require.Equal(t, filepath.Join(root, "ok.txt"), ev.Name,
		"a crafted .. entry name is dropped; the sane event still flows")
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

func TestBackendSelectionScriptedConstructors(t *testing.T) {
	// Drives newBackendNotifier through a scripted fanotify
	// constructor, so all three watcher.backend selections are pinned
	// without CAP_SYS_ADMIN: strict mode never falls back to inotify
	// (fanotify or the "none" notifier, loudly), auto falls back
	// cleanly, and the pinned inotify mode never even probes fanotify.
	orig := newFanotifyFn
	defer func() { newFanotifyFn = orig }()
	roots := []string{t.TempDir()}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Constructor failure (the unprivileged reality).
	newFanotifyFn = func([]string) (notifier, error) { return nil, errors.New("EPERM: no CAP_SYS_ADMIN") }

	n, err := newBackendNotifier("fanotify", roots)()
	require.NoError(t, err, "strict mode degrades to the none notifier, never an error")
	bi, ok := n.(backendInfo)
	require.True(t, ok)
	name, wide := bi.kind()
	require.Equal(t, "none", name, "strict mode must NOT fall back to inotify")
	require.True(t, wide)
	require.NoError(t, n.Close())
	require.Contains(t, buf.String(),
		`watch: backend "fanotify" required by config but unavailable (EPERM: no CAP_SYS_ADMIN); live watching DISABLED, sweeps keep the index converging`,
		"the strict refusal is announced loudly")

	buf.Reset()
	n, err = newBackendNotifier("auto", roots)()
	require.NoError(t, err)
	name, wide = n.(backendInfo).kind()
	require.Equal(t, PerDirBackendName(), name, "auto falls back to per-directory fsnotify, labeled honestly")
	require.False(t, wide)
	require.NoError(t, n.Close())
	require.Contains(t, buf.String(), "watch: fanotify unavailable (EPERM: no CAP_SYS_ADMIN); falling back to per-directory inotify watches")

	// Constructor success: both fanotify-capable selections take it.
	fake := newFakeNotifier()
	calls := 0
	newFanotifyFn = func([]string) (notifier, error) { calls++; return fake, nil }

	n, err = newBackendNotifier("fanotify", roots)()
	require.NoError(t, err)
	require.Same(t, fake, n, "strict mode uses the fanotify notifier when it starts")
	n, err = newBackendNotifier("auto", roots)()
	require.NoError(t, err)
	require.Same(t, fake, n, "auto prefers fanotify when it starts")
	require.Equal(t, 2, calls)

	n, err = newBackendNotifier("inotify", roots)()
	require.NoError(t, err)
	name, wide = n.(backendInfo).kind()
	require.Equal(t, PerDirBackendName(), name, "the pinned inotify mode yields per-directory fsnotify, labeled honestly")
	require.False(t, wide)
	require.NoError(t, n.Close())
	require.Equal(t, 2, calls, "the pinned inotify mode never probes fanotify")
	require.NoError(t, fake.Close())
}
