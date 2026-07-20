//go:build darwin

package sysstats

// The production darwin readers: thin, IO-only glue behind the
// darwinReaders seam (all decoding and derivation logic lives
// untagged in darwin.go). This is the package's ONE cgo file -- the
// mach host calls and the IOKit registry read have no pure-Go
// spelling -- while the swap and interface-counter readers are
// darwin-only-but-pure syscalls. The GPU reader links the IOKit +
// CoreFoundation frameworks (the LDFLAGS below); everything else is
// libSystem. Real-call tests live in readers_darwin_test.go and run
// headlessly on the mac CI job.

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation

#include <stdlib.h>
#include <mach/mach.h>
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>

// cs_cpu_ticks reads the cumulative all-core CPU scheduler ticks
// (HOST_CPU_LOAD_INFO; each counter is a natural_t, widened to 64
// bits here). Returns 0 on success, the kern_return_t otherwise.
static int
cs_cpu_ticks(unsigned long long *user, unsigned long long *system,
             unsigned long long *idle, unsigned long long *nice)
{
	host_cpu_load_info_data_t info;
	mach_msg_type_number_t count = HOST_CPU_LOAD_INFO_COUNT;
	host_t host = mach_host_self();
	kern_return_t kr = host_statistics(host, HOST_CPU_LOAD_INFO, (host_info_t)&info, &count);
	mach_port_deallocate(mach_task_self(), host);
	if (kr != KERN_SUCCESS) {
		return (int)kr;
	}
	*user = info.cpu_ticks[CPU_STATE_USER];
	*system = info.cpu_ticks[CPU_STATE_SYSTEM];
	*idle = info.cpu_ticks[CPU_STATE_IDLE];
	*nice = info.cpu_ticks[CPU_STATE_NICE];
	return 0;
}

// cs_vm_stat reads the HOST_VM_INFO64 page counts the memory-used
// derivation needs, plus the host page size (16384 on Apple Silicon
// -- never hardcode 4096). Returns 0 on success.
static int
cs_vm_stat(unsigned long long *internal, unsigned long long *purgeable,
           unsigned long long *wired, unsigned long long *compressor,
           unsigned long long *pagesize)
{
	vm_statistics64_data_t vm;
	mach_msg_type_number_t count = HOST_VM_INFO64_COUNT;
	host_t host = mach_host_self();
	kern_return_t kr = host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&vm, &count);
	if (kr == KERN_SUCCESS) {
		vm_size_t ps = 0;
		kr = host_page_size(host, &ps);
		if (kr == KERN_SUCCESS) {
			*internal = vm.internal_page_count;
			*purgeable = vm.purgeable_count;
			*wired = vm.wire_count;
			*compressor = vm.compressor_page_count;
			*pagesize = ps;
		}
	}
	mach_port_deallocate(mach_task_self(), host);
	return kr == KERN_SUCCESS ? 0 : (int)kr;
}

