package sysstats

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cpuCounters is the aggregate jiffy state from /proc/stat's "cpu "
// line: total across all accounted fields and the busy share (total
// minus idle minus iowait).
type cpuCounters struct {
	total uint64
	busy  uint64
}

// parseCPUStat extracts cpuCounters from a /proc/stat dump. Only the
// aggregate "cpu " line is consumed. The first 8 fields (user nice
// system idle iowait irq softirq steal) are summed -- guest and
// guest_nice are already included in user/nice, so counting them
// would double-book guest time.
func parseCPUStat(data []byte) (cpuCounters, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 5 {
			return cpuCounters{}, fmt.Errorf("aggregate cpu line has %d fields, need at least 5", len(fields))
		}
		if len(fields) > 8 {
			fields = fields[:8]
		}
		var c cpuCounters
		var idle, iowait uint64
		for i, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuCounters{}, fmt.Errorf("aggregate cpu field %d: %w", i, err)
			}
			c.total += v
			switch i {
			case 3:
				idle = v
			case 4:
				iowait = v
			}
		}
		c.busy = c.total - idle - iowait
		return c, nil
	}
	return cpuCounters{}, errors.New("no aggregate cpu line")
}

// cpuRate turns two counter states into a busy percentage. A zero or
// negative total delta (duplicate read, wrapped counter) or a busy
// counter running backwards reports ok=false -- the caller skips that
// update and keeps its previous value.
func cpuRate(prev, cur cpuCounters) (pct float64, ok bool) {
	if cur.total <= prev.total || cur.busy < prev.busy {
		return 0, false
	}
	return clampPct(100 * float64(cur.busy-prev.busy) / float64(cur.total-prev.total)), true
}

// memInfo carries the four /proc/meminfo values the stats row needs,
// in bytes, each with a presence flag (a kernel before 3.14 has no
// MemAvailable; a swapless build could in principle omit the swap
// lines).
type memInfo struct {
	memTotal, memAvail  uint64
	haveTotal           bool
	haveAvail           bool
	swapTotal, swapFree uint64
	haveSwapTotal       bool
	haveSwapFree        bool
}

// parseMemInfo scans a /proc/meminfo dump for MemTotal, MemAvailable,
// SwapTotal, and SwapFree. Values are kB on every kernel; they are
// returned as bytes. A malformed value line counts as missing.
func parseMemInfo(data []byte) memInfo {
	var mi memInfo
	for _, line := range strings.Split(string(data), "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		var dst *uint64
		var have *bool
		switch key {
		case "MemTotal":
			dst, have = &mi.memTotal, &mi.haveTotal
		case "MemAvailable":
			dst, have = &mi.memAvail, &mi.haveAvail
		case "SwapTotal":
			dst, have = &mi.swapTotal, &mi.haveSwapTotal
		case "SwapFree":
			dst, have = &mi.swapFree, &mi.haveSwapFree
		default:
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		*dst, *have = v*1024, true
	}
	return mi
}

// netCounters is the summed receive/transmit byte state over the real
// (non-virtual) network interfaces.
type netCounters struct {
	rx uint64
	tx uint64
}

// virtualIfacePrefixes are interface-name prefixes whose traffic is
// local plumbing (bridges, container veths, tunnels, VPNs) rather
// than machine throughput; they are excluded from the summed rates.
// Physical-ish names -- eth*, en*, wl*, ww*, bond* -- pass through.
var virtualIfacePrefixes = []string{
	"veth", "docker", "br-", "virbr", "vnet", "tap", "tun", "wg",
	"zt", "dummy", "ifb", "kube", "cni", "flannel", "cali",
}

// isVirtualInterface reports whether name is the loopback or matches a
// virtual-interface prefix.
func isVirtualInterface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseNetDev sums rx_bytes/tx_bytes over the real interfaces of a
// /proc/net/dev dump. Header lines (no colon) and malformed interface
// lines are skipped; a dump with no interface line at all is an error
// so a garbage file degrades the metric instead of reporting a silent
// zero.
func parseNetDev(data []byte) (netCounters, error) {
	var c netCounters
	seen := false
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		seen = true
		if isVirtualInterface(name) {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(fields[0], 10, 64)
		tx, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		c.rx += rx
		c.tx += tx
	}
	if !seen {
		return c, errors.New("no interface lines")
	}
	return c, nil
}

// netRate turns two counter states dt apart into bytes-per-second
// rates. A non-positive dt or a counter running backwards (wrap, an
// interface vanishing mid-flight) reports ok=false; the caller skips
// that update.
func netRate(prev, cur netCounters, dt time.Duration) (rx, tx float64, ok bool) {
	if dt <= 0 || cur.rx < prev.rx || cur.tx < prev.tx {
		return 0, 0, false
	}
	secs := dt.Seconds()
	return float64(cur.rx-prev.rx) / secs, float64(cur.tx-prev.tx) / secs, true
}

// parseLeadingInt parses the leading decimal integer of s's first
// non-blank line -- the shape of both the amdgpu busy-percent file
// ("42\n") and nvidia-smi's --format=csv,noheader,nounits output
// (one "42" line per GPU; the first wins). Trailing text is ignored.
func parseLeadingInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[:nl]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("no leading integer in %q", s)
	}
	v, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, fmt.Errorf("leading integer in %q: %w", s, err)
	}
	return v, nil
}
