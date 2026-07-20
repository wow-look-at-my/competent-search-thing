package ipc

import (
	"bufio"
	"encoding/json"
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
// toggle/show/hide commands. Handlers are invoked AFTER the
// acknowledgement is written: a slow or blocked handler (e.g. one
// stuck behind a busy GUI main thread during startup indexing) can no
// longer time out the client. Handlers still run on connection
// goroutines and must be safe to call concurrently. A nil member
// answers the not-ready error, so a not-yet-wired (or partially
// wired) server degrades gracefully instead of crashing.
type Handlers struct {
	Toggle func()
	Show   func()
	Hide   func()
	// Config summons the bar into its config editor (the CLI "config"
	// subcommand); nil answers not-ready like the others.
	Config func()
}

// Server owns the unix listener and the accept loop behind the
// single-instance socket. Create one with Listen; wire the app in
// later with SetHandlers (commands arriving before that are answered
// the not-ready error, except version/ping which always work).
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

// handle serves one request/response exchange on conn: read the
// request line, plan its JSON response, write it, then run the
// command's handler (if any). JSON is the only wire shape: a line
// that does not parse as a JSON request -- including the bare command
// words of the deleted pre-JSON line protocol -- is answered with the
// invalid-request error.
func (s *Server) handle(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(connTimeout))
	line, err := bufio.NewReader(io.LimitReader(conn, maxLine)).ReadString('\n')
	if err != nil && line == "" {
		return // nothing readable (timeout, empty close): no response owed
	}
	resp, after := s.plan(strings.TrimSpace(line))
	reply, _ := json.Marshal(resp) // plain struct: Marshal cannot fail
	_, _ = conn.Write(append(reply, '\n'))
	if after != nil {
		// The handler runs only after the acknowledgement is on the
		// wire, so a slow app never times the client out -- but still
		// on this connection goroutine, so Close's WaitGroup (which
		// tracks it) keeps waiting for in-flight handlers.
		after()
	}
}

// plan maps one request line to its Response plus the handler to
// invoke after that response is written (nil for commands that answer
// without side effects, unknown commands, unwired handlers, and
// invalid requests). Unknown fields in the request are ignored (the
// tolerance contract); a line that does not parse as JSON answers the
// invalid-request error and runs nothing.
func (s *Server) plan(line string) (Response, func()) {
	var req Request
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return Response{Error: errInvalidRequest}, nil
	}
	switch req.Cmd {
	case CmdPing:
		return Response{OK: true}, nil
	case CmdVersion:
		return Response{OK: true, Version: s.version}, nil
	}
	f, known := s.handlerFor(req.Cmd)
	if !known {
		return Response{Error: errUnknownCommand}, nil
	}
	if f == nil {
		return Response{Error: errNotReady}, nil
	}
	return Response{OK: true, Accepted: req.Cmd}, f
}

// handlerFor resolves a toggle/show/hide/config command to its wired
// handler (nil = not wired yet, the not-ready answer); known is false
// for any other command.
func (s *Server) handlerFor(cmd string) (f func(), known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch cmd {
	case CmdToggle:
		return s.handlers.Toggle, true
	case CmdShow:
		return s.handlers.Show, true
	case CmdHide:
		return s.handlers.Hide, true
	case CmdConfig:
		return s.handlers.Config, true
	}
	return nil, false
}
