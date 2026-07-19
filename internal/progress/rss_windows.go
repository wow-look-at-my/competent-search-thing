//go:build windows

package progress

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// golang.org/x/sys/windows (v0.46.0) exports neither GetProcessMemoryInfo
// nor PROCESS_MEMORY_COUNTERS, so we go through the lazy-DLL pattern.
var (
	modpsapi                 = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters mirrors psapi's PROCESS_MEMORY_COUNTERS layout.
// SIZE_T maps to uintptr (4 bytes on 386, 8 on amd64/arm64).
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// rssBytes returns the current working set size, 0 on any failure.
// CurrentProcess yields a pseudo-handle, so no Close is needed. The
// windows binary is cross-compiled but never executed in CI, so this
// path is compile-verified only.
func rssBytes() uint64 {
	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	r1, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(windows.CurrentProcess()),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.cb),
	)
	if r1 == 0 {
		return 0
	}
	return uint64(pmc.WorkingSetSize)
}
