//go:build linux

package progress

import (
	"os"
	"strconv"
	"strings"
)

// rssBytes reads the resident set size from /proc/self/statm: the
// second whitespace field is the resident page count, in units of the
// system page size. Any read or parse failure returns 0 (RAM falls
// back to runtime figures).
func rssBytes() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
