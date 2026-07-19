package progress

import (
	"fmt"
	"runtime"
)

// FormatBytes renders a byte count in decimal units with one decimal
// place: "384.2MB", "12.6GB". Everything below 1e9 is MB (0 ->
// "0.0MB").
func FormatBytes(b uint64) string {
	if b >= 1e9 {
		return fmt.Sprintf("%.1fGB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.1fMB", float64(b)/1e6)
}

// RAM returns the process's memory footprint in bytes: the platform
// resident set size when readable, else the Go runtime's Sys figure
// (RSS unavailable on this platform or failed; Sys is the closest
// whole-process figure the runtime offers).
func RAM() uint64 {
	if r := rssBytes(); r != 0 {
		return r
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Sys
}
