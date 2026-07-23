package watchsetup

import "syscall"

// prodReExec replaces the current process image with a fresh exec of
// exe (the resolved, now-capable binary), preserving argv and passing
// env. execve applies the binary's file capabilities, so the new process
// comes up with fanotify available. It only returns on failure -- on
// success there is no "after".
//
// The single-instance socket fd closes on exec (Go sets O_CLOEXEC),
// leaving a dead socket file the child's ipc.Listen self-heal recovers;
// any WebKit helper processes exit when their parent connection drops.
func prodReExec(exe string, argv, env []string) error {
	return syscall.Exec(exe, argv, env)
}
