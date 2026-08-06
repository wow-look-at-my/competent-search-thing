# internal/sysstats

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/sysstats` -- the system-stats sampler behind the
frontend's stats row, pure and headless-tested (fixture proc/sys
trees, injectable clock + gpuExec seams). `Snapshot` is the wire
contract (json tags enabled/cpuPct/cpuOk/gpuPct/gpuOk/memUsed/
memTotal/memOk/swapUsed/swapTotal/swapOk/netRxBps/netTxBps/netOk;
bytes for mem/swap, bytes/sec for net, 0..100 pcts; *Ok=false =
"render a dash"; Enabled is the APP layer's field -- the sampler
always leaves it false, internal/app stamps it true on every
GetStats return and emitStats payload, so Enabled false = feature
off = the frontend hides the row). `New(Options{ProcRoot "/proc",
SysRoot "/sys", GOOS
runtime.GOOS, Interval 1500ms, GPUInterval 5s, GPUTimeout 1s,
LookPath exec.LookPath, OnUpdate, Logf; unexported test seams
gpuExec + now + darwin})` probes sources ONCE, cheaply (no
subprocess spawns), by a GOOS switch: windows/unknown = zero
sources + one "placeholders" log;
linux = the three proc files assumed present, GPU = first readable
glob hit of SysRoot/class/drm/card*/device/gpu_busy_percent
(amdgpu) else LookPath("nvidia-smi") else none (intel: deliberately
absent, no cheap sysfs busy%), all summarized in ONE "stats:
sources: cpu=... gpu=..." line; darwin = the darwinReaders seam
(Options.darwin, else newDarwinReaders -- binding only, zero IO at
probe time) + its own accurate source line ("cpu=host_statistics
mem=vm_statistics64+hw.memsize swap=vm.swapusage net=sysctl(iflist2)
gpu=ioaccelerator" -- gpu=none when the gpu reader member is nil),
while a readers value with nothing bound (the !darwin
stub) degrades to the placeholders path. DARWIN LAYOUT (the
fixture tests must keep running on BOTH CI jobs, so the split is
VALUE-driven, build tags confined to the thin readers): darwin.go
is UNTAGGED pure logic -- the darwinReaders struct (cpuTicks/
memTotal/vmStat/swapRaw/ifRIB/ifNames/gpuStats; nil member = that
metric
degrades alone), cpuCountersFromTicks (busy=user+system+nice,
total=busy+idle; uint32 tick wraps take the linux skip-one-update
path), memFromVMStat = Activity Monitor's "Memory Used"
((internal - purgeable clamped at 0) + wired + compressor pages *
pageSize -- NOT total-free, which macOS's tiny free_count makes
meaningless), decodeXswUsage (vm.swapusage xsw_usage: LE uint64s
at 0/8/16, min len 24; total 0 = the valid empty-dynamic-swap
zero, rendered 0M), decodeIfList2 (bounds-checked NET_RT_IFLIST2 walker, the
fanotify_parse pattern: 4-byte prologue, advance by ifm_msglen,
RTM_IFINFO2=0x12 records read ifm_index@12 + if_data64 64-bit
ibytes/obytes@96/104; malformed lengths error, zero usable records
error -- never a silent zero), the SEPARATE darwin iface filter
(skip lo/gif/stf/awdl/llw/utun/ap/bridge/anpi/pktap/feth/vmnet;
keep en* -- Wi-Fi IS en0 on Macs -- and bond*), the IOAccelerator
utilization selection (gpuUtilKeys "Device Utilization %" preferred
then "Renderer Utilization %" per accelerator -- the two
widely-attested PerformanceStatistics keys -- and gpuPctFromStats =
busiest accelerator, clamped 0..100, ok=false when nothing
published), and the sampleCPUDarwin/sampleMemDarwin/
sampleNetDarwin/sampleGPUDarwin bodies;
readers_darwin.go (the package's ONE cgo file, darwin-only) binds
host_statistics/host_statistics64+host_page_size (mach ports
deallocated per call) + unix.SysctlUint64("hw.memsize") +
unix.SysctlRaw("vm.swapusage") + syscall.RouteRIB(NET_RT_IFLIST2)
(deprecated-but-kept: x/net/route exposes NO darwin byte counters
-- verified) + net.Interfaces + the IOAccelerator
PerformanceStatistics reader (cs_gpu_perf: IOKit port 0 = the
default port on every macOS version, the matching dict consumed by
IOServiceGetMatchingServices so it is never CFReleased, every
created object released, re-matched per read -- no cached service
handles; `-framework IOKit -framework CoreFoundation` LDFLAGS, the
package's only framework link); readers_other.go (!darwin) binds
nothing. sampleCPU/sampleMem/sampleNet/sampleGPU dispatch on
s.dwn != nil
and share the extracted updateCPURate/updateNetRate with linux
(byte-identical linux behavior); sampleGPUDarwin rides the fast
loop like the linux amdgpu sysfs read (an in-process registry
call, no subprocess -- the nvidia slow-goroutine pattern is
deliberately not used), and a nil gpu reader is the silent dash
while a failed read or a registry publishing no utilization key
(VM paravirtual GPUs) = GPUOK false + one log line = the honest
dash. THE invariant: nothing outside the
sampler goroutines ever does IO -- Snapshot() is a mutex-guarded
copy, SetVisible(v) is a flag flip (+ non-blocking 1-buffered kick
send on true), and while hidden the loops sample NOTHING, so the
start-hidden app reads zero bytes until first summon. Start(ctx):
zero sources = log + return, else ONE fast goroutine (select ctx /
kick / Interval ticker / one-shot follow-up timer) + ONE slow
nvidia goroutine only for the nvidia source (GPUInterval ticker,
exec via gpuExec seam under a GPUTimeout CommandContext with 250ms
WaitDelay -- a hung nvidia-smi is killed -- parse leading int,
store value+timestamp; the fast loop folds it in and expires it to
GPUOK=false past 3*GPUInterval; no summon kick here by design, an
exec has no business on the summon path). A kick = immediate
baseline sample (point-in-time mem/swap/amdgpu/ioaccelerator
published; previous
RATE values kept, never blanked) then a one-shot follow-up at
Interval/5 (~300ms) so cpu/net rates turn fresh right away. Rates
(cpu pct, net Bps) come from counter deltas ONLY when the stored
counters are <= 3*Interval old (rateWindow) -- older = re-store +
keep previous values -- and negative/zero deltas (wrap) skip the
update; cpu busy = total - idle - iowait over the first 8 "cpu "
aggregate fields (guest/guest_nice excluded, already inside
user/nice), pct clamped 0..100; mem used = MemTotal - MemAvailable
(missing MemAvailable = MemOK false; kB * 1024 = bytes); swap =
SwapTotal/SwapFree, total 0 valid (SwapOK true, rendered 0M); net =
sum of
rx/tx bytes over real interfaces -- "lo" exact plus the virtual
prefixes veth/docker/br-/virbr/vnet/tap/tun/wg/zt/dummy/ifb/kube/
cni/flannel/cali skipped, eth/en/wl/ww/bond kept. Per-metric
failures = that metric OK=false + one log per distinct message
(bounded map, 64), everything else unaffected; OnUpdate fires on
the sampler goroutine after each published sample while visible
(nil tolerated). Exhaustively table-tested (parsers incl. malformed
input + wrap + the iface filter, probe variants, direct-sample rate
math on a fake clock, full lifecycle over a real loop, nvidia fake
incl. ctx-deadline + real /bin/sh subprocess kill) plus
BenchmarkSample: one full fast-path sample against the real /proc
(skips where unreadable). darwin_test.go is UNTAGGED (synthetic
xsw_usage/RIB buffers, scripted fakeDarwin readers with call
counts, GOOS "darwin" + injected seam lifecycle incl. the
hidden-reads-nothing proof -- runs on linux CI AND the mac job;
the nil-seam stub expectation is runtime.GOOS-gated);
readers_darwin_test.go (darwin-only) real-calls every production
reader on the mac runner (tick monotonicity, memsize/vm_stat
sanity, real-RIB decode against the documented offsets, the swap
pipeline gate -- decode + sampleMem SwapOK=true whatever the total,
the SWP field-report regression pin -- the GPU reader's clean
semantics -- the registry match never errors, 0..100 when a
utilization key is published, graceful ok=false on VM runners
whose paravirtual GPU publishes nothing -- and a two-real-samples
end-to-end with live cpu/net + the GPU either live in 0..100 or
the logged honest dash).
Consumed by internal/app's stats.go.
