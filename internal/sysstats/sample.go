package sysstats

// The fast-path sampling half of the Sampler: one full sample per
// tick/kick, the per-metric sample funcs shared by the linux and
// darwin sources, and the rate/clamp/log-once helpers. Split from
// sysstats.go, which keeps the types, source probing, and the loops.

import (
	"fmt"
	"os"
	"time"
)

// sample takes one full fast-path sample and publishes it: point-in-
// time metrics (mem, swap, amdgpu busy) read fresh, delta-based rates
// (cpu, net) computed only when the stored counters are recent enough
// (rateWindow), otherwise the counters are re-stored and the previous
// rate VALUES stay in the snapshot rather than blanking -- the
// follow-up or next tick refreshes them. Any per-metric failure sets
// that metric's OK=false and is logged once; the others are
// unaffected. OnUpdate fires after the snapshot is stored.
func (s *Sampler) sample() {
	now := s.opt.now()
	s.mu.Lock()
	snap := s.snap
	s.mu.Unlock()

	s.sampleCPU(&snap, now)
	s.sampleMem(&snap)
	s.sampleNet(&snap, now)
	s.sampleGPU(&snap, now)

	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
	if s.opt.OnUpdate != nil {
		s.opt.OnUpdate(snap)
	}
}

// sampleCPU updates the busy percentage from the platform counter
// delta: /proc/stat on linux, the mach tick counters on darwin. Zero
// or negative deltas (a clock hiccup or counter wrap) skip the
// update, keeping the previous value and OK flag.
func (s *Sampler) sampleCPU(snap *Snapshot, now time.Time) {
	if s.dwn != nil {
		s.sampleCPUDarwin(snap, now)
		return
	}
	data, err := os.ReadFile(s.cpuPath)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: cpu: %v", err))
		snap.CPUOK = false
		return
	}
	cur, err := parseCPUStat(data)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: cpu: %s: %v", s.cpuPath, err))
		snap.CPUOK = false
		return
	}
	s.updateCPURate(snap, cur, now)
}

// updateCPURate feeds one counter reading into the rate state shared
// by the linux and darwin CPU paths: the new baseline is stored, and
// only when the previous one is recent enough (rateWindow) does the
// delta reach the snapshot -- zero/negative deltas (wrap) skip the
// update via cpuRate's ok=false.
func (s *Sampler) updateCPURate(snap *Snapshot, cur cpuCounters, now time.Time) {
	prev, prevAt, valid := s.rs.cpu, s.rs.cpuAt, s.rs.cpuValid
	s.rs.cpu, s.rs.cpuAt, s.rs.cpuValid = cur, now, true
	if !valid || now.Sub(prevAt) > s.rateWindow() {
		return // fresh baseline stored; the previous rate value stands
	}
	if pct, ok := cpuRate(prev, cur); ok {
		snap.CPUPct, snap.CPUOK = pct, true
	}
}

// sampleMem fills the point-in-time memory and swap figures:
// /proc/meminfo on linux, the mach/sysctl readers on darwin. A swap
// total of zero is a valid answer (no swap configured; SwapOK stays
// true and the frontend renders it as the live "0M" value -- only
// SwapOK=false earns the dash), while a missing MemAvailable line
// means the used figure cannot be computed.
func (s *Sampler) sampleMem(snap *Snapshot) {
	if s.dwn != nil {
		s.sampleMemDarwin(snap)
		return
	}
	data, err := os.ReadFile(s.memPath)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: mem: %v", err))
		snap.MemOK = false
		snap.SwapOK = false
		return
	}
	mi := parseMemInfo(data)
	if mi.haveTotal && mi.haveAvail {
		snap.MemTotal = mi.memTotal
		snap.MemUsed = 0
		if mi.memTotal > mi.memAvail {
			snap.MemUsed = mi.memTotal - mi.memAvail
		}
		snap.MemOK = true
	} else {
		s.logOnce(fmt.Sprintf("stats: mem: %s is missing MemTotal/MemAvailable", s.memPath))
		snap.MemOK = false
	}
	if mi.haveSwapTotal && mi.haveSwapFree {
		snap.SwapTotal = mi.swapTotal
		snap.SwapUsed = 0
		if mi.swapTotal > mi.swapFree {
			snap.SwapUsed = mi.swapTotal - mi.swapFree
		}
		snap.SwapOK = true
	} else {
		s.logOnce(fmt.Sprintf("stats: mem: %s is missing SwapTotal/SwapFree", s.memPath))
		snap.SwapOK = false
	}
}

