package sysstats

// Headless tests for the darwin stats sources: pure decoders and
// derivations over synthetic buffers, plus the darwin sample paths
// over scripted readers. Deliberately UNTAGGED -- these run on the
// linux CI job AND the mac runner, so every logical property is
// pinned platform-independently; only the thin production readers
// need the real-call tests in readers_darwin_test.go.

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

/* --- scripted readers ---------------------------------------------- */

// fakeDarwin scripts the darwinReaders seam with mutable readings,
// per-reader forced errors, and call counts (the zero-IO-while-hidden
// proof).
type fakeDarwin struct {
	mu       sync.Mutex
	ticks    cpuTicksDarwin
	memTotal uint64
	vm       vmStat64
	swap     []byte
	rib      []byte
	names    map[int]string
	gpu      []map[string]int64
	errs     map[string]error
	calls    map[string]int
}

func newFakeDarwin() *fakeDarwin {
	return &fakeDarwin{
		ticks:    cpuTicksDarwin{user: 100, system: 50, idle: 800, nice: 10},
		memTotal: 16 << 30,
		vm:       vmStat64{internalPages: 1000, purgeable: 100, wired: 200, compressor: 50, pageSize: 16384},
		swap:     xswUsage(4<<30, 3<<30, 1<<30),
		rib:      ribDump(ifInfo2Record(1, 999, 999), ifInfo2Record(4, 1000, 2000)),
		names:    map[int]string{1: "lo0", 4: "en0"},
		gpu:      []map[string]int64{{"Device Utilization %": 37}},
		errs:     map[string]error{},
		calls:    map[string]int{},
	}
}

func (f *fakeDarwin) called(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.errs[name]
}

func (f *fakeDarwin) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

func (f *fakeDarwin) setErr(name string, err error) {
	f.mu.Lock()
	f.errs[name] = err
	f.mu.Unlock()
}

