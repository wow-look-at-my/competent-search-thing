package sysstats

// The darwin (macOS) stats sources. This file is deliberately
// UNTAGGED: everything in it is pure logic over the darwinReaders
// seam -- byte decoders, derivations, the interface filter, and the
// darwin sample paths -- so it compiles and is unit-tested on every
// platform (the linux CI job included, over synthetic buffers and
// scripted readers). The thin production readers, which need mach
// calls (cgo) and darwin-only syscalls, live in readers_darwin.go;
// the !darwin twin readers_other.go binds no readers at all, so a
// non-darwin build asked for GOOS "darwin" without an injected seam
// degrades to the placeholders row.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// darwinReaders is the darwin source seam: one function per raw
// reading, bound to the real mach/sysctl calls by newDarwinReaders
// (readers_darwin.go) and to scripted fakes in tests (the gpuExec
// pattern). A nil member means "source absent" and degrades that one
// metric alone; readers run ONLY from the sampler goroutine's sample
// paths, so the zero-IO-while-hidden invariant holds by construction.
type darwinReaders struct {
	// cpuTicks reads the host-wide cumulative CPU tick counters
	// (host_statistics HOST_CPU_LOAD_INFO).
	cpuTicks func() (cpuTicksDarwin, error)
	// memTotal reads the physical memory size in bytes (hw.memsize).
	memTotal func() (uint64, error)
	// vmStat reads the page counts + page size behind the memory-used
	// derivation (host_statistics64 HOST_VM_INFO64 + host_page_size).
	vmStat func() (vmStat64, error)
	// swapRaw reads the raw vm.swapusage sysctl payload (struct
	// xsw_usage; decoded by decodeXswUsage).
	swapRaw func() ([]byte, error)
	// ifRIB reads the NET_RT_IFLIST2 routing information base dump
	// (if_msghdr2 records; decoded by decodeIfList2).
	ifRIB func() ([]byte, error)
	// ifNames maps interface indexes to names (net.Interfaces).
	ifNames func() (map[int]string, error)
}

// ok reports whether any darwin source is bound at all -- false for
// the !darwin stub readers, which sends New to the placeholders path.
func (d *darwinReaders) ok() bool {
	return d != nil && (d.cpuTicks != nil || d.memTotal != nil || d.vmStat != nil ||
		d.swapRaw != nil || d.ifRIB != nil || d.ifNames != nil)
}

// cpuTicksDarwin is one host_statistics HOST_CPU_LOAD_INFO reading:
// cumulative scheduler ticks per CPU state, summed over all cores.
// Each counter is a natural_t (uint32) in the mach ABI, widened here;
// a wrapped counter shows up as cur < prev and takes the same
// skip-one-update path as a linux /proc/stat wrap (cpuRate ok=false).
type cpuTicksDarwin struct {
	user   uint64
	system uint64
	idle   uint64
	nice   uint64
}

// cpuCountersFromTicks maps a mach tick reading onto the shared rate
// state: busy = user + system + nice, total = busy + idle -- exactly
// the shape cpuRate consumes for /proc/stat.
func cpuCountersFromTicks(t cpuTicksDarwin) cpuCounters {
	busy := t.user + t.system + t.nice
	return cpuCounters{total: busy + t.idle, busy: busy}
}

// vmStat64 carries the vm_statistics64 page counts behind the darwin
// memory-used figure, plus the host page size in bytes (16384 on
// Apple Silicon -- never assume 4096).
type vmStat64 struct {
	internalPages uint64 // internal_page_count: anonymous (app) pages
	purgeable     uint64 // purgeable_count: reclaimable-at-will pages
	wired         uint64 // wire_count: unpageable kernel memory
	compressor    uint64 // compressor_page_count: compressed-store pages
	pageSize      uint64
}

// memFromVMStat derives the used-memory figure in bytes, matching
// what Activity Monitor calls "Memory Used":
//
//	used = (internal - purgeable)   ("App Memory")
//	     + wired                    ("Wired Memory")
//	     + compressor               ("Compressed")
//
// all in pages, times pageSize. This is the figure macOS users will
// diff the row against. "total - free" would massively overstate
// (free_count is tiny by design -- the kernel keeps files cached),
// and active+wired+compressed counts purgeable/file-backed active
// pages and reads high. The subtraction is clamped at zero
// defensively (purgeable > internal would otherwise wrap).
func memFromVMStat(v vmStat64) uint64 {
	app := int64(v.internalPages) - int64(v.purgeable)
	if app < 0 {
		app = 0
	}
	return (uint64(app) + v.wired + v.compressor) * v.pageSize
}

