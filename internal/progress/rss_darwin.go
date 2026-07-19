//go:build darwin

package progress

import "syscall"

// rssBytes returns the process's CURRENT physical memory footprint
// via mach task_info TASK_VM_INFO phys_footprint (the number Activity
// Monitor shows; footprint_darwin.go). Fallback on any mach failure:
// getrusage ru_maxrss -- the PEAK resident set, in BYTES on darwin
// (unlike linux's kilobytes). The old ru_maxrss-only reading meant
// the startup summary reported the high-water mark forever -- the
// heap sawtooths under GC, the peak of the sawtooth got stamped, and
// no post-build improvement could ever show. The footprint read makes
// the darwin figure current, like linux's /proc/self/statm one.
func rssBytes() uint64 {
	if fp := physFootprintBytes(); fp != 0 {
		return fp
	}
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss)
}