func (f *fakeDarwin) readers() *darwinReaders {
	return &darwinReaders{
		cpuTicks: func() (cpuTicksDarwin, error) {
			if err := f.called("cpuTicks"); err != nil {
				return cpuTicksDarwin{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.ticks, nil
		},
		memTotal: func() (uint64, error) {
			if err := f.called("memTotal"); err != nil {
				return 0, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.memTotal, nil
		},
		vmStat: func() (vmStat64, error) {
			if err := f.called("vmStat"); err != nil {
				return vmStat64{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.vm, nil
		},
		swapRaw: func() ([]byte, error) {
			if err := f.called("swapRaw"); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.swap, nil
		},
		ifRIB: func() ([]byte, error) {
			if err := f.called("ifRIB"); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.rib, nil
		},
		ifNames: func() (map[int]string, error) {
			if err := f.called("ifNames"); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.names, nil
		},
		gpuStats: func() ([]map[string]int64, error) {
			if err := f.called("gpuStats"); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.gpu, nil
		},
	}
}

func (f *fakeDarwin) set(fn func(f *fakeDarwin)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// newDarwinSampler builds a GOOS "darwin" sampler over scripted
// readers and a fake clock, for driving sample() directly.
func newDarwinSampler(fake *fakeDarwin, lr *logRecorder, clk *fakeClock) *Sampler {
	opt := Options{GOOS: "darwin", Logf: lr.logf}
	opt.darwin = fake.readers()
	if clk != nil {
		opt.now = clk.Now
	}
	return New(opt)
}

/* --- New / probe behavior ------------------------------------------ */

func TestNewDarwinSources(t *testing.T) {
	lr := &logRecorder{}
	s := newDarwinSampler(newFakeDarwin(), lr, nil)
	require.True(t, s.hasSources())
	require.Equal(t, 1, lr.count("stats: sources: cpu=host_statistics mem=vm_statistics64+hw.memsize swap=vm.swapusage net=sysctl(iflist2) gpu=ioaccelerator"),
		"the darwin source line names every source incl. the IOAccelerator gpu read")
	require.Zero(t, lr.count("no sources on this platform"))
}

func TestNewDarwinSourcesNoGPUReader(t *testing.T) {
	// A readers value without the gpu member (partial seams, older
	// scripted fakes) is announced honestly as gpu=none and keeps the
	// dash.
	lr := &logRecorder{}
	fake := newFakeDarwin()
	rd := fake.readers()
	rd.gpuStats = nil
	opt := Options{GOOS: "darwin", Logf: lr.logf}
	opt.darwin = rd
	s := New(opt)
	require.True(t, s.hasSources())
	require.Equal(t, 1, lr.count("gpu=none"))
	s.sample()
	require.False(t, s.Snapshot().GPUOK, "nil gpu reader is the silent dash")
	require.Zero(t, lr.count("stats: gpu:"))
}

func TestNewDarwinZeroReaders(t *testing.T) {
	// A darwinReaders value with nothing bound (what the !darwin stub
	// yields) degrades to the placeholders row -- injected explicitly
	// so the behavior is pinned on every platform.
	lr := &logRecorder{}
	opt := Options{GOOS: "darwin", Logf: lr.logf}
	opt.darwin = &darwinReaders{}
	s := New(opt)
	require.False(t, s.hasSources())
	require.Equal(t, 1, lr.count("no sources on this platform"))
	s.Start(context.Background())
	require.Equal(t, 1, lr.count("nothing to sample"))
}

func TestNewDarwinStubOnNonDarwinHosts(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("on darwin the production readers bind; covered by readers_darwin_test.go")
	}
	// No injected seam: newDarwinReaders is the readers_other.go stub
	// here, so GOOS "darwin" behaves like an unknown platform.
	lr := &logRecorder{}
	s := New(Options{GOOS: "darwin", Logf: lr.logf})
	require.False(t, s.hasSources())
	require.Equal(t, 1, lr.count("no sources on this platform"))
}

/* --- sample paths --------------------------------------------------- */

func TestDarwinSampleRatesAndStaleness(t *testing.T) {
	lr := &logRecorder{}
	fake := newFakeDarwin()
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDarwinSampler(fake, lr, clk)

	// Baseline: point-in-time metrics live (the IOAccelerator gpu read
	// included), rates not yet.
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK, "no rate before a second read")
	require.False(t, snap.NetOK)
	require.True(t, snap.GPUOK, "the registry read is point-in-time: live on the first sample")
	require.Equal(t, 37.0, snap.GPUPct)
	require.True(t, snap.MemOK)
	require.Equal(t, uint64(16<<30), snap.MemTotal)
	require.Equal(t, uint64(1000-100+200+50)*16384, snap.MemUsed,
		"the Activity Monitor used formula: internal - purgeable + wired + compressed")
	require.True(t, snap.SwapOK)
	require.Equal(t, uint64(4<<30), snap.SwapTotal)
	require.Equal(t, uint64(1<<30), snap.SwapUsed)

	// Second sample 1s later: exact rates. Ticks +40 busy of +240
	// total; en0 +1000 rx +3000 tx (lo0 moves too and is filtered).
	fake.set(func(f *fakeDarwin) {
		f.ticks = cpuTicksDarwin{user: 150, system: 50, idle: 1000, nice: 0}
		f.rib = ribDump(ifInfo2Record(1, 99999, 99999), ifInfo2Record(4, 2000, 5000))
	})
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK)
	require.InDelta(t, 100*40.0/240.0, snap.CPUPct, 0.001)
	require.True(t, snap.NetOK)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)
	require.InDelta(t, 3000.0, snap.NetTxBps, 0.001)

	// Stale counters (hidden stretch): previous values kept, the
	// baseline re-stored, the next delta computes fresh.
	fake.set(func(f *fakeDarwin) {
		f.ticks = cpuTicksDarwin{user: 1150, system: 550, idle: 3000, nice: 0}
		f.rib = ribDump(ifInfo2Record(4, 500000, 700000))
	})
	clk.Advance(time.Hour)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK, "previous value survives a stale baseline")
	require.InDelta(t, 100*40.0/240.0, snap.CPUPct, 0.001)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)

	fake.set(func(f *fakeDarwin) {
		// +500 busy of +1000 total; +2000 rx +4000 tx over 2s.
		f.ticks = cpuTicksDarwin{user: 1500, system: 700, idle: 3500, nice: 0}
		f.rib = ribDump(ifInfo2Record(4, 502000, 704000))
	})
	clk.Advance(2 * time.Second)
	s.sample()
	snap = s.Snapshot()
	require.InDelta(t, 50.0, snap.CPUPct, 0.001)
	require.InDelta(t, 1000.0, snap.NetRxBps, 0.001)
	require.InDelta(t, 2000.0, snap.NetTxBps, 0.001)

	// A wrapped tick counter skips one update, value kept.
	fake.set(func(f *fakeDarwin) {
		f.ticks = cpuTicksDarwin{user: 5, system: 5, idle: 100, nice: 0}
	})
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK)
	require.InDelta(t, 50.0, snap.CPUPct, 0.001)
}