// sampleNet updates the summed real-interface throughput from the
// platform counter deltas -- /proc/net/dev on linux, the
// NET_RT_IFLIST2 RIB on darwin -- with the same staleness and wrap
// rules as the CPU rate.
func (s *Sampler) sampleNet(snap *Snapshot, now time.Time) {
	if s.dwn != nil {
		s.sampleNetDarwin(snap, now)
		return
	}
	data, err := os.ReadFile(s.netPath)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: net: %v", err))
		snap.NetOK = false
		return
	}
	cur, err := parseNetDev(data)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: net: %s: %v", s.netPath, err))
		snap.NetOK = false
		return
	}
	s.updateNetRate(snap, cur, now)
}

// updateNetRate is updateCPURate's network twin: shared by the linux
// and darwin paths, negative deltas (wrap, a vanished interface) skip
// the update via netRate's ok=false.
func (s *Sampler) updateNetRate(snap *Snapshot, cur netCounters, now time.Time) {
	prev, prevAt, valid := s.rs.net, s.rs.netAt, s.rs.netValid
	s.rs.net, s.rs.netAt, s.rs.netValid = cur, now, true
	if !valid || now.Sub(prevAt) > s.rateWindow() {
		return
	}
	if rx, tx, ok := netRate(prev, cur, now.Sub(prevAt)); ok {
		snap.NetRxBps, snap.NetTxBps, snap.NetOK = rx, tx, true
	}
}

// sampleGPU fills the GPU percentage from whichever source New probed:
// darwin reads the IOAccelerator registry on the fast tick (an
// in-process IOKit call, the amdgpu-read analogue); the amdgpu sysfs
// file is a ~16-byte read on the fast tick; the nvidia value is the
// slow loop's last reading, expired after 3*GPUInterval so a wedged
// nvidia-smi degrades to a dash instead of a frozen number. No source
// leaves GPUOK false.
func (s *Sampler) sampleGPU(snap *Snapshot, now time.Time) {
	if s.dwn != nil {
		s.sampleGPUDarwin(snap)
		return
	}
	switch {
	case s.amdPath != "":
		data, err := os.ReadFile(s.amdPath)
		if err != nil {
			s.logOnce(fmt.Sprintf("stats: gpu: %v", err))
			snap.GPUOK = false
			return
		}
		pct, err := parseLeadingInt(string(data))
		if err != nil {
			s.logOnce(fmt.Sprintf("stats: gpu: %s: %v", s.amdPath, err))
			snap.GPUOK = false
			return
		}
		snap.GPUPct, snap.GPUOK = clampPct(float64(pct)), true
	case s.nvidiaPath != "":
		s.mu.Lock()
		v, at := s.nvidiaPct, s.nvidiaAt
		s.mu.Unlock()
		if at.IsZero() || now.Sub(at) > 3*s.opt.GPUInterval {
			snap.GPUOK = false
			return
		}
		snap.GPUPct, snap.GPUOK = v, true
	}
}

// logOnce logs msg the first time it is seen (bounded by
// maxLoggedMessages), so a persistently broken source is one log line,
// not one per sample.
func (s *Sampler) logOnce(msg string) {
	s.mu.Lock()
	if s.logged[msg] || len(s.logged) >= maxLoggedMessages {
		s.mu.Unlock()
		return
	}
	s.logged[msg] = true
	s.mu.Unlock()
	s.opt.Logf("%s", msg)
}

// clampPct clamps v into 0..100.
func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
