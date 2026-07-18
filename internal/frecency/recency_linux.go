//go:build linux

package frecency

import (
	"os"
	"syscall"
	"time"
)

// fileRecency is max(atime, mtime) on Linux: mtime catches
// just-downloaded/just-written files, atime (coarse under relatime,
// see the package doc) catches recently-read ones. A FileInfo whose
// Sys is not a *syscall.Stat_t (test fakes) falls back to mtime.
func fileRecency(fi os.FileInfo) time.Time {
	mtime := fi.ModTime()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if atime := time.Unix(st.Atim.Unix()); atime.After(mtime) {
			return atime
		}
	}
	return mtime
}
