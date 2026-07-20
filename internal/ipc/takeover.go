package ipc

// The takeover half of the self-healing single-instance layer (the
// probe half, ListenOptions and the seams live in probe.go): acting
// on a probe verdict -- the quit handshake, the exact-pid terminate
// ladder, the bounded release wait, the forced bind, and the flock
// that serializes concurrent deciders.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// recoverBusySocket owns the EADDRINUSE branch: serialize deciders on
// the takeover flock, probe the holder, and act on the verdict. The
// only outcome that leaves the holder alone is a healthy instance of
// the SAME version+build (ErrAlreadyRunning, byte-identical to the
// callers' old contract); everything else self-heals so the user
// never has to find, kill, or unlink anything by hand.
func recoverBusySocket(path string, o *listenCfg) (net.Listener, error) {
	o.logf("ipc: socket %s is bound; probing the holder", path)
	unlock, ok := lockTakeover(path+lockSuffix, o)
	if !ok {
		o.logf("ipc: another launcher held the takeover lock for %s past %s; conceding the socket to it", path, lockWait)
		return nil, ErrAlreadyRunning
	}
	defer unlock()
	pr := probeInstance(path, o)
	switch pr.verdict {
	case verdictDead:
		o.logf("ipc: no process holds %s (a crashed instance's leftover file); removing it", path)
		return removeAndBind(path, o, false)
	case verdictHealthy:
		if pr.version == o.version && pr.build == o.build {
			return nil, ErrAlreadyRunning
		}
		o.logf("ipc: running instance is version %s build %q, this binary is %s %q; asking it to quit",
			pr.version, pr.build, o.version, o.build)
		return replaceInstance(path, o, pr, true)
	case verdictLegacy:
		o.logf("ipc: instance behind %s speaks the pre-JSON protocol (reply %q, peer pid %s); new instance wins",
			path, pr.raw, pr.pidLabel())
		return replaceInstance(path, o, pr, false)
	default: // verdictUnresponsive
		o.logf("ipc: no healthy instance behind %s (%s): %s; taking over", path, peerDesc(o, pr), pr.failure)
		return replaceInstance(path, o, pr, false)
	}
}

// peerDesc renders what is known about the socket's owner for the
// takeover log line.
func peerDesc(o *listenCfg, pr probeReport) string {
	if !pr.credOK {
		return "owner pid unknown"
	}
	exe, _ := o.procIdent(pr.pid)
	if exe == "" {
		return fmt.Sprintf("peer pid %d uid %d", pr.pid, pr.uid)
	}
	return fmt.Sprintf("peer pid %d uid %d exe %q", pr.pid, pr.uid, exe)
}

// replaceInstance is the new-instance-wins path: optionally ask a
// healthy-but-skewed holder to quit over IPC, then run the terminate
// ladder -- SIGTERM to the exact peercred-verified pid only, never any
// pattern kill -- wait (bounded) for the socket to be released, and
// bind regardless, logging a survivor loudly.
func replaceInstance(path string, o *listenCfg, pr probeReport, askQuit bool) (net.Listener, error) {
	if askQuit && o.askQuit(path, pr) {
		if elapsed, ok := waitRelease(path, o, pr); ok {
			o.logf("ipc: pid %s released the socket in %s", pr.pidLabel(), elapsed.Round(time.Millisecond))
			return removeAndBind(path, o, true)
		}
		o.logf("ipc: the socket was not released within %s of the accepted quit; escalating to SIGTERM", o.releaseWait)
	}
	// The terminate ladder. Signaling is gated on the peer credentials
	// captured during the probe: same uid, alive, and (linux) an
	// exe/comm that names this app.
	if pr.credOK {
		if pr.uid != o.getuid() {
			o.logf("ipc: peer pid %d is not this app (uid %d, not ours); refusing to signal it", pr.pid, pr.uid)
			return nil, ErrAlreadyRunning
		}
		if err := o.kill(pr.pid, syscall.Signal(0)); err != nil {
			o.logf("ipc: pid %d is already gone (%v); waiting for the socket to clear", pr.pid, err)
		} else {
			exe, comm := o.procIdent(pr.pid)
			if !sameBinary(exe, comm, o.ownBase) {
				o.logf("ipc: peer pid %d is not this app (exe %q, comm %q, want %q); refusing to signal it",
					pr.pid, exe, comm, o.ownBase)
				return nil, ErrAlreadyRunning
			}
			if err := o.kill(pr.pid, syscall.SIGTERM); err != nil {
				o.logf("ipc: SIGTERM to pid %d failed: %v", pr.pid, err)
			} else {
				o.logf("ipc: sent SIGTERM to pid %d (the exact socket owner)", pr.pid)
			}
		}
	} else {
		o.logf("ipc: the socket owner's pid is unknown; nothing to signal (taking over after the release wait)")
	}
	if elapsed, ok := waitRelease(path, o, pr); ok {
		o.logf("ipc: pid %s released the socket in %s", pr.pidLabel(), elapsed.Round(time.Millisecond))
	} else {
		o.logf("ipc: pid %s still holds the socket after %s; forcing takeover (the leftover process may need manual attention)",
			pr.pidLabel(), o.releaseWait)
	}
	return removeAndBind(path, o, true)
}

