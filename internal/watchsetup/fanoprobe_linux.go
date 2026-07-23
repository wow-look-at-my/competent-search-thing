package watchsetup

import (
	"errors"

	"golang.org/x/sys/unix"
)

// probeFanotifySupport asks the kernel, cheaply and without side
// effects, whether the fanotify mode internal/watch needs would work.
// It creates the SAME group internal/watch's fanoInit does
// (FAN_CLASS_NOTIF | FAN_REPORT_DFID_NAME, non-blocking) and immediately
// closes it -- no marks, no reads. The init call alone distinguishes the
// three cases the setup cares about:
//
//   - success -> the capabilities are already present (StateReady); the
//     watcher will pick fanotify on its own, no escalation needed.
//   - EPERM   -> the kernel supports it, this binary just lacks
//     CAP_SYS_ADMIN (StateNeedsCaps); a setcap grant enables it.
//   - anything else (EINVAL from a pre-5.9 kernel without
//     FAN_REPORT_DFID_NAME, ENOSYS where fanotify is not built in, ...)
//     -> genuinely unsupported here (StateUnsupported); capabilities
//     cannot help.
func probeFanotifySupport() State {
	fd, err := unix.FanotifyInit(
		unix.FAN_CLASS_NOTIF|unix.FAN_CLOEXEC|unix.FAN_NONBLOCK|unix.FAN_REPORT_DFID_NAME,
		unix.O_RDONLY|unix.O_LARGEFILE|unix.O_CLOEXEC)
	if err == nil {
		_ = unix.Close(fd)
		return StateReady
	}
	if errors.Is(err, unix.EPERM) {
		return StateNeedsCaps
	}
	return StateUnsupported
}
