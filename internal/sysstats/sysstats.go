// Package sysstats samples lightweight system statistics -- CPU, GPU,
// memory, swap, and network throughput -- for the searchbar's stats
// row. The design constraint is that NOTHING outside the sampler's own
// goroutines ever performs IO: the app reads a mutex-guarded cached
// Snapshot, visibility flips are flag writes plus a non-blocking
// channel send, and sampling happens only while the bar is visible, so
// a hidden app costs zero reads. Sources are probed once, cheaply, at
// construction (no subprocess spawns); every per-metric failure
// degrades that one metric to OK=false and is logged once per distinct
// message, never crashing or blocking the rest.
package sysstats

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Snapshot is one published stats sample. The json tags are the wire
// contract with the frontend (the "stats:update" event payload and the
// GetStats bound-method return). Memory and swap sizes are bytes;
// network rates are bytes per second; percentages are 0..100. Each
// metric's *OK flag reports whether its value is currently live --
// false means "render a dash", covering missing sources (non-Linux,
// no GPU), failed reads, and rates that have not accumulated a
// counter delta yet.
//
// Enabled is NOT the sampler's field to set: the sampler always leaves
// it false, and the app layer sets it true on every snapshot that
// crosses to the frontend from a live sampler (GetStats, the
// "stats:update" relay). Enabled=false therefore means "the feature is
// off" (stats.disabled, no sampler) and the frontend hides the row
// entirely, while Enabled=true with per-metric OK=false renders
// dashes.
type Snapshot struct {
	Enabled   bool    `json:"enabled"`
	CPUPct    float64 `json:"cpuPct"`
	CPUOK     bool    `json:"cpuOk"`
	GPUPct    float64 `json:"gpuPct"`
	GPUOK     bool    `json:"gpuOk"`
	MemUsed   uint64  `json:"memUsed"`
	MemTotal  uint64  `json:"memTotal"`
	MemOK     bool    `json:"memOk"`
	SwapUsed  uint64  `json:"swapUsed"`
	SwapTotal uint64  `json:"swapTotal"`
	SwapOK    bool    `json:"swapOk"`
	NetRxBps  float64 `json:"netRxBps"`
	NetTxBps  float64 `json:"netTxBps"`
	NetOK     bool    `json:"netOk"`
}

// Defaults for the Options knobs.
const (
	// DefaultInterval is the fast-loop cadence while visible.
	DefaultInterval = 1500 * time.Millisecond
	// DefaultGPUInterval is the nvidia-smi slow-loop cadence.
	DefaultGPUInterval = 5 * time.Second
	// DefaultGPUTimeout bounds one nvidia-smi invocation; a hung child
	// is killed when it expires.
	DefaultGPUTimeout = time.Second
)

// maxLoggedMessages caps the once-per-distinct-message dedup map so a
// pathological source that errors with ever-changing text cannot grow
// it without bound; messages beyond the cap are dropped silently.
const maxLoggedMessages = 64

// Options configures a Sampler. Every seam is injectable so tests run
// headlessly over fixture trees; New fills production defaults for
// zero values.
type Options struct {
	// ProcRoot is the procfs mount to read cpu/mem/net data from
	// (default "/proc").
	ProcRoot string
	// SysRoot is the sysfs mount probed for the amdgpu busy file
	// (default "/sys").
	SysRoot string
	// GOOS selects the platform sources (default runtime.GOOS);
	// "linux" and "darwin" have real sources, anything else yields
	// zero sources and a Sampler that only ever serves the zero
	// Snapshot.
	GOOS string
	// Interval is the fast-loop cadence while visible (default 1500ms).
	Interval time.Duration
	// GPUInterval is the nvidia-smi cadence (default 5s); the slow
	// loop exists only when nvidia-smi is the probed GPU source.
	GPUInterval time.Duration
	// GPUTimeout bounds one nvidia-smi run (default 1s).
	GPUTimeout time.Duration
	// LookPath locates nvidia-smi at probe time (default exec.LookPath).
	LookPath func(file string) (string, error)
	// OnUpdate, when non-nil, is called on the sampler goroutine after
	// each published sample while visible (the app relays it as the
	// "stats:update" event). It must be goroutine-safe.
	OnUpdate func(Snapshot)
	// Logf receives the package's log lines (default log.Printf).
	Logf func(format string, args ...any)

	// gpuExec, now, and darwin are test seams: gpuExec runs the
	// probed nvidia-smi binary under ctx and returns its stdout (nil
	// means runNvidiaSMI, the real subprocess); now is the clock (nil
	// means time.Now); darwin injects scripted darwin readers (nil
	// means newDarwinReaders, the real mach/sysctl calls -- which
	// bind nothing on a non-darwin build).
	gpuExec func(ctx context.Context, path string) (string, error)
	now     func() time.Time
	darwin  *darwinReaders
}

// rateState is the fast-loop goroutine's private counter memory for
// the delta-based rates. Only sample() (and the direct-call unit
// tests, which never run the loop) touches it -- deliberately outside
// the mutex.
type rateState struct {
	cpu      cpuCounters
	cpuAt    time.Time
	cpuValid bool
	net      netCounters
	netAt    time.Time
	netValid bool
}

