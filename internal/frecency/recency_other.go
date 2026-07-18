//go:build !linux

package frecency

import (
	"os"
	"time"
)

// fileRecency falls back to plain mtime off Linux: the syscall-level
// atime fields are per-OS (Atimespec on darwin, FILETIME on windows)
// and mtime alone still covers the just-downloaded cold-start case.
// The windows/amd64 cross-compile in CI builds this file.
func fileRecency(fi os.FileInfo) time.Time {
	return fi.ModTime()
}
