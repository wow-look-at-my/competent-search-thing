//go:build darwin

package watch

import (
	"math"

	"golang.org/x/sys/unix"
)

// readFDLimit reads the process's CURRENT soft RLIMIT_NOFILE -- the
// raw input for the darwin auto watch budget; 0 means unreadable.
// Read-only on purpose: the Go runtime already raises the soft limit
// to the hard cap (clamped to kern.maxfilesperproc) in syscall's
// init, before any package code runs, so an app-level Setrlimit could
// not recover headroom -- and issuing one would clobber the runtime's
// saved original limit, which it restores in exec'd children so
// legacy select()-based launchees keep working. See the field
// incident notes in the README's watcher section: the per-directory
// kqueue backend costs one fd per watched dir PLUS one per direct
// child file, so this limit is what the budget must be derived from.
func readFDLimit() int {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0
	}
	if lim.Cur > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(lim.Cur)
}