// Sampler owns the stats sources and the cached Snapshot. Build one
// with New, start its goroutines with Start, and drive visibility with
// SetVisible; Snapshot and SetVisible never perform IO.
type Sampler struct {
	opt Options

	// Sources, decided once in New: empty/zero means "absent". On
	// linux the three proc paths are always set; at most one of
	// amdPath / nvidiaPath is set. On darwin dwn holds the reader
	// seam instead and the paths stay empty.
	cpuPath    string
	memPath    string
	netPath    string
	amdPath    string
	nvidiaPath string
	dwn        *darwinReaders

	// kick wakes the fast loop for the immediate summon sample; it is
	// 1-buffered and only ever sent to non-blockingly.
	kick chan struct{}

	mu      sync.Mutex // guards snap, visible, nvidiaPct/nvidiaAt, logged
	snap    Snapshot
	visible bool
	// nvidiaPct/nvidiaAt are the slow goroutine's last successful
	// reading; the fast loop folds them into the snapshot and expires
	// values older than 3*GPUInterval.
	nvidiaPct float64
	nvidiaAt  time.Time
	logged    map[string]bool

	rs rateState
}

// New builds a Sampler and probes its sources once, cheaply: file
// paths and a LookPath only, never a subprocess. It logs one line
// describing the chosen sources. Intel GPUs are deliberately absent --
// there is no cheap sysfs busy-percent file to read.
func New(opt Options) *Sampler {
	if opt.ProcRoot == "" {
		opt.ProcRoot = "/proc"
	}
	if opt.SysRoot == "" {
		opt.SysRoot = "/sys"
	}
	if opt.GOOS == "" {
		opt.GOOS = runtime.GOOS
	}
	if opt.Interval <= 0 {
		opt.Interval = DefaultInterval
	}
	if opt.GPUInterval <= 0 {
		opt.GPUInterval = DefaultGPUInterval
	}
	if opt.GPUTimeout <= 0 {
		opt.GPUTimeout = DefaultGPUTimeout
	}
	if opt.LookPath == nil {
		opt.LookPath = exec.LookPath
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.gpuExec == nil {
		opt.gpuExec = runNvidiaSMI
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	s := &Sampler{
		opt:    opt,
		kick:   make(chan struct{}, 1),
		logged: make(map[string]bool),
	}
	switch opt.GOOS {
	case "linux":
		s.cpuPath = filepath.Join(opt.ProcRoot, "stat")
		s.memPath = filepath.Join(opt.ProcRoot, "meminfo")
		s.netPath = filepath.Join(opt.ProcRoot, "net", "dev")
		gpu := "none"
		if p, ok := probeAMDGPU(opt.SysRoot); ok {
			s.amdPath = p
			gpu = fmt.Sprintf("amdgpu(%s)", amdCardName(p))
		} else if p, err := opt.LookPath("nvidia-smi"); err == nil {
			s.nvidiaPath = p
			gpu = "nvidia-smi"
		}
		opt.Logf("stats: sources: cpu=%s mem=%s net=%s gpu=%s", s.cpuPath, s.memPath, s.netPath, gpu)
	case "darwin":
		dwn := opt.darwin
		if dwn == nil {
			dwn = newDarwinReaders()
		}
		if !dwn.ok() {
			// Only reachable when a non-darwin build asks for GOOS
			// "darwin" (readers_other.go binds nothing): behave like
			// an unknown platform.
			opt.Logf("stats: no sources on this platform; stats row will show placeholders")
			break
		}
		s.dwn = dwn
		// GPU is deliberately absent on darwin for now (the honest
		// dash, GPUOK false): the only spawn-free source is IOKit's
		// IOAccelerator "PerformanceStatistics" registry ("Device
		// Utilization %"), a chunk of IOKit/CoreFoundation cgo whose
		// key names are not API-stable across macOS releases -- the
		// "intel: deliberately absent" precedent. That route is the
		// known future source if the dash ever needs replacing.
		opt.Logf("stats: sources: cpu=host_statistics mem=vm_statistics64+hw.memsize swap=vm.swapusage net=sysctl(iflist2) gpu=none")
	default:
		opt.Logf("stats: no sources on this platform; stats row will show placeholders")
	}
	return s
}

// probeAMDGPU finds the first readable amdgpu busy-percent file under
// sysRoot (filepath.Glob returns sorted matches, so card0 wins over
// card1 when both are readable).
func probeAMDGPU(sysRoot string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(sysRoot, "class", "drm", "card*", "device", "gpu_busy_percent"))
	if err != nil {
		return "", false
	}
	for _, m := range matches {
		if _, err := os.ReadFile(m); err == nil {
			return m, true
		}
	}
	return "", false
}

// amdCardName extracts the "cardN" path component for the source log.
func amdCardName(busyPath string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(busyPath)))
}

