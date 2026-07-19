//go:build darwin

package progress

import "syscall"

// rssBytes returns the PEAK resident set size via getrusage -- on
// darwin ru_maxrss is in BYTES (unlike linux's kilobytes), and darwin
// has no cheap current-RSS read without cgo/task_info. Peak equals
// current for the monotonic-growth indexing phase this feeds. Any
// failure returns 0.
func rssBytes() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return uint64(ru.Maxrss)
}
