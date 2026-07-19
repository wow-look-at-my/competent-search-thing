//go:build windows

package progress

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// rssBytes returns the current working set size. CurrentProcess yields
// a pseudo-handle, so no Close is needed. Any failure returns 0.
func rssBytes() uint64 {
	var pmc windows.PROCESS_MEMORY_COUNTERS
	err := windows.GetProcessMemoryInfo(windows.CurrentProcess(), &pmc, uint32(unsafe.Sizeof(pmc)))
	if err != nil {
		return 0
	}
	return uint64(pmc.WorkingSetSize)
}
