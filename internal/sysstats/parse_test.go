package sysstats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// procStat builds a plausible /proc/stat dump around the given
// aggregate line.
func procStat(aggregate string) []byte {
	return []byte(aggregate + "\n" +
		"cpu0 100 0 50 800 25 0 5 0 0 0\n" +
		"intr 12345 0 0\n" +
		"ctxt 987654\n" +
		"btime 1700000000\n")
}

func TestParseCPUStat(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		want    cpuCounters
		wantErr bool
	}{
		{
			name: "full modern line",
			// user nice system idle iowait irq softirq steal guest guest_nice
			data: procStat("cpu  100 10 50 800 25 5 5 5 0 0"),
			// guest fields excluded: total = 100+10+50+800+25+5+5+5 = 1000
			// busy = 1000 - 800 - 25 = 175
			want: cpuCounters{total: 1000, busy: 175},
		},
		{
			name: "guest fields are ignored",
			data: procStat("cpu  100 10 50 800 25 5 5 5 999 999"),
			want: cpuCounters{total: 1000, busy: 175},
		},
		{
			name: "minimal five fields",
			data: procStat("cpu  100 0 100 700 100"),
			want: cpuCounters{total: 1000, busy: 200},
		},
		{name: "too few fields", data: procStat("cpu  100 0 100 700"), wantErr: true},
		{name: "non-numeric field", data: procStat("cpu  100 0 abc 700 100"), wantErr: true},
		{name: "no aggregate line", data: []byte("cpu0 1 2 3 4 5 6 7 8\nintr 5\n"), wantErr: true},
		{name: "empty", data: nil, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCPUStat(tc.data)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCPURate(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur cpuCounters
		wantPct   float64
		wantOK    bool
	}{
		{
			name: "half busy",
			prev: cpuCounters{total: 1000, busy: 100},
			cur:  cpuCounters{total: 1200, busy: 200},
			// busyDelta 100 / totalDelta 200
			wantPct: 50, wantOK: true,
		},
		{
			name:    "idle",
			prev:    cpuCounters{total: 1000, busy: 100},
			cur:     cpuCounters{total: 1200, busy: 100},
			wantPct: 0, wantOK: true,
		},
		{
			name: "fully busy clamps at 100",
			prev: cpuCounters{total: 1000, busy: 100},
			cur:  cpuCounters{total: 1200, busy: 350},
			// busyDelta 250 > totalDelta 200 (jiffy skew) -> clamp
			wantPct: 100, wantOK: true,
		},
		{
			name:   "zero total delta skipped",
			prev:   cpuCounters{total: 1000, busy: 100},
			cur:    cpuCounters{total: 1000, busy: 100},
			wantOK: false,
		},
		{
			name:   "total wrap skipped",
			prev:   cpuCounters{total: 1000, busy: 100},
			cur:    cpuCounters{total: 500, busy: 50},
			wantOK: false,
		},
		{
			name:   "busy wrap skipped",
			prev:   cpuCounters{total: 1000, busy: 100},
			cur:    cpuCounters{total: 1200, busy: 50},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pct, ok := cpuRate(tc.prev, tc.cur)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.InDelta(t, tc.wantPct, pct, 0.001)
			}
		})
	}
}

const memInfoFull = `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:   10000000 kB
Buffers:          500000 kB
SwapCached:            0 kB
SwapTotal:       4000000 kB
SwapFree:        3000000 kB
HugePages_Total:       0
`

func TestParseMemInfo(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		mi := parseMemInfo([]byte(memInfoFull))
		require.True(t, mi.haveTotal)
		require.True(t, mi.haveAvail)
		require.True(t, mi.haveSwapTotal)
		require.True(t, mi.haveSwapFree)
		require.Equal(t, uint64(16000000)*1024, mi.memTotal)
		require.Equal(t, uint64(10000000)*1024, mi.memAvail)
		require.Equal(t, uint64(4000000)*1024, mi.swapTotal)
		require.Equal(t, uint64(3000000)*1024, mi.swapFree)
	})
	t.Run("missing MemAvailable", func(t *testing.T) {
		mi := parseMemInfo([]byte("MemTotal: 100 kB\nSwapTotal: 10 kB\nSwapFree: 5 kB\n"))
		require.True(t, mi.haveTotal)
		require.False(t, mi.haveAvail)
		require.True(t, mi.haveSwapTotal)
	})
	t.Run("malformed value counts as missing", func(t *testing.T) {
		mi := parseMemInfo([]byte("MemTotal: lots kB\nMemAvailable: 10 kB\n"))
		require.False(t, mi.haveTotal)
		require.True(t, mi.haveAvail)
	})
	t.Run("value without unit", func(t *testing.T) {
		mi := parseMemInfo([]byte("SwapTotal: 0\nSwapFree: 0\n"))
		require.True(t, mi.haveSwapTotal)
		require.True(t, mi.haveSwapFree)
		require.Zero(t, mi.swapTotal)
	})
	t.Run("empty value", func(t *testing.T) {
		mi := parseMemInfo([]byte("MemTotal:\n"))
		require.False(t, mi.haveTotal)
	})
	t.Run("empty and garbage", func(t *testing.T) {
		require.Equal(t, memInfo{}, parseMemInfo(nil))
		require.Equal(t, memInfo{}, parseMemInfo([]byte("no colons here\njust text\n")))
	})
}

