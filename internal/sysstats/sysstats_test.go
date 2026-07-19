package sysstats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// logRecorder captures Logf lines for assertions.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logRecorder) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// fakeClock is the injectable now seam for the direct-sample tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fixture is a fake /proc + /sys tree with plausible default contents.
type fixture struct {
	proc string
	sys  string
}

const (
	// stat v1: total 1000, busy 200.
	statV1 = "cpu  100 0 100 700 100 0 0 0 0 0"
	// stat v2: total 2600, busy 600 -> delta 1600/400 = 25% busy.
	statV2 = "cpu  300 0 300 1900 100 0 0 0 0 0"
)

func netDev(rx, tx uint64) string {
	return netDevHeader +
		"    lo: 9999 9 0 0 0 0 0 0 9999 9 0 0 0 0 0 0\n" +
		fmt.Sprintf("  eth0: %d 10 0 0 0 0 0 0 %d 20 0 0 0 0 0 0\n", rx, tx) +
		"veth42: 5555 5 0 0 0 0 0 0 5555 5 0 0 0 0 0 0\n"
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{proc: filepath.Join(root, "proc"), sys: filepath.Join(root, "sys")}
	require.NoError(t, os.MkdirAll(filepath.Join(f.proc, "net"), 0o755))
	f.writeStat(t, statV1)
	f.writeMemInfo(t, memInfoFull)
	f.writeNetDev(t, netDev(1000, 2000))
	return f
}

func (f *fixture) writeStat(t *testing.T, aggregate string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.proc, "stat"), procStat(aggregate), 0o644))
}

func (f *fixture) writeMemInfo(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.proc, "meminfo"), []byte(content), 0o644))
}

func (f *fixture) writeNetDev(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.proc, "net", "dev"), []byte(content), 0o644))
}

// addAMDCard creates sys/class/drm/<card>/device/gpu_busy_percent.
func (f *fixture) addAMDCard(t *testing.T, card, busy string) string {
	t.Helper()
	dir := filepath.Join(f.sys, "class", "drm", card, "device")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, "gpu_busy_percent")
	require.NoError(t, os.WriteFile(p, []byte(busy), 0o644))
	return p
}

func (f *fixture) options(lr *logRecorder) Options {
	return Options{ProcRoot: f.proc, SysRoot: f.sys, GOOS: "linux", Logf: lr.logf}
}

// TestNewNoSourcePlatforms covers the platforms with zero sources
// (darwin has real sources now -- its cases live in darwin_test.go).
func TestNewNoSourcePlatforms(t *testing.T) {
	for _, goos := range []string{"windows", "plan9"} {
		t.Run(goos, func(t *testing.T) {
			lr := &logRecorder{}
			var calls atomic.Int64
			s := New(Options{GOOS: goos, Logf: lr.logf, OnUpdate: func(Snapshot) { calls.Add(1) }})
			require.Equal(t, 1, lr.count("no sources on this platform"))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.Start(ctx)
			require.Equal(t, 1, lr.count("nothing to sample"))
			s.SetVisible(true) // must not block or spawn anything
			time.Sleep(50 * time.Millisecond)
			require.Zero(t, calls.Load(), "no goroutine may be sampling")
			require.Equal(t, Snapshot{}, s.Snapshot())
			s.SetVisible(false)
		})
	}
}

func TestNewProbesAMDGPU(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	// card0 exists but has no busy file; card1 has one.
	require.NoError(t, os.MkdirAll(filepath.Join(f.sys, "class", "drm", "card0", "device"), 0o755))
	want := f.addAMDCard(t, "card1", "42\n")
	s := New(f.options(lr))
	require.Equal(t, want, s.amdPath)
	require.Empty(t, s.nvidiaPath)
	require.Contains(t, lr.all(), "gpu=amdgpu(card1)")
	require.Contains(t, lr.all(), "cpu="+filepath.Join(f.proc, "stat"))
}

