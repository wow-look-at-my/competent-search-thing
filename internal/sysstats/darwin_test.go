package sysstats

// Headless tests for the darwin stats sources: pure decoders and
// derivations over synthetic buffers, plus the darwin sample paths
// over scripted readers. Deliberately UNTAGGED -- these run on the
// linux CI job AND the mac runner, so every logical property is
// pinned platform-independently; only the thin production readers
// need the real-call tests in readers_darwin_test.go.

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

/* --- synthetic fixture builders ------------------------------------ */

// ifInfo2Record builds one 160-byte if_msghdr2 record (the real
// kernel size: 32-byte header + 128-byte if_data64) with the given
// interface index and byte counters at the documented offsets.
func ifInfo2Record(index int, rx, tx uint64) []byte {
	rec := make([]byte, 160)
	binary.LittleEndian.PutUint16(rec[0:2], uint16(len(rec))) // ifm_msglen
	rec[2] = 5                                                // ifm_version (RTM_VERSION)
	rec[3] = rtmIfInfo2                                       // ifm_type
	binary.LittleEndian.PutUint16(rec[ifm2IndexOff:ifm2IndexOff+2], uint16(index))
	binary.LittleEndian.PutUint64(rec[ifm2IBytesOff:ifm2IBytesOff+8], rx)
	binary.LittleEndian.PutUint64(rec[ifm2OBytesOff:ifm2OBytesOff+8], tx)
	return rec
}

// otherRecord builds a non-IFINFO2 RIB record (e.g. the RTM_NEWADDR
// records interleaved after each interface) of the given length.
func otherRecord(typ byte, length int) []byte {
	rec := make([]byte, length)
	binary.LittleEndian.PutUint16(rec[0:2], uint16(length))
	rec[2] = 5
	rec[3] = typ
	return rec
}

func ribDump(records ...[]byte) []byte {
	var out []byte
	for _, r := range records {
		out = append(out, r...)
	}
	return out
}

// xswUsage builds a vm.swapusage payload: total/avail/used uint64s
// plus the trailing pagesize+flag bytes the decoder ignores.
func xswUsage(total, avail, used uint64) []byte {
	raw := make([]byte, 32)
	binary.LittleEndian.PutUint64(raw[0:8], total)
	binary.LittleEndian.PutUint64(raw[8:16], avail)
	binary.LittleEndian.PutUint64(raw[16:24], used)
	binary.LittleEndian.PutUint32(raw[24:28], 4096)
	return raw
}

/* --- decoders and derivations -------------------------------------- */

func TestDecodeXswUsage(t *testing.T) {
	total, used, err := decodeXswUsage(xswUsage(8<<30, 7<<30, 1<<30))
	require.NoError(t, err)
	require.Equal(t, uint64(8<<30), total)
	require.Equal(t, uint64(1<<30), used)

	// Exactly 24 bytes (no trailing pagesize) still decodes.
	total, used, err = decodeXswUsage(xswUsage(2<<20, 2<<20, 0)[:24])
	require.NoError(t, err)
	require.Equal(t, uint64(2<<20), total)
	require.Zero(t, used)

	_, _, err = decodeXswUsage(xswUsage(1, 1, 1)[:23])
	require.Error(t, err, "short payloads are rejected")
	_, _, err = decodeXswUsage(nil)
	require.Error(t, err)
}

func TestDecodeIfList2(t *testing.T) {
	dump := ribDump(
		otherRecord(0x0c, 20), // RTM_NEWADDR-shaped noise before
		ifInfo2Record(4, 1000, 2000),
		otherRecord(0x0c, 24),
		ifInfo2Record(11, 30, 40),
	)
	got, err := decodeIfList2(dump)
	require.NoError(t, err)
	require.Equal(t, map[int]ifCountersDarwin{
		4:  {rx: 1000, tx: 2000},
		11: {rx: 30, tx: 40},
	}, got)
}

func TestDecodeIfList2Corruption(t *testing.T) {
	// A record claiming more bytes than remain: corruption, error.
	long := ifInfo2Record(1, 1, 1)
	binary.LittleEndian.PutUint16(long[0:2], 500)
	_, err := decodeIfList2(long)
	require.Error(t, err)

	// A zero/short msglen must error, never loop forever.
	zero := ifInfo2Record(1, 1, 1)
	binary.LittleEndian.PutUint16(zero[0:2], 0)
	_, err = decodeIfList2(zero)
	require.Error(t, err)

	// A truncated prologue (fewer than 4 trailing bytes) errors.
	_, err = decodeIfList2(ribDump(ifInfo2Record(1, 1, 1), []byte{9, 0, 5}))
	require.Error(t, err)

	// An IFINFO2 record too short for the counters is skipped; with
	// nothing else usable the dump is an error (never a silent zero).
	short := otherRecord(rtmIfInfo2, 20)
	_, err = decodeIfList2(ribDump(short))
	require.Error(t, err)
	_, err = decodeIfList2(nil)
	require.Error(t, err, "an empty dump has no records")
	_, err = decodeIfList2(otherRecord(0x0c, 20))
	require.Error(t, err, "a dump with only non-IFINFO2 records has no counters")

	// The short IFINFO2 record does not poison later good ones.
	got, err := decodeIfList2(ribDump(short, ifInfo2Record(7, 5, 6)))
	require.NoError(t, err)
	require.Equal(t, map[int]ifCountersDarwin{7: {rx: 5, tx: 6}}, got)
}

