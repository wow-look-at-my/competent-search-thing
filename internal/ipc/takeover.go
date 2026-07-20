package ipc

// The self-healing half of the single-instance layer: when Listen
// finds the socket address taken, the code here decides -- with a
// bounded liveness probe, never a bare connect -- whether a healthy
// instance really holds it, and REPLACES the holder in every other
// case (dead, wedged mid-death, pre-JSON legacy, version-skewed). The
// lesson this file encodes: a unix connect(2) succeeds as soon as the
// kernel queues the connection in the listen backlog, BEFORE any
// userspace accept, so connect success is NOT liveness -- a daemon
// frozen in a crash traceback accepts connects for seconds and then
// resets every queued connection when it dies (the field incident:
// "read: connection reset by peer" from a client that raced the
// daemon's death). Only a reply round-trip proves a live instance.
//
// Everything here logs, unconditionally, in the repo's inline-metric
// style ("ipc: ..." lines); production wires log.Printf, tests record.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// probeAttempts is how many ping/version round-trips the occupied-
	// socket probe makes before declaring the holder unresponsive. A
	// daemon mid-death usually finishes dying between attempts, so a
	// later attempt sees connect-refused and classifies it dead
	// instead.
	probeAttempts = 3
	// defaultProbeTimeout bounds each probe attempt (the dial and the
	// reply read separately). A healthy daemon answers ping/version in
	// single-digit milliseconds even during startup indexing -- the
	// ack path never touches the app -- so 500ms is >50x headroom.
	defaultProbeTimeout = 500 * time.Millisecond
	// defaultProbeGap spaces the probe attempts.
	defaultProbeGap = 250 * time.Millisecond
	// defaultReleaseWait bounds how long the takeover waits for a
	// quitting (or SIGTERMed) holder to release the socket before
	// forcing the takeover regardless.
	defaultReleaseWait = 2 * time.Second
	// releasePollInterval is the release-wait poll cadence.
	releasePollInterval = 100 * time.Millisecond
	// releaseProbeTimeout bounds each release-poll dial.
	releaseProbeTimeout = 100 * time.Millisecond
	// lockSuffix names the flock file beside the socket that
	// serializes concurrent takeover deciders.
	lockSuffix = ".lock"
	// lockWait caps how long a decider waits for the takeover lock
	// before conceding the socket to whoever holds it.
	lockWait = 3 * time.Second
	// lockPollInterval is the non-blocking flock retry cadence.
	lockPollInterval = 50 * time.Millisecond
)

// ListenOptions tunes ListenWith's occupied-socket probe and takeover
// engine. The zero value is production behavior. The exported fields
// double as test seams: unit tests MUST set Kill to a recorder --
// in-process fake daemons report the TEST's own pid as the socket
// owner, so a real kill(2) would signal the test process itself.
type ListenOptions struct {
	// Logf receives the probe/takeover log lines; nil means the
	// standard logger (everything logs, unconditionally).
	Logf func(format string, args ...any)
	// Build is this binary's build identity for version-skew
	// detection; empty derives it from the build info (OwnBuild).
	Build string
	// Kill delivers a signal to the exact pid (sig 0 = liveness
	// probe). nil = kill(2). Tests record instead of signaling.
	Kill func(pid int, sig syscall.Signal) error
	// ProbeTimeout, ProbeGap and ReleaseWait override the takeover
	// timing constants (0 = the production defaults above); exported
	// so the cli tests can keep the wedged-peer matrix fast.
	ProbeTimeout time.Duration
	ProbeGap     time.Duration
	ReleaseWait  time.Duration

	// Unexported seams for this package's own tests.
	peerCred  func(conn net.Conn) (pid, uid int, ok bool)
	procIdent func(pid int) (exe, comm string)
	getuid    func() int
	ownBase   string
	dial      func(path string, timeout time.Duration) (net.Conn, error)
	sleep     func(d time.Duration)
}