// decodeXswUsage decodes the vm.swapusage sysctl payload (struct
// xsw_usage): three little-endian uint64 fields -- total, available,
// used, all in BYTES already -- followed by a uint32 pagesize and an
// encryption flag this row does not need. x/sys/unix ships no darwin
// XswUsage type, so the offsets are fixed here: 0 total, 8 avail,
// 16 used, minimum length 24. A zero total is the valid "no swap in
// use" answer (macOS swap is dynamic), which the frontend renders as
// a dash per the SwapOK contract.
func decodeXswUsage(raw []byte) (total, used uint64, err error) {
	if len(raw) < 24 {
		return 0, 0, fmt.Errorf("xsw_usage payload is %d bytes, need at least 24", len(raw))
	}
	return binary.LittleEndian.Uint64(raw[0:8]), binary.LittleEndian.Uint64(raw[16:24]), nil
}

// The NET_RT_IFLIST2 record layout (fixed darwin ABI; both darwin
// architectures are little-endian LP64 with natural alignment, so the
// offsets are identical on arm64 and amd64). A RIB dump interleaves
// if_msghdr2 records (ifm_type RTM_IFINFO2) with per-address records;
// every record starts with the same prologue {uint16 ifm_msglen,
// uint8 ifm_version, uint8 ifm_type} and the walker advances by
// ifm_msglen.
const (
	rtmIfInfo2 = 0x12 // RTM_IFINFO2
	// if_msghdr2 offsets: ifm_index is the uint16 at 12; ifm_data
	// (struct if_data64) starts at 32, and within it the 64-bit
	// ifi_ibytes/ifi_obytes counters sit at +64 and +72.
	ifm2IndexOff  = 12
	ifm2IBytesOff = 32 + 64
	ifm2OBytesOff = 32 + 72
	ifm2MinLen    = ifm2OBytesOff + 8
)

// ifCountersDarwin is one interface's cumulative byte counters out of
// an if_msghdr2 record (if_data64: 64-bit, no 4 GiB wrap concerns).
type ifCountersDarwin struct {
	rx uint64
	tx uint64
}

// decodeIfList2 walks a NET_RT_IFLIST2 RIB dump and returns the
// cumulative rx/tx byte counters per interface index. Bounds-checked
// throughout (the fanotify_parse_linux.go pattern): a record whose
// length cannot hold its own prologue or runs past the buffer stops
// the walk as corruption, an RTM_IFINFO2 record too short for the
// counters is skipped, and a dump with no usable RTM_IFINFO2 record
// at all is an error so garbage degrades the metric instead of
// reporting a silent zero (the parseNetDev convention).
func decodeIfList2(rib []byte) (map[int]ifCountersDarwin, error) {
	out := map[int]ifCountersDarwin{}
	off := 0
	for off < len(rib) {
		if len(rib)-off < 4 {
			return nil, fmt.Errorf("truncated RIB record prologue at offset %d", off)
		}
		msglen := int(binary.LittleEndian.Uint16(rib[off : off+2]))
		typ := rib[off+3]
		if msglen < 4 || msglen > len(rib)-off {
			return nil, fmt.Errorf("RIB record at offset %d claims %d bytes of %d remaining", off, msglen, len(rib)-off)
		}
		if typ == rtmIfInfo2 && msglen >= ifm2MinLen {
			rec := rib[off : off+msglen]
			idx := int(binary.LittleEndian.Uint16(rec[ifm2IndexOff : ifm2IndexOff+2]))
			out[idx] = ifCountersDarwin{
				rx: binary.LittleEndian.Uint64(rec[ifm2IBytesOff : ifm2IBytesOff+8]),
				tx: binary.LittleEndian.Uint64(rec[ifm2OBytesOff : ifm2OBytesOff+8]),
			}
		}
		off += msglen
	}
	if len(out) == 0 {
		return nil, errors.New("no RTM_IFINFO2 records")
	}
	return out, nil
}

// virtualIfacePrefixesDarwin are the darwin interface-name prefixes
// whose traffic is local plumbing rather than machine throughput --
// the darwin parallel of virtualIfacePrefixes, kept SEPARATE because
// the platforms' names differ (linux tun vs darwin utun, and en* is
// the PHYSICAL family on Macs). Skipped: loopback (lo0), tunnel and
// 6to4 pseudo-interfaces (gif, stf), Apple Wireless Direct Link
// (awdl), low-latency WLAN (llw), VPNs incl. iCloud Private Relay
// (utun), the personal-hotspot access point (ap), Internet Sharing
// bridges (bridge -- member en* traffic would double-count), Apple
// private debug/USB-C interfaces (anpi), packet taps (pktap), fake
// ethernet pairs (feth), and VM networks (vmnet). Kept: en* (Wi-Fi
// IS en0 on Macs; Thunderbolt/USB ethernet are enN) and bond*.
var virtualIfacePrefixesDarwin = []string{
	"lo", "gif", "stf", "awdl", "llw", "utun", "ap", "bridge",
	"anpi", "pktap", "feth", "vmnet",
}

