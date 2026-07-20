//go:build darwin

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCredOf reads the socket peer's pid and uid off a connected unix
// conn: LOCAL_PEERPID for the pid, LOCAL_PEERCRED for the uid. Like
// linux's SO_PEERCRED these report the LISTENER's identity captured
// at connect time, so a backlog-queued connection to a frozen daemon
// still yields its credentials.
func peerCredOf(conn net.Conn) (pid, uid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var (
		p          int
		cred       *unix.Xucred
		perr, xerr error
	)
	if cerr := raw.Control(func(fd uintptr) {
		p, perr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		cred, xerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); cerr != nil || perr != nil || xerr != nil || cred == nil || p <= 0 {
		return 0, 0, false
	}
	return p, int(cred.Uid), true
}

// defaultProcIdent has no /proc to read on darwin: the terminate
// ladder's identity floor there is same-uid (LOCAL_PEERCRED) plus pid
// liveness, per the design. Empty results make sameBinary fail open.
func defaultProcIdent(int) (exe, comm string) { return "", "" }