// listenCfg is ListenOptions with every default resolved.
type listenCfg struct {
	version, build string
	logf           func(format string, args ...any)
	kill           func(pid int, sig syscall.Signal) error
	peerCred       func(conn net.Conn) (pid, uid int, ok bool)
	procIdent      func(pid int) (exe, comm string)
	getuid         func() int
	ownBase        string
	dial           func(path string, timeout time.Duration) (net.Conn, error)
	sleep          func(d time.Duration)
	probeTimeout   time.Duration
	probeGap       time.Duration
	releaseWait    time.Duration
}

func (o ListenOptions) resolve(version string) *listenCfg {
	c := &listenCfg{
		version:      version,
		build:        o.Build,
		logf:         o.Logf,
		kill:         o.Kill,
		peerCred:     o.peerCred,
		procIdent:    o.procIdent,
		getuid:       o.getuid,
		ownBase:      o.ownBase,
		dial:         o.dial,
		sleep:        o.sleep,
		probeTimeout: o.ProbeTimeout,
		probeGap:     o.ProbeGap,
		releaseWait:  o.ReleaseWait,
	}
	if c.build == "" {
		c.build = OwnBuild()
	}
	if c.logf == nil {
		c.logf = log.Printf
	}
	if c.kill == nil {
		c.kill = defaultKill
	}
	if c.peerCred == nil {
		c.peerCred = peerCredOf
	}
	if c.procIdent == nil {
		c.procIdent = defaultProcIdent
	}
	if c.getuid == nil {
		c.getuid = os.Getuid
	}
	if c.ownBase == "" {
		c.ownBase = ownExeBase()
	}
	if c.dial == nil {
		c.dial = func(path string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", path, timeout)
		}
	}
	if c.sleep == nil {
		c.sleep = time.Sleep
	}
	if c.probeTimeout <= 0 {
		c.probeTimeout = defaultProbeTimeout
	}
	if c.probeGap <= 0 {
		c.probeGap = defaultProbeGap
	}
	if c.releaseWait <= 0 {
		c.releaseWait = defaultReleaseWait
	}
	return c
}

// ownBuild memoizes the binary's vcs-revision stamp.
var (
	ownBuildOnce sync.Once
	ownBuildVal  string
)

// OwnBuild returns this binary's build identity for version-skew
// detection: the leading 12 hex characters of the VCS revision the Go
// toolchain stamped into the build info, or "" for builds without one
// (dev builds outside a git checkout). The app's Version constant
// never changes across releases, so this stamp is the discriminator
// behind "new instance wins" -- two unstamped dev builds compare
// equal on purpose (no takeover churn in go-run workflows).
func OwnBuild() string {
	ownBuildOnce.Do(func() {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		for _, s := range bi.Settings {
			if s.Key != "vcs.revision" {
				continue
			}
			ownBuildVal = s.Value
			if len(ownBuildVal) > 12 {
				ownBuildVal = ownBuildVal[:12]
			}
			return
		}
	})
	return ownBuildVal
}

// ownExeBase is the running binary's base name, the reference for the
// peer-identity sanity check.
func ownExeBase() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return filepath.Base(exe)
	}
	if len(os.Args) > 0 && os.Args[0] != "" {
		return filepath.Base(os.Args[0])
	}
	return ""
}

// probeVerdict classifies what holds the occupied socket.
type probeVerdict int

const (
	// verdictHealthy: a parsed JSON reply arrived -- a live JSON
	// daemon (version+build decide same-instance vs skew).
	verdictHealthy probeVerdict = iota
	// verdictDead: connect refused -- nothing holds the socket, only
	// the leftover file (today's stale-file recovery).
	verdictDead
	// verdictLegacy: a raw non-JSON reply line -- a live pre-JSON v1
	// daemon (new instance wins; it has no quit command).
	verdictLegacy
	// verdictUnresponsive: every attempt ended in reset/EOF/timeout --
	// a wedged or mid-death holder that will never answer.
	verdictUnresponsive
)

