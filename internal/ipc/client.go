package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// ErrNotRunning wraps the dial failure Send hits when no instance is
// listening on the socket; branch on it with IsNotRunning.
var ErrNotRunning = errors.New("no running instance")

// Reply is Send's parsed result. All parsing lives here so callers
// branch on fields, never on wire strings.
type Reply struct {
	OK       bool   // the request was accepted (or answered)
	Accepted string // the acked command (ack = accepted, not completed)
	Version  string // the version answer
	Err      string // wire error text ("not ready", "unknown command") when not OK
	Raw      string // the raw reply line as received (evidence for error reports)
}

// NotReady reports whether the reply is the instance-still-booting
// answer ("not ready"), which callers usually treat as success: the
// booting instance acts once ready.
func (r Reply) NotReady() bool { return !r.OK && r.Err == errNotReady }

// Send dials the single-instance socket, delivers cmd as one JSON
// request line, and returns the parsed reply. timeout is one absolute
// deadline bounding the dial and the exchange. A failure to dial --
// no socket file, or a dead one -- comes back wrapped in
// ErrNotRunning so callers can distinguish "nothing to talk to" from
// a broken exchange. A reply line that does not parse as JSON is
// returned in-band (Reply.Raw set, OK false, empty Err) for the
// caller to quote in its "unexpected reply" report -- that is what a
// still-running pre-JSON daemon's raw line now earns, the
// restart-it-once upgrade signal -- and only transport failures are
// errors.
func Send(path, cmd string, timeout time.Duration) (Reply, error) {
	deadline := time.Now().Add(timeout)
	req, err := json.Marshal(Request{Cmd: cmd})
	if err != nil {
		return Reply{}, err // unreachable: a plain string field always marshals
	}
	line, err := exchange(path, string(req), deadline)
	if err != nil {
		return Reply{}, err
	}
	return parseReply(line), nil
}

// exchange performs one dial-write-read cycle: line out (newline
// appended), one response line back, whitespace-trimmed. deadline
// bounds the dial and the whole exchange.
func exchange(path, line string, deadline time.Time) (string, error) {
	conn, err := net.DialTimeout("unix", path, time.Until(deadline))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		return "", err
	}
	resp, err := bufio.NewReader(io.LimitReader(conn, maxLine)).ReadString('\n')
	if err != nil && resp == "" {
		return "", err
	}
	return strings.TrimSpace(resp), nil
}

// parseReply maps one response line to a Reply: a line that parses as
// JSON maps field-for-field (unknown fields ignored -- the same
// tolerance the server applies to requests); anything else is
// garbage, returned in-band via Raw with OK false and an empty Err.
func parseReply(line string) Reply {
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err == nil {
		return Reply{OK: resp.OK, Accepted: resp.Accepted, Version: resp.Version, Err: resp.Error, Raw: line}
	}
	return Reply{Raw: line}
}

// IsNotRunning reports whether err (from Send) means no instance is
// listening on the socket.
func IsNotRunning(err error) bool {
	return errors.Is(err, ErrNotRunning)
}
