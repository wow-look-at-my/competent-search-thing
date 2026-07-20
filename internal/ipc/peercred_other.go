//go:build !linux && !darwin

package ipc

import "net"

// peerCredOf has no peer-credential source on this platform; the
// takeover ladder runs its no-pid rung (never signals, socket verdict
// governs).
func peerCredOf(net.Conn) (pid, uid int, ok bool) { return 0, 0, false }

// defaultProcIdent has nothing to read here; empty results make
// sameBinary fail open (unreachable anyway without peer credentials).
func defaultProcIdent(int) (exe, comm string) { return "", "" }