// runNvidiaSMI is the production gpuExec seam: one nvidia-smi
// utilization query under ctx. CommandContext kills the child on
// expiry and WaitDelay force-closes its pipes shortly after, so a
// child that ignores the kill (or a grandchild holding stdout) cannot
// wedge the slow loop.
func runNvidiaSMI(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	cmd.WaitDelay = 250 * time.Millisecond
	out, err := cmd.Output()
	return string(out), err
}

// hasSources reports whether New found anything to sample (linux: the
// three proc files are assumed present; darwin: the reader seam is
// bound).
func (s *Sampler) hasSources() bool { return s.cpuPath != "" || s.dwn != nil }

// Start brings the sampler goroutines up: the fast loop (cpu, mem,
// swap, net, and the amdgpu read), plus one slow nvidia-smi loop when
// that is the probed GPU source. With zero sources it logs and starts
// nothing. All goroutines exit when ctx is cancelled. Call it once.
func (s *Sampler) Start(ctx context.Context) {
	if !s.hasSources() {
		s.opt.Logf("stats: nothing to sample; sampler not started")
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go s.runFast(ctx)
	if s.nvidiaPath != "" {
		go s.runNvidia(ctx)
	}
}

// SetVisible tells the sampler whether the bar is on screen. It never
// performs IO: true flips the flag and non-blockingly kicks the fast
// loop (the immediate summon sample plus a one-shot follow-up so the
// rates turn fresh right away); false just flips the flag, and loop
// iterations while hidden do nothing at all -- no reads, no callback.
func (s *Sampler) SetVisible(v bool) {
	s.mu.Lock()
	s.visible = v
	s.mu.Unlock()
	if v {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
}

// Snapshot returns the cached sample instantly (a mutex-guarded copy,
// no IO). Before the first visible sample it is the zero Snapshot:
// every OK flag false.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

func (s *Sampler) visibleNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visible
}

// followUpDelay is the gap between a summon's baseline sample and its
// one-shot follow-up: Interval/5, i.e. ~300ms at the default cadence,
// so the delta-based rates appear almost immediately after the bar
// opens instead of one full Interval later.
func (s *Sampler) followUpDelay() time.Duration { return s.opt.Interval / 5 }

// rateWindow is the maximum age of stored counters still usable as a
// rate baseline. Counters older than this (the bar was hidden; ticks
// sampled nothing) would average the delta over the whole hidden
// span, so they are re-stored instead and the rate refreshes one
// follow-up later.
func (s *Sampler) rateWindow() time.Duration { return 3 * s.opt.Interval }

// runFast is the fast sampling loop: ticker-paced while visible, woken
// immediately by a summon kick, with a one-shot follow-up after each
// kick. Hidden iterations do nothing.
func (s *Sampler) runFast(ctx context.Context) {
	ticker := time.NewTicker(s.opt.Interval)
	defer ticker.Stop()
	var follow *time.Timer
	var followC <-chan time.Time
	defer func() {
		if follow != nil {
			follow.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			if !s.visibleNow() {
				continue
			}
			s.sample()
			if follow != nil {
				follow.Stop()
			}
			follow = time.NewTimer(s.followUpDelay())
			followC = follow.C
		case <-followC:
			followC = nil
			if !s.visibleNow() {
				continue
			}
			s.sample()
		case <-ticker.C:
			if !s.visibleNow() {
				continue
			}
			s.sample()
		}
	}
}

// runNvidia is the slow GPU loop: one nvidia-smi utilization query per
// GPUInterval, visibility-gated like the fast loop, storing the value
// for the fast loop to fold into the snapshot. It is a separate
// goroutine because a subprocess spawn has no business on the fast
// path -- and deliberately has no summon kick: the first reading
// appears up to one GPUInterval after the bar opens.
func (s *Sampler) runNvidia(ctx context.Context) {
	ticker := time.NewTicker(s.opt.GPUInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.visibleNow() {
				continue
			}
			s.sampleNvidia(ctx)
		}
	}
}

// sampleNvidia runs one nvidia-smi query under GPUTimeout and stores
// the parsed value with its timestamp. Failures (timeout, exec error,
// unparseable output) keep the previous value -- the fast loop expires
// it into GPUOK=false once it is 3*GPUInterval old.
func (s *Sampler) sampleNvidia(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, s.opt.GPUTimeout)
	defer cancel()
	out, err := s.opt.gpuExec(cctx, s.nvidiaPath)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: nvidia-smi: %v", err))
		return
	}
	pct, err := parseLeadingInt(out)
	if err != nil {
		s.logOnce(fmt.Sprintf("stats: nvidia-smi: %v", err))
		return
	}
	s.mu.Lock()
	s.nvidiaPct = clampPct(float64(pct))
	s.nvidiaAt = s.opt.now()
	s.mu.Unlock()
}

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
// the amdgpu sysfs file is a ~16-byte read on the fast tick; the
// nvidia value is the slow loop's last reading, expired after
// 3*GPUInterval so a wedged nvidia-smi degrades to a dash instead of a
// frozen number. No source leaves GPUOK false.
func (s *Sampler) sampleGPU(snap *Snapshot, now time.Time) {
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
