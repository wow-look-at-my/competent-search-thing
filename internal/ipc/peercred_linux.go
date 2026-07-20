//go:build linux

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCredOf reads the socket peer's pid and uid off a connected unix
// conn via SO_PEERCRED. For a stream socket the kernel reports the
// LISTENER's credentials captured at connect(2) time, so this works
// even for a connection still queued in the backlog of a frozen (or
// mid-death) daemon -- exactly the case the takeover ladder needs.
func peerCredOf(conn net.Conn) (pid, uid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *unix.Ucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil || cred.Pid <= 0 {
		return 0, 0, false
	}
	return int(cred.Pid), int(cred.Uid), true
}

// defaultProcIdent resolves a pid's executable path and comm name from
// /proc -- the linux extra-sanity input for the terminate ladder's
// is-it-really-our-app check (procIdentAt is the untagged, fixture-
// tested reader in takeover.go).
func defaultProcIdent(pid int) (exe, comm string) {
	return procIdentAt("/proc", pid)
}
