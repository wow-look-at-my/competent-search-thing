//go:build !linux && !darwin && !windows

package progress

// rssBytes has no cheap implementation on the remaining platforms; RAM
// falls back to the runtime's Sys figure.
func rssBytes() uint64 { return 0 }
