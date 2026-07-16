package ipc

import (
	"bufio"
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

// Send dials the single-instance socket, writes one command line and
// returns the single response line (whitespace-trimmed). timeout
// bounds the dial and the whole exchange. A failure to dial -- no
// socket file, or a dead one -- comes back wrapped in ErrNotRunning so
// callers can distinguish "nothing to talk to" from a broken exchange.
func Send(path, cmd string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, maxLine)).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// IsNotRunning reports whether err (from Send) means no instance is
// listening on the socket.
func IsNotRunning(err error) bool {
	return errors.Is(err, ErrNotRunning)
}