const netDevHeader = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
`

func TestParseNetDev(t *testing.T) {
	t.Run("sums real interfaces only", func(t *testing.T) {
		data := netDevHeader +
			"    lo: 9000 90 0 0 0 0 0 0 9000 90 0 0 0 0 0 0\n" +
			"  eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n" +
			" wlan0: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0\n" +
			"docker0: 5000 50 0 0 0 0 0 0 5000 50 0 0 0 0 0 0\n" +
			"veth12ab: 7000 70 0 0 0 0 0 0 7000 70 0 0 0 0 0 0\n"
		c, err := parseNetDev([]byte(data))
		require.NoError(t, err)
		require.Equal(t, netCounters{rx: 1100, tx: 2200}, c)
	})
	t.Run("malformed lines skipped", func(t *testing.T) {
		data := netDevHeader +
			"  eth0: 1000 10\n" + // too few fields
			"  eth1: abc 10 0 0 0 0 0 0 200 2 0 0 0 0 0 0\n" + // bad rx
			"  eth2: 100 10 0 0 0 0 0 0 xyz 2 0 0 0 0 0 0\n" + // bad tx
			"  eth3: 40 1 0 0 0 0 0 0 60 2 0 0 0 0 0 0\n"
		c, err := parseNetDev([]byte(data))
		require.NoError(t, err)
		require.Equal(t, netCounters{rx: 40, tx: 60}, c)
	})
	t.Run("all virtual is valid and zero", func(t *testing.T) {
		data := netDevHeader + "    lo: 9000 90 0 0 0 0 0 0 9000 90 0 0 0 0 0 0\n"
		c, err := parseNetDev([]byte(data))
		require.NoError(t, err)
		require.Equal(t, netCounters{}, c)
	})
	t.Run("no interface lines is an error", func(t *testing.T) {
		_, err := parseNetDev([]byte(netDevHeader))
		require.Error(t, err)
		_, err = parseNetDev(nil)
		require.Error(t, err)
		_, err = parseNetDev([]byte("total garbage\nno interfaces\n"))
		require.Error(t, err)
	})
}

func TestIsVirtualInterface(t *testing.T) {
	virtual := []string{
		"lo",
		"veth12ab", "docker0", "br-abc123", "virbr0", "vnet7",
		"tap0", "tun0", "tunl0", "wg0", "zt3jnzuiqf", "dummy0",
		"ifb0", "kube-bridge", "cni0", "flannel.1", "cali12345",
	}
	for _, name := range virtual {
		require.True(t, isVirtualInterface(name), "%s must be filtered", name)
	}
	real := []string{
		"eth0", "enp3s0", "eno1", "ens18", "wlan0", "wlp2s0",
		"wwan0", "bond0", "loop-free", // "loop-free" is contrived: only exact "lo" is loopback
	}
	for _, name := range real {
		require.False(t, isVirtualInterface(name), "%s must be kept", name)
	}
}

func TestNetRate(t *testing.T) {
	prev := netCounters{rx: 1000, tx: 2000}
	t.Run("normal", func(t *testing.T) {
		rx, tx, ok := netRate(prev, netCounters{rx: 3000, tx: 3000}, 2*time.Second)
		require.True(t, ok)
		require.InDelta(t, 1000, rx, 0.001)
		require.InDelta(t, 500, tx, 0.001)
	})
	t.Run("zero delta is a valid zero rate", func(t *testing.T) {
		rx, tx, ok := netRate(prev, prev, time.Second)
		require.True(t, ok)
		require.Zero(t, rx)
		require.Zero(t, tx)
	})
	t.Run("non-positive dt skipped", func(t *testing.T) {
		_, _, ok := netRate(prev, netCounters{rx: 2000, tx: 3000}, 0)
		require.False(t, ok)
		_, _, ok = netRate(prev, netCounters{rx: 2000, tx: 3000}, -time.Second)
		require.False(t, ok)
	})
	t.Run("wrap skipped", func(t *testing.T) {
		_, _, ok := netRate(prev, netCounters{rx: 500, tx: 3000}, time.Second)
		require.False(t, ok)
		_, _, ok = netRate(prev, netCounters{rx: 2000, tx: 500}, time.Second)
		require.False(t, ok)
	})
}

func TestParseLeadingInt(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "42\n", want: 42},
		{in: "42", want: 42},
		{in: "0\n", want: 0},
		{in: "  7  ", want: 7},
		{in: "42 %", want: 42},
		{in: "\n\n42\n17\n", want: 42}, // first non-blank line wins
		{in: "100\n", want: 100},
		{in: "", wantErr: true},
		{in: "N/A", wantErr: true},
		{in: "[Not Supported]", wantErr: true},
		{in: "-5", wantErr: true}, // busy files never go negative; a '-' is garbage
		{in: "99999999999999999999999999", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseLeadingInt(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestClampPct(t *testing.T) {
	require.Equal(t, 0.0, clampPct(-3))
	require.Equal(t, 55.5, clampPct(55.5))
	require.Equal(t, 100.0, clampPct(180))
}
