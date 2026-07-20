//go:build !windows

package ipc

import (
	"os"
	"syscall"
)

// defaultKill is the production signal seam: plain kill(2) on the
// exact pid. NEVER a process group, never a pattern kill -- the
// takeover ladder signals only the peercred-verified socket owner.
func defaultKill(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// flockFile takes (non-blocking) the exclusive takeover lock; the
// kernel releases it on any process death, so it can never go stale
// the way the socket file does.
func flockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// funlockFile releases the takeover lock.
func funlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// identOfPath is the verified-unlink identity read: the socket FILE's
// device+inode at path (lstat -- never fstat of the listener fd,
// which reports the anonymous sockfs inode).
func identOfPath(path string) (dev, ino uint64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
