package appctx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcInfo reads a process's executable path and command name from a
// /proc-style filesystem: exe is the readlink of <procRoot>/<pid>/exe
// and comm the trimmed content of <procRoot>/<pid>/comm. Each is
// empty when unreadable -- readlink on /proc/<pid>/exe routinely
// fails for other users' processes, which is fine and expected; comm
// usually still works. procRoot is injectable for tests (pass "/proc"
// for the real thing).
func ProcInfo(procRoot string, pid int) (exe string, comm string) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	if target, err := os.Readlink(filepath.Join(base, "exe")); err == nil {
		exe = target
	}
	if b, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		comm = strings.TrimSpace(string(b))
	}
	return exe, comm
}
