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

// Reply is Send's parsed result, covering both wire shapes. All
// parsing lives here so callers branch on fields, never on wire
// strings.
type Reply struct {
	OK       bool   // the request was accepted (or answered)
	Accepted string // the acked command (JSON servers; ack = accepted, not completed)
	Version  string // the version answer
	Err      string // wire error text ("not ready", "unknown command") when not OK
	Raw      string // the raw reply line as received (evidence for error reports)
	Legacy   bool   // the reply was parsed from the legacy (v1) line protocol
}

// NotReady reports whether the reply is the instance-still-booting
// answer (JSON "not ready" or legacy "err not ready"), which callers
// usually treat as success: the booting instance acts once ready.
func (r Reply) NotReady() bool { return !r.OK && r.Err == errNotReady }

// Send dials the single-instance socket, delivers cmd, and returns
// the parsed reply. It speaks the JSON (v2) protocol and degrades to
// the legacy (v1) line protocol against an old daemon: exactly the
// legacy "err unknown command" string answering the JSON request
// means the server predates JSON, so Send retries ONCE on a fresh
// connection (one request per connection) with the bare legacy
// command line. timeout is one absolute deadline bounding everything
// -- dial, exchange, and the retry. A failure to dial -- no socket
// file, or a dead one -- comes back wrapped in ErrNotRunning so
// callers can distinguish "nothing to talk to" from a broken
// exchange. A reply that fits neither shape is returned in-band
// (Reply.Raw with OK false and an empty Err) for the caller to
// report; only transport failures are errors.
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
	if line != replyUnknown {
		return parseReply(cmd, line), nil
	}
	// The legacy "err unknown command" line answering a JSON request
	// is an old, pre-JSON daemon: retry once with the legacy bare-word
	// request. A JSON-speaking server never answers these bytes to a
	// JSON line (its unknown-command answer is itself JSON).
	line, err = exchange(path, cmd, deadline)
	if err != nil {
		return Reply{}, err
	}
	return parseLegacy(cmd, line), nil
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

// parseReply maps one response line to a Reply: a line that looks
// like JSON parses as a Response (unknown fields ignored -- the same
// tolerance the server applies to requests), anything else takes the
// legacy mapping. cmd contextualizes the legacy bare-version reply.
func parseReply(cmd, line string) Reply {
	if strings.HasPrefix(line, "{") {
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err == nil {
			return Reply{OK: resp.OK, Accepted: resp.Accepted, Version: resp.Version, Err: resp.Error, Raw: line}
		}
		return Reply{Raw: line} // JSON-looking garbage: in-band for the caller
	}
	return parseLegacy(cmd, line)
}

// parseLegacy maps one legacy (v1) response line to a Reply: "ok",
// "err <reason>", or -- for the version command only, whose legacy
// answer is the bare version string -- any other non-empty line.
// Anything else is garbage, returned in-band via Raw.
func parseLegacy(cmd, line string) Reply {
	switch {
	case line == ReplyOK:
		return Reply{OK: true, Raw: line, Legacy: true}
	case strings.HasPrefix(line, "err "):
		return Reply{Err: strings.TrimPrefix(line, "err "), Raw: line, Legacy: true}
	case cmd == CmdVersion && line != "":
		return Reply{OK: true, Version: line, Raw: line, Legacy: true}
	default:
		return Reply{Raw: line, Legacy: true}
	}
}

// IsNotRunning reports whether err (from Send) means no instance is
// listening on the socket.
func IsNotRunning(err error) bool {
	return errors.Is(err, ErrNotRunning)
}