func TestDarwinPerReaderFailure(t *testing.T) {
	lr := &logRecorder{}
	fake := newFakeDarwin()
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := newDarwinSampler(fake, lr, clk)

	// Break every reader: each metric degrades alone, one log per
	// distinct message across repeated samples.
	for _, name := range []string{"cpuTicks", "memTotal", "swapRaw", "ifRIB", "gpuStats"} {
		fake.setErr(name, errors.New(name+" boom"))
	}
	s.sample()
	clk.Advance(time.Second)
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK)
	require.False(t, snap.MemOK)
	require.False(t, snap.SwapOK)
	require.False(t, snap.NetOK)
	require.False(t, snap.GPUOK)
	require.Equal(t, 1, lr.count("stats: cpu: host_statistics:"))
	require.Equal(t, 1, lr.count("stats: mem: hw.memsize:"))
	require.Equal(t, 1, lr.count("stats: swap: vm.swapusage:"))
	require.Equal(t, 1, lr.count("stats: net: NET_RT_IFLIST2:"))
	require.Equal(t, 1, lr.count("stats: gpu: ioaccelerator: gpuStats boom"))

	// Heal everything: metrics come back (the rate baseline survived).
	for _, name := range []string{"cpuTicks", "memTotal", "swapRaw", "ifRIB", "gpuStats"} {
		fake.setErr(name, nil)
	}
	fake.set(func(f *fakeDarwin) {
		f.ticks = cpuTicksDarwin{user: 200, system: 100, idle: 1600, nice: 20}
	})
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.MemOK)
	require.True(t, snap.SwapOK)
	require.True(t, snap.GPUOK, "the gpu read heals with its reader")
	require.Equal(t, 37.0, snap.GPUPct)

	// A registry that stops publishing utilization keys degrades gpu
	// alone, with its own once-only log line.
	fake.set(func(f *fakeDarwin) { f.gpu = []map[string]int64{{}} })
	clk.Advance(time.Second)
	s.sample()
	clk.Advance(time.Second)
	s.sample()
	require.False(t, s.Snapshot().GPUOK)
	require.Equal(t, 1, lr.count("stats: gpu: ioaccelerator: no utilization statistics published"))
	fake.set(func(f *fakeDarwin) { f.gpu = []map[string]int64{{"Renderer Utilization %": 8}} })

	// vmStat failing alone: mem degrades, swap unaffected -- and the
	// gpu read comes back through the renderer-key fallback.
	fake.setErr("vmStat", errors.New("vm boom"))
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.False(t, snap.MemOK)
	require.True(t, snap.SwapOK)
	require.True(t, snap.GPUOK)
	require.Equal(t, 8.0, snap.GPUPct)
	require.Equal(t, 1, lr.count("stats: mem: vm_statistics64:"))

	// ifNames failing alone: net degrades.
	fake.setErr("vmStat", nil)
	fake.setErr("ifNames", errors.New("names boom"))
	clk.Advance(time.Second)
	s.sample()
	require.False(t, s.Snapshot().NetOK)
	require.Equal(t, 1, lr.count("stats: net: interfaces:"))

	// A corrupt RIB dump degrades net through the decoder.
	fake.setErr("ifNames", nil)
	fake.set(func(f *fakeDarwin) { f.rib = []byte{1, 2, 3} })
	clk.Advance(time.Second)
	s.sample()
	require.False(t, s.Snapshot().NetOK)

	// A short swap payload degrades swap through the decoder.
	fake.set(func(f *fakeDarwin) { f.swap = []byte{1, 2, 3} })
	clk.Advance(time.Second)
	s.sample()
	require.False(t, s.Snapshot().SwapOK)
}

