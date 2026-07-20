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
// live cpu/mem/swap/net -- and the GPU dash.
func TestDarwinEndToEndReal(t *testing.T) {
	lr := &logRecorder{}
	s := New(Options{GOOS: "darwin", Logf: lr.logf})
	require.True(t, s.hasSources())
	require.Equal(t, 1, lr.count("gpu=none"))

	s.sample()
	snap := s.Snapshot()
	require.True(t, snap.MemOK)
	require.Positive(t, snap.MemTotal)
	require.Positive(t, snap.MemUsed)
	require.True(t, snap.SwapOK)
	require.False(t, snap.GPUOK, "darwin GPU is the honest dash")

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
	require.False(t, snap.GPUOK)
}