func TestMemFromVMStat(t *testing.T) {
	tests := []struct {
		name string
		v    vmStat64
		want uint64
	}{
		{
			name: "activity monitor formula, 4k pages",
			v:    vmStat64{internalPages: 1000, purgeable: 100, wired: 200, compressor: 50, pageSize: 4096},
			want: (1000 - 100 + 200 + 50) * 4096,
		},
		{
			name: "16k apple silicon pages",
			v:    vmStat64{internalPages: 10, purgeable: 0, wired: 5, compressor: 0, pageSize: 16384},
			want: 15 * 16384,
		},
		{
			name: "purgeable exceeding internal clamps to zero app memory",
			v:    vmStat64{internalPages: 10, purgeable: 50, wired: 3, compressor: 2, pageSize: 4096},
			want: 5 * 4096,
		},
		{
			name: "zero everything",
			v:    vmStat64{pageSize: 16384},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, memFromVMStat(tt.v))
		})
	}
}

func TestCPUCountersFromTicks(t *testing.T) {
	c := cpuCountersFromTicks(cpuTicksDarwin{user: 100, system: 50, idle: 800, nice: 10})
	require.Equal(t, cpuCounters{total: 960, busy: 160}, c)

	// A wrapped uint32 tick counter makes cur < prev; cpuRate skips
	// exactly like the linux wrap.
	prev := cpuCountersFromTicks(cpuTicksDarwin{user: 4_000_000_000, idle: 100})
	cur := cpuCountersFromTicks(cpuTicksDarwin{user: 5, idle: 200})
	_, ok := cpuRate(prev, cur)
	require.False(t, ok, "wrap skips the update")

	// A normal advance yields the exact busy share.
	next := cpuCountersFromTicks(cpuTicksDarwin{user: 150, system: 50, idle: 1000, nice: 0})
	pct, ok := cpuRate(c, next)
	require.True(t, ok)
	require.InDelta(t, 100*40.0/240.0, pct, 0.001)
}

func TestIsVirtualInterfaceDarwin(t *testing.T) {
	skip := []string{"lo0", "gif0", "stf0", "awdl0", "llw0", "utun3", "ap1",
		"bridge0", "anpi0", "pktap1", "feth0", "vmnet8"}
	for _, name := range skip {
		require.True(t, isVirtualInterfaceDarwin(name), name)
	}
	keep := []string{"en0", "en5", "bond0"}
	for _, name := range keep {
		require.False(t, isVirtualInterfaceDarwin(name), name)
	}
}

func TestNetCountersFromIfList2(t *testing.T) {
	counters := map[int]ifCountersDarwin{
		1: {rx: 999, tx: 999},   // lo0: filtered
		4: {rx: 1000, tx: 2000}, // en0: counted
		5: {rx: 10, tx: 20},     // en5: counted
		7: {rx: 555, tx: 555},   // utun0: filtered
		9: {rx: 777, tx: 777},   // unnamed index: skipped
	}
	names := map[int]string{1: "lo0", 4: "en0", 5: "en5", 7: "utun0"}
	c := netCountersFromIfList2(counters, names)
	require.Equal(t, netCounters{rx: 1010, tx: 2020}, c)
}

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
	require.Equal(t, 1, lr.count("stats: sources: cpu=host_statistics mem=vm_statistics64+hw.memsize swap=vm.swapusage net=sysctl(iflist2) gpu=none"),
		"the darwin source line names every source and the honest gpu=none")
	require.Zero(t, lr.count("no sources on this platform"))
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

	// Baseline: point-in-time metrics live, rates not yet, GPU an
	// honest dash.
	s.sample()
	snap := s.Snapshot()
	require.False(t, snap.CPUOK, "no rate before a second read")
	require.False(t, snap.NetOK)
	require.False(t, snap.GPUOK, "darwin GPU is deliberately dashed")
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
	for _, name := range []string{"cpuTicks", "memTotal", "swapRaw", "ifRIB"} {
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
	require.Equal(t, 1, lr.count("stats: cpu: host_statistics:"))
	require.Equal(t, 1, lr.count("stats: mem: hw.memsize:"))
	require.Equal(t, 1, lr.count("stats: swap: vm.swapusage:"))
	require.Equal(t, 1, lr.count("stats: net: NET_RT_IFLIST2:"))

	// Heal everything: metrics come back (the rate baseline survived).
	for _, name := range []string{"cpuTicks", "memTotal", "swapRaw", "ifRIB"} {
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

	// vmStat failing alone: mem degrades, swap unaffected.
	fake.setErr("vmStat", errors.New("vm boom"))
	clk.Advance(time.Second)
	s.sample()
	snap = s.Snapshot()
	require.False(t, snap.MemOK)
	require.True(t, snap.SwapOK)
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
	require.True(t, newFakeDarwin().readers().ok())
}