func TestNewProbesAMDGPUSkipsUnreadable(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	// card0's busy path is a directory: ReadFile fails, card1 wins.
	require.NoError(t, os.MkdirAll(filepath.Join(f.sys, "class", "drm", "card0", "device", "gpu_busy_percent"), 0o755))
	want := f.addAMDCard(t, "card1", "7\n")
	s := New(f.options(lr))
	require.Equal(t, want, s.amdPath)
}

func TestNewProbesNvidia(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	opt := f.options(lr)
	opt.LookPath = func(file string) (string, error) {
		require.Equal(t, "nvidia-smi", file)
		return "/opt/bin/nvidia-smi", nil
	}
	s := New(opt)
	require.Equal(t, "/opt/bin/nvidia-smi", s.nvidiaPath)
	require.Empty(t, s.amdPath)
	require.Contains(t, lr.all(), "gpu=nvidia-smi")
}

func TestNewProbesNoGPU(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	opt := f.options(lr)
	opt.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	s := New(opt)
	require.Empty(t, s.amdPath)
	require.Empty(t, s.nvidiaPath)
	require.Contains(t, lr.all(), "gpu=none")
}

// newDirectSampler builds a linux sampler over the fixture with a fake
// clock, for driving sample() directly (no goroutines).
func newDirectSampler(t *testing.T, f *fixture, lr *logRecorder, clk *fakeClock) *Sampler {
	t.Helper()
	opt := f.options(lr)
	opt.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	opt.now = clk.Now
	return New(opt)
}

func TestSampleRatesAndStaleness(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	f.addAMDCard(t, "card0", "42\n")
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDirectSampler(t, f, lr, clk)

	// Baseline: point-in-time metrics live, rates not yet.
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK, "no rate before a second read")
	require.False(t, snap.NetOK)
	require.True(t, snap.MemOK)
	require.Equal(t, uint64(16000000)*1024, snap.MemTotal)
	require.Equal(t, uint64(6000000)*1024, snap.MemUsed)
	require.True(t, snap.SwapOK)
	require.Equal(t, uint64(4000000)*1024, snap.SwapTotal)
	require.Equal(t, uint64(1000000)*1024, snap.SwapUsed)
	require.True(t, snap.GPUOK)
	require.Equal(t, 42.0, snap.GPUPct)

	// Second sample 1s later (inside the 4.5s window): exact rates.
	f.writeStat(t, statV2)
	f.writeNetDev(t, netDev(2000, 4000))
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK)
	require.InDelta(t, 25.0, snap.CPUPct, 0.001)
	require.True(t, snap.NetOK)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)
	require.InDelta(t, 2000.0, snap.NetTxBps, 0.001)

	// Stale counters (a long hidden stretch): values kept, no rate
	// computed over the gap, counters re-stored.
	f.writeStat(t, "cpu  1000 0 1000 5000 100 0 0 0 0 0") // total 7100 busy 2100
	f.writeNetDev(t, netDev(100000, 200000))
	clk.Advance(time.Hour)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK, "previous value survives a stale baseline")
	require.InDelta(t, 25.0, snap.CPUPct, 0.001)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)

	// The follow-up after the stale baseline computes fresh rates:
	// +2s, busy delta 500 of total delta 1000 = 50%; net +1000/+3000.
	f.writeStat(t, "cpu  1400 0 1100 5500 100 0 0 0 0 0") // total 8100 busy 2600
	f.writeNetDev(t, netDev(101000, 203000))
	clk.Advance(2 * time.Second)
	s.sample()
	snap = s.Snapshot()
	require.InDelta(t, 50.0, snap.CPUPct, 0.001)
	require.InDelta(t, 500.0, snap.NetRxBps, 0.001)
	require.InDelta(t, 1500.0, snap.NetTxBps, 0.001)
}

func TestSampleCounterWrapKeepsPreviousValue(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDirectSampler(t, f, lr, clk)

	s.sample()
	f.writeStat(t, statV2)
	f.writeNetDev(t, netDev(2000, 4000))
	clk.Advance(time.Second)
	s.sample()
	require.InDelta(t, 25.0, s.Snapshot().CPUPct, 0.001)

	// Counters running backwards (wrap): the update is skipped, the
	// previous value and OK flag stand.
	f.writeStat(t, statV1)
	f.writeNetDev(t, netDev(10, 20))
	clk.Advance(time.Second)
	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.CPUOK)
	require.InDelta(t, 25.0, snap.CPUPct, 0.001)
	require.True(t, snap.NetOK)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)
}

