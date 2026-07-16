package ipc

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by Listen when a live instance already
// serves the socket (the stale-socket probe got an answer).
var ErrAlreadyRunning = errors.New("another instance is already running")

const (
	// probeTimeout bounds the is-anyone-there dial Listen makes when
	// the socket address is already taken.
	probeTimeout = 500 * time.Millisecond
	// connTimeout bounds each accepted connection: one request line
	// in, one response line out.
	connTimeout = 2 * time.Second
	// maxLine caps how many bytes of a request or response line are
	// read; the protocol's lines are all tiny.
	maxLine = 4096
)

// Handlers carries the app callbacks the server invokes for the
// toggle/show/hide commands. Handlers are invoked on connection
// goroutines and must be safe to call concurrently. A nil member
// answers ReplyNotReady, so a not-yet-wired (or partially wired)
// server degrades gracefully instead of crashing.
type Handlers struct {
	Toggle func()
	Show   func()
	Hide   func()
}

// Server owns the unix listener and the accept loop behind the
// single-instance socket. Create one with Listen; wire the app in
// later with SetHandlers (commands arriving before that are answered
// ReplyNotReady, except version/ping which always work).
type Server struct {
	ln      net.Listener
	version string

	mu       sync.Mutex // guards handlers
	handlers Handlers

	wg        sync.WaitGroup // accept loop + in-flight connections
	closeOnce sync.Once
	closeErr  error
}

// Listen binds the single-instance socket at path and starts the
// accept loop. When the address is taken it probes for a live
// instance: an answering socket means ErrAlreadyRunning, a dead one
// (crashed instance that never unlinked) is removed and the listen
// retried once. The socket file is chmodded to 0600 -- the protocol
// has no authentication beyond filesystem permissions.
func Listen(path, version string) (*Server, error) {
	ln, err := listenOrRecover(path)
	if err != nil {
		return nil, err
	}
	// Best effort: the socket is created with the process umask; keep
	// it owner-only regardless.
	_ = os.Chmod(path, 0o600)
	s := &Server{ln: ln, version: version}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// listenOrRecover implements the stale-socket recovery around a plain
// unix listen.
func listenOrRecover(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
		return ln, err
	}
	conn, derr := net.DialTimeout("unix", path, probeTimeout)
	if derr == nil {
		_ = conn.Close()
		return nil, ErrAlreadyRunning
	}
	// Nobody answered: a crashed instance left the file behind.
	// Remove it and retry exactly once.
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return nil, rerr
	}
	return net.Listen("unix", path)
}

// SetHandlers installs (or replaces) the command callbacks. Safe to
// call at any time relative to incoming connections.
func (s *Server) SetHandlers(h Handlers) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.handlers = h
	s.mu.Unlock()
}

// Close stops the accept loop, closes the listener (Go unlinks the
// socket file), and waits for in-flight connections to finish. It is
// idempotent and safe on a nil Server.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.ln.Close() })
	s.wg.Wait()
	return s.closeErr
}

// accept hands each connection to its own goroutine until the
// listener is closed.
func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed (or fatally broken): stop serving
		}
		s.wg.Add(1)
		go s.handle(conn)
	}
}

// handle serves one request/response exchange on conn.
func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(connTimeout))
	line, err := bufio.NewReader(io.LimitReader(conn, maxLine)).ReadString('\n')
	if err != nil && line == "" {
		return // nothing readable (timeout, empty close): no response owed
	}
	_, _ = conn.Write([]byte(s.respond(strings.TrimSpace(line)) + "\n"))
}

// respond maps one command line to its response line.
func (s *Server) respond(cmd string) string {
	switch cmd {
	case CmdPing:
		return ReplyOK
	case CmdVersion:
		return s.version
	case CmdToggle, CmdShow, CmdHide:
		s.mu.Lock()
		var f func()
		switch cmd {
		case CmdToggle:
			f = s.handlers.Toggle
		case CmdShow:
			f = s.handlers.Show
		case CmdHide:
			f = s.handlers.Hide
		}
		s.mu.Unlock()
		if f == nil {
			return ReplyNotReady
		}
		f()
		return ReplyOK
	default:
		return replyUnknown
	}
}
