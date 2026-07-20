//go:build darwin

package sysstats

// Real-call tests for the production darwin readers. ABI offsets,
// mach behavior, and sane magnitudes can only be proven on a real
// darwin host, so these run UN-GATED on the mac CI job (headless --
// none of them needs WindowServer); every logical property is already
// pinned platform-independently in darwin_test.go.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadCPUTicksReal(t *testing.T) {
	a, err := readCPUTicks()
	require.NoError(t, err)
	c1 := cpuCountersFromTicks(a)
	require.Positive(t, c1.total, "a booted mac has accumulated ticks")
	require.Positive(t, c1.busy)

	time.Sleep(50 * time.Millisecond)
	b, err := readCPUTicks()
	require.NoError(t, err)
	c2 := cpuCountersFromTicks(b)
	require.GreaterOrEqual(t, c2.total, c1.total, "tick counters are monotonic")
	require.GreaterOrEqual(t, c2.busy, c1.busy)
}

func TestReadMemReal(t *testing.T) {
	total, err := readMemTotal()
	require.NoError(t, err)
	require.Positive(t, total)

	vm, err := readVMStat()
	require.NoError(t, err)
	require.Positive(t, vm.pageSize, "host_page_size answers")
	used := memFromVMStat(vm)
	require.Positive(t, used, "a running mac uses memory")
	require.Less(t, used, total, "the used figure stays under the physical size")
}

func TestReadSwapReal(t *testing.T) {
	raw, err := readSwapRaw()
	require.NoError(t, err)
	total, used, err := decodeXswUsage(raw)
	require.NoError(t, err)
	// macOS swap is dynamic: total 0 is legal AND the common state on
	// an idle machine (the CI runners included) -- exactly the field
	// report's shape, where the startup log showed swap=vm.swapusage
	// wired yet SWP rendered a dash. Used can never exceed the total
	// either way.
	require.LessOrEqual(t, used, total)

	// The full sampler pipeline must report that reading as LIVE
	// (SwapOK true; the frontend then renders 0M for a zero total,
	// never a dash) -- the mechanical gate for the report: only a
	// failed read may degrade the metric.
	lr := &logRecorder{}
	s := New(Options{GOOS: "darwin", Logf: lr.logf})
	var snap Snapshot
	s.sampleMem(&snap)
	require.True(t, snap.SwapOK, "a healthy vm.swapusage read is a live value whatever the total")
	require.LessOrEqual(t, snap.SwapUsed, snap.SwapTotal)
	require.Zero(t, lr.count("stats: swap:"))
}

func TestReadGPUStatsReal(t *testing.T) {
	// macos-latest CI runners are VMs whose paravirtual GPU may
	// legitimately publish NO IOAccelerator PerformanceStatistics (or
	// no IOAccelerator service at all), so this test cannot demand a
	// utilization value. It hard-asserts the CLEAN semantics instead:
	// the registry match itself never errors on a real darwin host
	// (zero matches is a KERN_SUCCESS empty iterator, not a failure),
	// and WHEN a utilization key is published -- real hardware, e.g.
	// the field report's M-series machine -- the derived percentage is
	// a sane 0..100. Unavailable degrades to ok=false, the honest
	// dash, never an error or a fake number.
	stats, err := readGPUStats()
	require.NoError(t, err, "matching IOAccelerator services must not fail, even with zero hits")
	pct, ok := gpuPctFromStats(stats)
	if ok {
		require.GreaterOrEqual(t, pct, 0.0)
		require.LessOrEqual(t, pct, 100.0)
		t.Logf("IOAccelerator utilization: %.0f%% across %d accelerator entries", pct, len(stats))
	} else {
		require.Zero(t, pct)
		t.Logf("no IOAccelerator utilization published (%d accelerator entries; VM runner?)", len(stats))
	}
}

func TestReadIfRIBReal(t *testing.T) {
	rib, err := readIfRIB()
	require.NoError(t, err)
	counters, err := decodeIfList2(rib)
	require.NoError(t, err, "the real RIB parses at the documented offsets")
	require.NotEmpty(t, counters)

	names, err := readIfNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)
	matched := 0
	for idx := range counters {
		if _, ok := names[idx]; ok {
			matched++
		}
	}
	require.Positive(t, matched, "decoded indexes intersect net.Interfaces")
}

// TestDarwinEndToEndReal drives the full production path: New with
// defaults on a real darwin host, two samples a real second apart,
// live cpu/mem/swap/net -- and the GPU either live in 0..100 (real
// hardware) or the honest logged dash (VM runners whose paravirtual
// GPU publishes no PerformanceStatistics; see TestReadGPUStatsReal).
func TestDarwinEndToEndReal(t *testing.T) {
	lr := &logRecorder{}
	s := New(Options{GOOS: "darwin", Logf: lr.logf})
	require.True(t, s.hasSources())
	require.Equal(t, 1, lr.count("gpu=ioaccelerator"),
		"the source line names the real gpu source now")

	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.MemOK)
	require.Positive(t, snap.MemTotal)
	require.Positive(t, snap.MemUsed)
	require.True(t, snap.SwapOK)

	// The scheduler tick counters advance at ~100Hz; a real second
	// guarantees a positive total delta for the cpu rate (net accepts
	// equal counters as a zero rate).
	time.Sleep(1200 * time.Millisecond)
	s.sample()
	snap = s.Snapshot()
	require.True(t, snap.CPUOK, "second sample yields a live cpu rate")
	require.GreaterOrEqual(t, snap.CPUPct, 0.0)
	require.LessOrEqual(t, snap.CPUPct, 100.0)
	require.True(t, snap.NetOK, "second sample yields live net rates")
	require.GreaterOrEqual(t, snap.NetRxBps, 0.0)
	require.GreaterOrEqual(t, snap.NetTxBps, 0.0)
	if snap.GPUOK {
		require.GreaterOrEqual(t, snap.GPUPct, 0.0)
		require.LessOrEqual(t, snap.GPUPct, 100.0)
	} else {
		require.Equal(t, 1, lr.count("stats: gpu: ioaccelerator:"),
			"an unavailable GPU logs its reason exactly once")
	}
}
