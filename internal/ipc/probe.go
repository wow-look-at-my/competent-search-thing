package ipc

// The probe half of the self-healing single-instance layer: when
// Listen finds the socket address taken, the code here decides --
// with a bounded liveness probe, never a bare connect -- whether a
// healthy instance really holds it; the takeover half that REPLACES
// unhealthy holders (dead, wedged mid-death, pre-JSON legacy,
// version-skewed) lives in takeover.go. The
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