func TestSampleReadFailuresDegradeAndLogOnce(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	f.addAMDCard(t, "card0", "42\n")
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDirectSampler(t, f, lr, clk)
	s.sample()

	// Break every source: each metric degrades alone, one log line
	// per distinct message even across repeated samples.
	require.NoError(t, os.Remove(filepath.Join(f.proc, "stat")))
	require.NoError(t, os.Remove(filepath.Join(f.proc, "meminfo")))
	require.NoError(t, os.Remove(filepath.Join(f.proc, "net", "dev")))
	require.NoError(t, os.WriteFile(s.amdPath, []byte("garbage"), 0o644))
	clk.Advance(time.Second)
	s.sample()
	clk.Advance(time.Second)
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK)
	require.False(t, snap.MemOK)
	require.False(t, snap.SwapOK)
	require.False(t, snap.NetOK)
	require.False(t, snap.GPUOK)
	require.Equal(t, 1, lr.count("stats: cpu:"), "cpu failure logged once")
	require.Equal(t, 1, lr.count("stats: mem:"))
	require.Equal(t, 1, lr.count("stats: net:"))
	require.Equal(t, 1, lr.count("stats: gpu:"))

	// Heal the sources: everything comes back, rates included (the
	// stored counters survived the outage and are still fresh).
	f.writeStat(t, statV2)
	f.writeMemInfo(t, memInfoFull)
	f.writeNetDev(t, netDev(2000, 4000))
	require.NoError(t, os.WriteFile(s.amdPath, []byte("17\n"), 0o644))
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK)
	require.True(t, snap.MemOK)
	require.True(t, snap.SwapOK)
	require.True(t, snap.NetOK)
	require.True(t, snap.GPUOK)
	require.Equal(t, 17.0, snap.GPUPct)
}

func TestSampleParseFailuresDegrade(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDirectSampler(t, f, lr, clk)

	f.writeStat(t, "cpu  bad fields here 1 2")
	f.writeNetDev(t, "no interfaces at all\n")
	f.writeMemInfo(t, "MemTotal: 100 kB\nSwapTotal: 10 kB\nSwapFree: 4 kB\n") // no MemAvailable
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK)
	require.False(t, snap.NetOK)
	require.False(t, snap.MemOK, "MemAvailable missing means no used figure")
	require.True(t, snap.SwapOK, "swap lines are complete")
	require.Equal(t, uint64(6)*1024, snap.SwapUsed)
	require.Equal(t, 1, lr.count("missing MemTotal/MemAvailable"))

	// Swap-less meminfo: the swap side degrades alone.
	f.writeMemInfo(t, "MemTotal: 100 kB\nMemAvailable: 40 kB\n")
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.MemOK)
	require.Equal(t, uint64(60)*1024, snap.MemUsed)
	require.False(t, snap.SwapOK)
	require.Equal(t, 1, lr.count("missing SwapTotal/SwapFree"))
}

func TestSampleZeroSwapTotalIsValid(t *testing.T) {
	lr := &logRecorder{}
	f := newFixture(t)
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDirectSampler(t, f, lr, clk)
	f.writeMemInfo(t, "MemTotal: 100 kB\nMemAvailable: 40 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.SwapOK, "no swap configured is a valid answer")
	require.Zero(t, snap.SwapTotal)
	require.Zero(t, snap.SwapUsed)
}

func TestLogOnceIsBounded(t *testing.T) {
	lr := &logRecorder{}
	s := New(Options{GOOS: "windows", Logf: lr.logf})
	for i := 0; i < 2*maxLoggedMessages; i++ {
		s.logOnce(fmt.Sprintf("distinct message %d", i))
	}
	require.Equal(t, maxLoggedMessages, lr.count("distinct message"),
		"messages beyond the cap are dropped")
	require.Len(t, s.logged, maxLoggedMessages)
}

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
