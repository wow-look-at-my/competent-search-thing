//go:build windows

package ipc

import (
	"errors"
	"os"
	"syscall"
)

// defaultKill: no unix signals on windows; unreachable in practice
// because peerCredOf never yields a pid there, so the ladder runs its
// no-pid rung.
func defaultKill(int, syscall.Signal) error {
	return errors.New("process signals are not supported on windows")
}

// flockFile / funlockFile: no flock(2) on windows -- the takeover
// critical section runs unguarded there (windows/amd64 only ever
// cross-compiles in CI; the race bound is documented best-effort).
func flockFile(*os.File) error { return nil }
func funlockFile(*os.File)     {}

// identOfPath: no unix stat identity on windows; Close keeps Go's
// default unlink-on-close.
func identOfPath(string) (dev, ino uint64, ok bool) { return 0, 0, false }