// cs_gpu_perf reads, for every IOAccelerator service in the
// IORegistry, the numeric "PerformanceStatistics" entries named by
// keys[0..nkeys). Output is a row-major matrix: vals[a*nkeys + k] and
// have[a*nkeys + k] for accelerator row a (caller-zeroed; have is set
// to 1 where a numeric value was read). Returns the number of rows
// filled (0 = no IOAccelerator services; services beyond max_accel
// are released and skipped), or -1 when the registry match itself
// failed. Port 0 is the default IOKit port on every macOS version
// (kIOMasterPortDefault and the macOS 12+ kIOMainPortDefault are both
// 0); the literal avoids deprecated-symbol churn across SDKs.
// IOServiceGetMatchingServices consumes the matching dictionary
// reference on success AND failure, so it is never CFReleased here;
// everything created (iterator, service objects, the property dict,
// the key strings) is released.
static int
cs_gpu_perf(char **keys, int nkeys, long long *vals, unsigned char *have, int max_accel)
{
	CFMutableDictionaryRef match = IOServiceMatching("IOAccelerator");
	if (match == NULL) {
		return -1;
	}
	io_iterator_t iter = 0;
	if (IOServiceGetMatchingServices(0, match, &iter) != KERN_SUCCESS) {
		return -1;
	}
	int rows = 0;
	io_object_t svc;
	while ((svc = IOIteratorNext(iter)) != 0) {
		if (rows < max_accel) {
			CFTypeRef props = IORegistryEntryCreateCFProperty(svc, CFSTR("PerformanceStatistics"), kCFAllocatorDefault, 0);
			if (props != NULL) {
				if (CFGetTypeID(props) == CFDictionaryGetTypeID()) {
					CFDictionaryRef dict = (CFDictionaryRef)props;
					int k;
					for (k = 0; k < nkeys; k++) {
						CFStringRef ck = CFStringCreateWithCString(kCFAllocatorDefault, keys[k], kCFStringEncodingUTF8);
						if (ck == NULL) {
							continue;
						}
						CFTypeRef v = CFDictionaryGetValue(dict, ck);
						CFRelease(ck);
						if (v != NULL && CFGetTypeID(v) == CFNumberGetTypeID()) {
							long long num = 0;
							if (CFNumberGetValue((CFNumberRef)v, kCFNumberSInt64Type, &num)) {
								vals[rows*nkeys + k] = num;
								have[rows*nkeys + k] = 1;
							}
						}
					}
				}
				CFRelease(props);
			}
			rows++;
		}
		IOObjectRelease(svc);
	}
	IOObjectRelease(iter);
	return rows;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// newDarwinReaders binds the production darwin sources. Binding is
// pure glue -- no IO happens here, so the probe stays cheap (cheaper
// than linux's amdgpu ReadFile probe); every reader runs only from
// the sampler goroutine's sample paths.
func newDarwinReaders() *darwinReaders {
	return &darwinReaders{
		cpuTicks: readCPUTicks,
		memTotal: readMemTotal,
		vmStat:   readVMStat,
		swapRaw:  readSwapRaw,
		ifRIB:    readIfRIB,
		ifNames:  readIfNames,
		gpuStats: readGPUStats,
	}
}

// readCPUTicks wraps the cs_cpu_ticks mach call.
func readCPUTicks() (cpuTicksDarwin, error) {
	var user, system, idle, nice C.ulonglong
	if rc := C.cs_cpu_ticks(&user, &system, &idle, &nice); rc != 0 {
		return cpuTicksDarwin{}, fmt.Errorf("host_statistics: kern_return %d", int(rc))
	}
	return cpuTicksDarwin{
		user:   uint64(user),
		system: uint64(system),
		idle:   uint64(idle),
		nice:   uint64(nice),
	}, nil
}

// readMemTotal reads the physical memory size in bytes (hw.memsize).
func readMemTotal() (uint64, error) {
	return unix.SysctlUint64("hw.memsize")
}

// readVMStat wraps the cs_vm_stat mach call.
func readVMStat() (vmStat64, error) {
	var internal, purgeable, wired, compressor, pageSize C.ulonglong
	if rc := C.cs_vm_stat(&internal, &purgeable, &wired, &compressor, &pageSize); rc != 0 {
		return vmStat64{}, fmt.Errorf("host_statistics64: kern_return %d", int(rc))
	}
	return vmStat64{
		internalPages: uint64(internal),
		purgeable:     uint64(purgeable),
		wired:         uint64(wired),
		compressor:    uint64(compressor),
		pageSize:      uint64(pageSize),
	}, nil
}

// readSwapRaw reads the raw vm.swapusage sysctl payload.
func readSwapRaw() ([]byte, error) {
	return unix.SysctlRaw("vm.swapusage")
}

// readIfRIB dumps the NET_RT_IFLIST2 routing information base.
// syscall.RouteRIB is marked deprecated in favor of x/net/route, but
// that package exposes NO byte counters on darwin (InterfaceMetrics
// is {Type, MTU} only and InterfaceMessage carries no if_data), so
// the stdlib call is deliberately kept: the if_msghdr2 records it
// returns carry the 64-bit if_data64 counters decodeIfList2 wants.
func readIfRIB() ([]byte, error) {
	//nolint:staticcheck // see the doc comment: x/net/route cannot replace this.
	return syscall.RouteRIB(syscall.NET_RT_IFLIST2, 0)
}

// maxGPUAccelerators bounds the per-sample readout matrix; even a
// multi-GPU Mac Pro tops out well under this.
const maxGPUAccelerators = 8

// readGPUStats reads the gpuUtilKeys entries of every IOAccelerator
// service's "PerformanceStatistics" property: one map per accelerator
// (absent keys omitted, so a service publishing nothing yields an
// empty map), an empty slice when the registry has no accelerator
// services at all. The match + property read is a handful of
// in-process mach calls -- cheap enough for the fast sample loop --
// and deliberately re-matches per read (no cached service handles):
// simplest correct behavior across GPU hotplug, at negligible cost.
func readGPUStats() ([]map[string]int64, error) {
	nkeys := len(gpuUtilKeys)
	ckeys := make([]*C.char, nkeys)
	for i, k := range gpuUtilKeys {
		ckeys[i] = C.CString(k)
	}
	defer func() {
		for _, p := range ckeys {
			C.free(unsafe.Pointer(p))
		}
	}()
	vals := make([]C.longlong, maxGPUAccelerators*nkeys)
	have := make([]C.uchar, maxGPUAccelerators*nkeys)
	rows := int(C.cs_gpu_perf(&ckeys[0], C.int(nkeys), &vals[0], &have[0], C.int(maxGPUAccelerators)))
	if rows < 0 {
		return nil, errors.New("IOAccelerator registry match failed")
	}
	out := make([]map[string]int64, 0, rows)
	for a := 0; a < rows; a++ {
		m := make(map[string]int64, nkeys)
		for k := 0; k < nkeys; k++ {
			if have[a*nkeys+k] != 0 {
				m[gpuUtilKeys[k]] = int64(vals[a*nkeys+k])
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// readIfNames maps interface indexes to names via net.Interfaces
// (pure Go on darwin; one cheap call per sample, the same RIB
// machinery underneath).
func readIfNames() (map[int]string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	names := make(map[int]string, len(ifs))
	for _, ifc := range ifs {
		names[ifc.Index] = ifc.Name
	}
	return names, nil
}