// isVirtualInterfaceDarwin reports whether a darwin interface name is
// local plumbing (see virtualIfacePrefixesDarwin).
func isVirtualInterfaceDarwin(name string) bool {
	for _, p := range virtualIfacePrefixesDarwin {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// netCountersFromIfList2 sums the real-interface byte counters of a
// decoded RIB dump: indexes are named via the index -> name map
// (net.Interfaces -- deliberately no sockaddr_dl parsing), unnamed
// indexes are skipped defensively, and the darwin virtual-interface
// filter drops local plumbing. The parseNetDev parallel.
func netCountersFromIfList2(counters map[int]ifCountersDarwin, names map[int]string) netCounters {
	var c netCounters
	for idx, ic := range counters {
		name, ok := names[idx]
		if !ok || isVirtualInterfaceDarwin(name) {
			continue
		}
		c.rx += ic.rx
		c.tx += ic.tx
	}
	return c
}

// sampleCPUDarwin reads the mach tick counters and updates the busy
// percentage through the shared delta/staleness/wrap machinery
// (updateCPURate -- identical semantics to the linux path).
func (s *Sampler) sampleCPUDarwin(snap *Snapshot, now time.Time) {
	if s.dwn.cpuTicks == nil {
		snap.CPUOK = false
		return
	}
	ticks, err := s.dwn.cpuTicks()
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: cpu: host_statistics: %v", err))
		snap.CPUOK = false
		return
	}
	s.updateCPURate(snap, cpuCountersFromTicks(ticks), now)
}

// sampleMemDarwin fills the point-in-time memory and swap figures:
// hw.memsize + the vm_statistics64 derivation (memFromVMStat, the
// Activity Monitor "Memory Used" definition) for memory, the
// vm.swapusage sysctl for swap. Each side degrades alone, and a swap
// total of zero is the valid no-swap answer (dash), which is exactly
// what macOS's dynamic swap reports while empty.
func (s *Sampler) sampleMemDarwin(snap *Snapshot) {
	switch {
	case s.dwn.memTotal == nil || s.dwn.vmStat == nil:
		snap.MemOK = false
	default:
		total, err := s.dwn.memTotal()
		if err != nil {
			s.logOnce(fmt.Sprintf("stats: mem: hw.memsize: %v", err))
			snap.MemOK = false
			break
		}
		vm, err := s.dwn.vmStat()
		if err != nil {
			s.logOnce(fmt.Sprintf("stats: mem: vm_statistics64: %v", err))
			snap.MemOK = false
			break
		}
		snap.MemTotal = total
		snap.MemUsed = memFromVMStat(vm)
		snap.MemOK = true
	}
	if s.dwn.swapRaw == nil {
		snap.SwapOK = false
		return
	}
	raw, err := s.dwn.swapRaw()
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: swap: vm.swapusage: %v", err))
		snap.SwapOK = false
		return
	}
	total, used, err := decodeXswUsage(raw)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: swap: vm.swapusage: %v", err))
		snap.SwapOK = false
		return
	}
	snap.SwapTotal, snap.SwapUsed, snap.SwapOK = total, used, true
}

// sampleNetDarwin sums the real-interface RIB byte counters and
// updates the throughput rates through the shared machinery
// (updateNetRate -- identical semantics to the linux path).
func (s *Sampler) sampleNetDarwin(snap *Snapshot, now time.Time) {
	if s.dwn.ifRIB == nil || s.dwn.ifNames == nil {
		snap.NetOK = false
		return
	}
	rib, err := s.dwn.ifRIB()
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: net: NET_RT_IFLIST2: %v", err))
		snap.NetOK = false
		return
	}
	names, err := s.dwn.ifNames()
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: net: interfaces: %v", err))
		snap.NetOK = false
		return
	}
	counters, err := decodeIfList2(rib)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: net: NET_RT_IFLIST2: %v", err))
		snap.NetOK = false
		return
	}
	s.updateNetRate(snap, netCountersFromIfList2(counters, names), now)
}