// askQuit sends the quit command to a healthy-but-skewed holder and
// reports whether it was ACCEPTED (ack-first: the reply arrives before
// the holder's shutdown runs). Old JSON daemons answer unknown-command
// (they predate quit), booting ones not-ready; both fall through to
// the SIGTERM ladder, as does a holder that died mid-exchange.
func (o *listenCfg) askQuit(path string, pr probeReport) bool {
	rep, err := Send(path, CmdQuit, o.probeTimeout)
	switch {
	case err == nil && rep.Parsed && rep.OK:
		o.logf("ipc: the running instance accepted the quit request")
		return true
	case err == nil && rep.Parsed && (rep.UnknownCommand() || rep.NotReady()):
		o.logf("ipc: running instance does not know the quit command (%q); sending SIGTERM to pid %s",
			rep.Err, pr.pidLabel())
	case err != nil:
		o.logf("ipc: quit request failed (%v); falling through to the terminate ladder", err)
	default:
		o.logf("ipc: quit request earned the unexpected reply %q; falling through to the terminate ladder", rep.Raw)
	}
	return false
}

// procIdentAt reads a pid's executable path (readlink exe) and comm
// name from a proc-shaped tree -- the linux terminate ladder's
// identity source, injectable-rooted so it fixture-tests on every OS.
// Empty results mean unreadable, which the identity check treats as
// absent metadata, not a mismatch. (A trimmed local copy of the
// internal/appctx.ProcInfo shape, duplicated deliberately so ipc
// stays free of internal dependencies.)
func procIdentAt(root string, pid int) (exe, comm string) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	if b, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
		comm = strings.TrimSpace(string(b))
	}
	return exe, comm
}

// sameBinary decides whether the peer's /proc identity names this app:
// the exe basename (tolerating the " (deleted)" suffix an
// upgraded-under-it binary shows) or the comm name (the kernel
// truncates comm to 15 bytes, so compare against the truncated own
// base). Empty identity data -- darwin has no /proc at all, and a
// same-uid read failure has nothing to disprove -- fails OPEN: the
// same-uid + liveness checks are the floor, and refusing the takeover
// on absent metadata would strand the user the fix exists for. A
// positive mismatch fails CLOSED (never signal a foreign process).
func sameBinary(peerExe, peerComm, ownBase string) bool {
	if ownBase == "" || (peerExe == "" && peerComm == "") {
		return true
	}
	exe := strings.TrimSuffix(peerExe, " (deleted)")
	if exe != "" && filepath.Base(exe) == ownBase {
		return true
	}
	if peerComm != "" {
		want := ownBase
		if len(want) > 15 {
			want = want[:15]
		}
		if peerComm == want {
			return true
		}
	}
	return false
}

// waitRelease polls (bounded by releaseWait) for the holder to release
// the socket: the file gone (graceful Close unlinks), the pid gone
// (ESRCH), or connects refused (listener destroyed; the file may
// remain). The wait-BEFORE-bind order is load-bearing: a gracefully
// quitting old daemon unlinks the path itself, and binding first would
// hand it OUR socket to unlink.
func waitRelease(path string, o *listenCfg, pr probeReport) (time.Duration, bool) {
	start := time.Now()
	deadline := start.Add(o.releaseWait)
	for {
		if socketReleased(path, o, pr) {
			return time.Since(start), true
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return time.Since(start), false
		}
		step := releasePollInterval
		if remain < step {
			step = remain
		}
		o.sleep(step)
	}
}

// socketReleased is one release-poll check.
func socketReleased(path string, o *listenCfg, pr probeReport) bool {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if pr.credOK && errors.Is(o.kill(pr.pid, syscall.Signal(0)), syscall.ESRCH) {
		return true
	}
	conn, err := o.dial(path, releaseProbeTimeout)
	if err != nil {
		return isDeadDial(err)
	}
	_ = conn.Close()
	return false
}

// removeAndBind clears the leftover socket file (tolerating one that
// a graceful quit already unlinked) and binds fresh. A bind that
// fails EADDRINUSE here means another launcher claimed the path in
// the microseconds after our remove (it came through the cold-start
// path, which never takes the takeover lock); concede to it -- it is
// a fresh instance of this same launch race -- instead of fighting
// over the inode.
func removeAndBind(path string, o *listenCfg, tookOver bool) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			o.logf("ipc: another launcher bound %s during the takeover; conceding to it", path)
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	if tookOver {
		o.logf("ipc: removed %s and took over as the single instance", path)
	}
	return ln, nil
}

// lockTakeover serializes concurrent takeover deciders on a flock
// beside the socket, bounding the two-new-instances race: the winner
// probes/replaces/binds, the loser then probes the winner's fresh
// socket and gets a healthy verdict. The kernel drops the lock on any
// process death, so unlike the socket file it can never go stale.
// ok=false means the lock stayed held past lockWait (the caller
// concedes); an unopenable lock file degrades to an unguarded run
// (logged) rather than blocking the takeover.
func lockTakeover(lockPath string, o *listenCfg) (unlock func(), ok bool) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		o.logf("ipc: takeover lock %s unavailable (%v); proceeding unguarded", lockPath, err)
		return func() {}, true
	}
	deadline := time.Now().Add(lockWait)
	for {
		if err := flockFile(f); err == nil {
			return func() {
				funlockFile(f)
				_ = f.Close()
			}, true
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, false
		}
		o.sleep(lockPollInterval)
	}
}