// probeReport carries the probe's verdict plus everything the takeover
// ladder needs: the peer credentials captured off the connected socket
// (SO_PEERCRED reports the LISTENER's credentials even for a
// backlog-queued connection to a frozen daemon), the healthy holder's
// version+build, a legacy holder's raw reply, and the last failure for
// the logs.
type probeReport struct {
	verdict  probeVerdict
	pid, uid int
	credOK   bool
	version  string
	build    string
	raw      string
	failure  string
}

// pidLabel renders the peer pid for log lines ("unknown" when the
// credentials could not be read).
func (pr probeReport) pidLabel() string {
	if pr.credOK {
		return strconv.Itoa(pr.pid)
	}
	return "unknown"
}

// probeInstance runs the bounded liveness probe against the occupied
// socket: up to probeAttempts version round-trips (version, not ping,
// so a healthy answer carries the skew discriminator in the same
// round-trip). Connect refused short-circuits to dead; a JSON or raw
// reply short-circuits to healthy/legacy; only all-attempts-failed
// (reset, EOF, timeout) is unresponsive.
func probeInstance(path string, o *listenCfg) probeReport {
	pr := probeReport{failure: "no probe attempt completed"}
	for i := 1; i <= probeAttempts; i++ {
		if i > 1 {
			o.sleep(o.probeGap)
		}
		start := time.Now()
		conn, err := o.dial(path, o.probeTimeout)
		if err != nil {
			if isDeadDial(err) {
				o.logf("ipc: probe %d/%d: nothing holds the socket (%v)", i, probeAttempts, err)
				pr.verdict = verdictDead
				return pr
			}
			pr.failure = err.Error()
			o.logf("ipc: probe %d/%d: %v", i, probeAttempts, err)
			continue
		}
		if pid, uid, ok := o.peerCred(conn); ok {
			pr.pid, pr.uid, pr.credOK = pid, uid, true
		}
		line, err := probeExchange(conn, time.Now().Add(o.probeTimeout))
		_ = conn.Close()
		if err != nil {
			pr.failure = describeFailure(err, o.probeTimeout)
			o.logf("ipc: probe %d/%d: %s", i, probeAttempts, pr.failure)
			continue
		}
		rep := parseReply(line)
		if !rep.Parsed {
			o.logf("ipc: probe %d/%d: non-JSON reply %q", i, probeAttempts, line)
			pr.verdict = verdictLegacy
			pr.raw = line
			return pr
		}
		o.logf("ipc: probe %d/%d: answered in %s (version %q, build %q)",
			i, probeAttempts, time.Since(start).Round(time.Millisecond), rep.Version, rep.Build)
		pr.verdict = verdictHealthy
		pr.version, pr.build = rep.Version, rep.Build
		return pr
	}
	pr.verdict = verdictUnresponsive
	return pr
}

// probeExchange writes one version request on conn and reads one reply
// line under deadline.
func probeExchange(conn net.Conn, deadline time.Time) (string, error) {
	_ = conn.SetDeadline(deadline)
	req, err := json.Marshal(Request{Cmd: CmdVersion})
	if err != nil {
		return "", err // unreachable: a plain string field always marshals
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxLine)).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// isDeadDial reports whether a dial error means nothing holds the
// socket (refused = leftover file with no listener; not-exist = the
// file vanished mid-probe).
func isDeadDial(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, fs.ErrNotExist)
}

// describeFailure renders one probe-attempt failure for the logs.
func describeFailure(err error, budget time.Duration) string {
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		return "read: connection reset by peer (the holder died with the request in flight)"
	case errors.Is(err, io.EOF):
		return "EOF before a reply"
	case errors.Is(err, syscall.EPIPE):
		return "write: broken pipe (the holder died before the request)"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("timeout after %s (connected, nothing answered)", budget)
	}
	return err.Error()
}

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
