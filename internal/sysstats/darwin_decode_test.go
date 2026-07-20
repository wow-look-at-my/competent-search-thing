package sysstats

// Synthetic-buffer builders and the pure darwin decoder/derivation
// tests, split from darwin_test.go (which keeps the scripted-reader
// sampler tests). Deliberately UNTAGGED like its sibling -- these run
// on the linux CI job AND the mac runner.

import (
	"encoding/binary"
	"testing"

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

func TestGpuPctFromStats(t *testing.T) {
	tests := []struct {
		name  string
		stats []map[string]int64
		want  float64
		ok    bool
	}{
		{name: "nil stats: nothing published", stats: nil, want: 0, ok: false},
		{name: "no accelerators", stats: []map[string]int64{}, want: 0, ok: false},
		{
			name:  "accelerator without utilization keys (VM paravirtual GPU)",
			stats: []map[string]int64{{"In use system memory": 12345}},
			want:  0, ok: false,
		},
		{
			name:  "device utilization key",
			stats: []map[string]int64{{"Device Utilization %": 37}},
			want:  37, ok: true,
		},
		{
			name:  "renderer fallback when the device key is absent",
			stats: []map[string]int64{{"Renderer Utilization %": 21}},
			want:  21, ok: true,
		},
		{
			name: "device key preferred over renderer within one accelerator",
			stats: []map[string]int64{{
				"Device Utilization %":   10,
				"Renderer Utilization %": 90,
			}},
			want: 10, ok: true,
		},
		{
			name: "multiple accelerators report the busiest",
			stats: []map[string]int64{
				{"Device Utilization %": 12},
				{"Renderer Utilization %": 55},
				{},
			},
			want: 55, ok: true,
		},
		{
			name:  "values clamp into 0..100",
			stats: []map[string]int64{{"Device Utilization %": 250}},
			want:  100, ok: true,
		},
		{
			name: "negative garbage clamps to zero but still counts as live",
			stats: []map[string]int64{
				{"Device Utilization %": -5},
			},
			want: 0, ok: true,
		},
		{
			name: "a keyless accelerator does not mask a keyed one",
			stats: []map[string]int64{
				{},
				{"Device Utilization %": 7},
			},
			want: 7, ok: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, ok := gpuPctFromStats(tt.stats)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, pct)
		})
	}
}