func TestDarwinNilIndividualReaders(t *testing.T) {
	// Individually missing readers flag their metric off silently
	// (only reachable via seam injection; production binds all).
	lr := &logRecorder{}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	opt := Options{GOOS: "darwin", Logf: lr.logf}
	opt.now = clk.Now
	opt.darwin = &darwinReaders{
		memTotal: func() (uint64, error) { return 8 << 30, nil },
		vmStat: func() (vmStat64, error) {
			return vmStat64{internalPages: 10, wired: 5, pageSize: 4096}, nil
		},
	}
	s := New(opt)
	require.True(t, s.hasSources())
	s.sample()
	clk.Advance(time.Second)
	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.MemOK)
	require.False(t, snap.CPUOK)
	require.False(t, snap.SwapOK)
	require.False(t, snap.NetOK)
	require.False(t, snap.GPUOK)
}

func TestDarwinSwapZeroTotalIsValid(t *testing.T) {
	lr := &logRecorder{}
	fake := newFakeDarwin()
	fake.set(func(f *fakeDarwin) { f.swap = xswUsage(0, 0, 0) })
	s := newDarwinSampler(fake, lr, &fakeClock{t: time.Unix(1000, 0)})
	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.SwapOK, "empty dynamic swap is a valid answer (the frontend renders 0M, never a dash)")
	require.Zero(t, snap.SwapTotal)
	require.Zero(t, snap.SwapUsed)
}

func TestDarwinLifecycleHiddenReadsNothing(t *testing.T) {
	lr := &logRecorder{}
	fake := newFakeDarwin()
	rec := &snapRecorder{}
	opt := Options{GOOS: "darwin", Logf: lr.logf, Interval: 25 * time.Millisecond, OnUpdate: rec.record}
	opt.darwin = fake.readers()
	s := New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// Hidden: not one reader call -- the zero-IO invariant.
	time.Sleep(120 * time.Millisecond)
	require.Zero(t, fake.callCount(), "hidden iterations must not touch the readers")
	require.Zero(t, rec.count())
	require.Equal(t, Snapshot{}, s.Snapshot())

	// Summon: the kick samples immediately; point-in-time metrics live.
	s.SetVisible(true)
	require.Eventually(t, func() bool { return rec.count() >= 1 },
		2*time.Second, 5*time.Millisecond)
	require.Positive(t, fake.callCount())
	require.Eventually(t, func() bool {
		sn := s.Snapshot()
		return sn.MemOK && sn.SwapOK
	}, 2*time.Second, 5*time.Millisecond)

	// The follow-up/tick turns the rates live under the real clock
	// (identical counters give a zero-rate ok answer for net; cpu
	// needs an advance, so move the ticks).
	fake.set(func(f *fakeDarwin) {
		f.ticks = cpuTicksDarwin{user: 200, system: 100, idle: 1600, nice: 20}
	})
	require.Eventually(t, func() bool { return s.Snapshot().NetOK },
		2*time.Second, 5*time.Millisecond)

	// Hide: reader calls stop.
	s.SetVisible(false)
	time.Sleep(80 * time.Millisecond)
	calls := fake.callCount()
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, calls, fake.callCount(), "no reads while hidden")
}

/* --- guards ---------------------------------------------------------- */

func TestDarwinReadersOK(t *testing.T) {
	require.False(t, (*darwinReaders)(nil).ok())
	require.False(t, (&darwinReaders{}).ok())
	require.True(t, (&darwinReaders{ifNames: func() (map[int]string, error) { return nil, nil }}).ok())
	require.True(t, (&darwinReaders{gpuStats: func() ([]map[string]int64, error) { return nil, nil }}).ok())
	require.True(t, newFakeDarwin().readers().ok())
}
