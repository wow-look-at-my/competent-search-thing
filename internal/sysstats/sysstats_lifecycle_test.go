package sysstats

// Sampler lifecycle and slow-loop tests over the real goroutines --
// the OnUpdate recorder, visibility gating, and the nvidia-smi slow
// path -- split from sysstats_test.go (which keeps the fixture,
// probe, and direct-sample tests).

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// snapRecorder collects OnUpdate callbacks.
type snapRecorder struct {
	mu    sync.Mutex
	snaps []Snapshot
}

func (r *snapRecorder) record(s Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, s)
}

func (r *snapRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snaps)
}

func TestSamplerLifecycle(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	f.addAMDCard(t, "card0", "42\n")
	rec := &snapRecorder{}
	opt := f.options(lr)
	opt.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	opt.Interval = 25 * time.Millisecond
	opt.OnUpdate = rec.record
	s := New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Hidden: nothing samples -- prove it by breaking every source;
	// any read would log. The app starts hidden, so this is the
	// steady state until the first summon.
	statPath := filepath.Join(f.proc, "stat")
	statData, err := os.ReadFile(statPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(statPath))
	time.Sleep(120 * time.Millisecond)
	require.Zero(t, rec.count(), "hidden iterations must not sample")
	require.Zero(t, lr.count("stats: cpu:"), "hidden iterations must not read")
	require.Equal(t, Snapshot{}, s.Snapshot())
	require.NoError(t, os.WriteFile(statPath, statData, 0o644))

	// Summon: the kick delivers a baseline sample immediately.
	s.SetVisible(true)
	require.Eventually(t, func() bool { return rec.count() >= 1 },
		2*time.Second, 5*time.Millisecond, "the kick samples without waiting for the ticker")
	snap := s.Snapshot()
	require.True(t, snap.MemOK)
	require.True(t, snap.GPUOK)
	require.Equal(t, 42.0, snap.GPUPct)

	// Advance the counters; the follow-up sample or a tick computes
	// rates soon after.
	f.writeStat(t, statV2)
	f.writeNetDev(t, netDev(2000, 4000))
	require.Eventually(t, func() bool {
		sn := s.Snapshot()
		return sn.CPUOK && sn.NetOK
	}, 2*time.Second, 5*time.Millisecond, "rates appear after the follow-up/tick")
	snap = s.Snapshot()
	require.GreaterOrEqual(t, snap.CPUPct, 0.0)
	require.LessOrEqual(t, snap.CPUPct, 100.0)

	// Hide: callbacks stop (at most one in-flight sample may still
	// land).
	s.SetVisible(false)
	time.Sleep(80 * time.Millisecond)
	stopped := rec.count()
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, stopped, rec.count(), "no callbacks while hidden")

	// Summon again: an immediate callback, not one ticker later.
	s.SetVisible(true)
	require.Eventually(t, func() bool { return rec.count() > stopped },
		2*time.Second, 5*time.Millisecond)

	// Concurrent Snapshot readers are safe (race detector food).
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.Snapshot()
			}
		}()
	}
	wg.Wait()

	// Cancel: the goroutines exit; callbacks stop for good.
	cancel()
	time.Sleep(60 * time.Millisecond)
	final := rec.count()
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, final, rec.count(), "cancelled sampler must not publish")
}

// gpuExecFake scripts the nvidia-smi seam: mode "ok" answers
// instantly, mode "hang" blocks until the per-run ctx dies (proving
// the timeout context is enforced).
type gpuExecFake struct {
	mu          sync.Mutex
	mode        string
	calls       int
	sawDeadline bool
}

func (g *gpuExecFake) run(ctx context.Context, path string) (string, error) {
	g.mu.Lock()
	g.calls++
	_, hasDeadline := ctx.Deadline()
	g.sawDeadline = g.sawDeadline || hasDeadline
	mode := g.mode
	g.mu.Unlock()
	if mode == "hang" {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "37\n", nil
}

func (g *gpuExecFake) setMode(m string) {
	g.mu.Lock()
	g.mode = m
	g.mu.Unlock()
}

func (g *gpuExecFake) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func TestNvidiaSlowPath(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	fake := &gpuExecFake{mode: "ok"}
	opt := f.options(lr)
	opt.LookPath = func(string) (string, error) { return "/fake/nvidia-smi", nil }
	opt.Interval = 20 * time.Millisecond
	opt.GPUInterval = 30 * time.Millisecond
	opt.GPUTimeout = 20 * time.Millisecond
	opt.gpuExec = fake.run
	s := New(opt)
	require.Equal(t, "/fake/nvidia-smi", s.nvidiaPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Hidden: the slow loop must not exec anything.
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, fake.callCount(), "hidden slow loop must not exec")

	// Visible: the value lands within a GPUInterval or two and the
	// fast loop folds it in.
	s.SetVisible(true)
	require.Eventually(t, func() bool {
		sn := s.Snapshot()
		return sn.GPUOK && sn.GPUPct == 37.0
	}, 2*time.Second, 5*time.Millisecond, "nvidia value reaches the snapshot")
	require.True(t, func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return fake.sawDeadline }(),
		"the exec context carries the GPUTimeout deadline")

	// A hanging nvidia-smi: the timeout kills each attempt, the last
	// value expires after 3*GPUInterval, GPU degrades to not-OK.
	fake.setMode("hang")
	require.Eventually(t, func() bool { return !s.Snapshot().GPUOK },
		2*time.Second, 5*time.Millisecond, "stale value expires")
	require.GreaterOrEqual(t, lr.count("stats: nvidia-smi:"), 1)

	// Hide again: exec calls stop.
	s.SetVisible(false)
	time.Sleep(80 * time.Millisecond)
	calls := fake.callCount()
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, calls, fake.callCount())
}

func TestRunNvidiaSMIReal(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	dir := t.TempDir()
	ok := filepath.Join(dir, "fake-nvidia-smi")
	require.NoError(t, os.WriteFile(ok, []byte("#!/bin/sh\necho 42\n"), 0o755))
	out, err := runNvidiaSMI(context.Background(), ok)
	require.NoError(t, err)
	v, err := parseLeadingInt(out)
	require.NoError(t, err)
	require.Equal(t, 42, v)

	// A hung child is killed at the context deadline instead of being
	// waited out (WaitDelay also force-closes the pipes).
	slow := filepath.Join(dir, "slow-nvidia-smi")
	require.NoError(t, os.WriteFile(slow, []byte("#!/bin/sh\nsleep 30\n"), 0o755))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = runNvidiaSMI(ctx, slow)
	require.Error(t, err)
	require.Less(t, time.Since(start), 10*time.Second, "the child was killed, not waited out")
}

// BenchmarkSample measures one full fast-path sample (cpu + mem + net
// reads, parses, and the snapshot publish) against the real /proc.
func BenchmarkSample(b *testing.B) {
	if _, err := os.ReadFile("/proc/stat"); err != nil {
		b.Skipf("no readable /proc/stat on this host: %v", err)
	}
	s := New(Options{GOOS: "linux", Logf: func(string, ...any) {}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.sample()
	}
}
