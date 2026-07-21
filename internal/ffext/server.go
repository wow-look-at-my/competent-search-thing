package ffext

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by Listen when a live process already
// serves the bridge socket (the stale-socket probe got an answer) --
// the ipc.ErrAlreadyRunning twin for the second socket.
var ErrAlreadyRunning = errors.New("another instance already serves the ffext socket")

// ErrNotConnected is returned by Activate when no companion extension
// host holds a live connection (or the token names a connection that
// is gone); callers fall back to opening the URL.
var ErrNotConnected = errors.New("no companion extension connected")

const (
	// probeTimeout bounds the is-anyone-there dial Listen makes when
	// the socket address is already taken.
	probeTimeout = 500 * time.Millisecond
	// maxLine caps one host->bridge socket line (a full tab dump plus
	// headroom; mirrors MaxInFrame).
	maxLine = 8 << 20
	// activateTimeout / listTimeout bound one request round-trip
	// through the host and the extension.
	activateTimeout = 1200 * time.Millisecond
	listTimeout     = 1000 * time.Millisecond
	// writeTimeout bounds one request line write so a wedged host
	// process can never block the app.
	writeTimeout = 2 * time.Second
)

// ServerOptions configures Listen.
type ServerOptions struct {
	// Logf receives diagnostics (nil = silent).
	Logf func(format string, args ...any)
}

// Server is the app-side bridge: it owns the unix listener the host
// relay processes dial, correlates request/reply pairs per
// connection, and keeps the merged last-known live-tab snapshot.
// Connections are PERSISTENT (unlike internal/ipc's one-request
// conns): one per running host, usually exactly one.
type Server struct {
	ln   net.Listener
	logf func(string, ...any)

	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	mu          sync.Mutex // guards conns, nextConn, refreshBusy, closed, and every hostConn's tabs/tabsAt
	conns       map[int64]*hostConn
	nextConn    int64
	refreshBusy bool
	closed      bool

	reqID atomic.Int64
}

// hostConn is one connected host relay.
type hostConn struct {
	id   int64
	conn net.Conn

	wmu sync.Mutex // serializes request-line writes

	pmu     sync.Mutex // guards pending
	pending map[int64]pendingReq

	// tabs/tabsAt are this connection's last-known tab list and when
	// it arrived -- guarded by the SERVER mutex (snapshot merging
	// iterates connections under it anyway).
	tabs   []Tab
	tabsAt time.Time
}

// pendingReq is one in-flight request slot. isList marks a listTabs
// request: its reply's tab dump is stored by serveConn itself, IN
// ARRIVAL ORDER relative to tabsChanged pushes on the same connection
// -- a requester-side store could run late and clobber a newer push.
type pendingReq struct {
	ch     chan inbound
	isList bool
}

// Listen binds the bridge socket at path and starts the accept loop.
// Stale sockets are recovered exactly like internal/ipc's Listen
// (probe dial; an answer = ErrAlreadyRunning, a dead file = remove +
// retry once), and the socket file is chmodded 0600 -- filesystem
// permissions are the only auth.
func Listen(path string, opts ServerOptions) (*Server, error) {
	ln, err := listenOrRecover(path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Server{ln: ln, logf: logf, conns: map[int64]*hostConn{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// listenOrRecover implements the stale-socket recovery around a plain
// unix listen (the internal/ipc shape).
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
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return nil, rerr
	}
	return net.Listen("unix", path)
}

// accept registers each host connection and serves it on its own
// goroutine until the listener closes. Every fresh connection is asked
// for its tab list once, so the snapshot warms up without waiting for
// a summon.
func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.nextConn++
		hc := &hostConn{id: s.nextConn, conn: c, pending: map[int64]pendingReq{}}
		s.conns[hc.id] = hc
		s.wg.Add(2)
		s.mu.Unlock()
		s.logf("ffext: extension host connected (conn %d)", hc.id)
		go s.serveConn(hc)
		go func() {
			defer s.wg.Done()
			s.listInto(hc)
		}()
	}
}

// serveConn reads one connection's lines: tabsChanged pushes update
// the snapshot, correlated replies complete their pending request, and
// anything else is ignored (the tolerance contract). On EOF or error
// the connection is dropped and its tabs leave the merged snapshot.
func (s *Server) serveConn(hc *hostConn) {
	defer s.wg.Done()
	sc := bufio.NewScanner(hc.conn)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		var in inbound
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			s.logf("ffext: conn %d: unparseable line: %v", hc.id, err)
			continue
		}
		switch {
		case in.Type == MsgTabsChanged:
			s.storeTabs(hc, in.Tabs)
		case in.ID != 0:
			hc.pmu.Lock()
			p, ok := hc.pending[in.ID]
			delete(hc.pending, in.ID)
			hc.pmu.Unlock()
			if ok {
				// Store a list reply's dump HERE, before handing the
				// reply over: snapshot updates then happen strictly in
				// arrival order on this goroutine, so a slow requester
				// can never overwrite a newer tabsChanged push.
				if p.isList && in.OK {
					s.storeTabs(hc, in.Tabs)
				}
				p.ch <- in // buffered(1); the sole send for this id
			}
		}
	}
	s.dropConn(hc)
}

// dropConn unregisters a dead connection: its tabs leave the merged
// snapshot immediately (they are no longer live), and every pending
// request completes with a closed channel instead of waiting out its
// timeout.
func (s *Server) dropConn(hc *hostConn) {
	s.mu.Lock()
	_, present := s.conns[hc.id]
	delete(s.conns, hc.id)
	closed := s.closed
	s.mu.Unlock()
	_ = hc.conn.Close()
	hc.pmu.Lock()
	pend := hc.pending
	hc.pending = nil
	hc.pmu.Unlock()
	for _, p := range pend {
		close(p.ch)
	}
	if present && !closed {
		s.logf("ffext: extension host disconnected (conn %d)", hc.id)
	}
}

// storeTabs replaces one connection's tab list with a fresh wire dump,
// tagging each row with the owning connection id.
func (s *Server) storeTabs(hc *hostConn, wire []wireTab) {
	tabs := make([]Tab, 0, len(wire))
	for _, w := range wire {
		if w.ID < 0 {
			continue // tabs.TAB_ID_NONE rows are not addressable
		}
		tabs = append(tabs, Tab{
			Conn:         hc.id,
			ID:           w.ID,
			WindowID:     w.WindowID,
			Title:        w.Title,
			URL:          w.URL,
			Pinned:       w.Pinned,
			LastAccessed: int64(w.LastAccessed),
			Active:       w.Active,
			FavIconURL:   w.FavIconURL,
		})
	}
	s.mu.Lock()
	hc.tabs, hc.tabsAt = tabs, time.Now()
	s.mu.Unlock()
}

// Tabs returns the merged last-known live-tab snapshot (connection
// order, then each connection's wire order) and the newest per-conn
// update time -- the caller's freshness gate. Empty + zero time when
// nothing has reported yet.
func (s *Server) Tabs() ([]Tab, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(s.conns))
	for id := range s.conns {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []Tab
	var at time.Time
	for _, id := range ids {
		hc := s.conns[id]
		out = append(out, hc.tabs...)
		if hc.tabsAt.After(at) {
			at = hc.tabsAt
		}
	}
	return out, at
}

// Connected reports whether at least one host connection is live.
func (s *Server) Connected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns) > 0
}

// KickRefresh asynchronously asks every connected host for a fresh tab
// list (single-flight; a no-op with no connections). It never blocks
// the caller -- the summon path calls it fire-and-forget.
func (s *Server) KickRefresh() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.refreshBusy || s.closed || len(s.conns) == 0 {
		s.mu.Unlock()
		return
	}
	s.refreshBusy = true
	conns := make([]*hostConn, 0, len(s.conns))
	for _, hc := range s.conns {
		conns = append(conns, hc)
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.refreshBusy = false
			s.mu.Unlock()
		}()
		for _, hc := range conns {
			s.listInto(hc)
		}
	}()
}

// listInto performs one listTabs round-trip on hc; the reply's dump is
// stored by serveConn on arrival (see pendingReq), so this only logs
// failures -- which keep the previous list (a dead connection drops it
// via dropConn instead).
func (s *Server) listInto(hc *hostConn) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	in, err := s.request(ctx, hc, request{Type: MsgListTabs})
	if err != nil {
		s.logf("ffext: conn %d: listTabs: %v", hc.id, err)
		return
	}
	if !in.OK {
		s.logf("ffext: conn %d: listTabs refused: %s", hc.id, in.Error)
	}
}

// Activate routes one tab activation to the owning connection and
// waits for the extension's verdict (bounded by activateTimeout). Any
// failure -- connection gone, timeout, ok:false -- is an error; the
// caller falls back to opening the URL.
func (s *Server) Activate(connID, tabID, windowID int64) error {
	if s == nil {
		return ErrNotConnected
	}
	s.mu.Lock()
	hc := s.conns[connID]
	s.mu.Unlock()
	if hc == nil {
		return fmt.Errorf("%w (connection %d is gone)", ErrNotConnected, connID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), activateTimeout)
	defer cancel()
	in, err := s.request(ctx, hc, request{Type: MsgActivate, TabID: tabID, WindowID: windowID})
	if err != nil {
		return fmt.Errorf("activate tab %d: %w", tabID, err)
	}
	if !in.OK {
		if in.Error != "" {
			return fmt.Errorf("activate tab %d: %s", tabID, in.Error)
		}
		return fmt.Errorf("activate tab %d: refused", tabID)
	}
	return nil
}

// request performs one correlated round-trip on hc: register a pending
// slot, write the request line, wait for the matching reply or ctx.
func (s *Server) request(ctx context.Context, hc *hostConn, req request) (inbound, error) {
	req.ID = s.reqID.Add(1)
	ch := make(chan inbound, 1)
	hc.pmu.Lock()
	if hc.pending == nil {
		hc.pmu.Unlock()
		return inbound{}, errors.New("host connection closed")
	}
	hc.pending[req.ID] = pendingReq{ch: ch, isList: req.Type == MsgListTabs}
	hc.pmu.Unlock()
	defer func() {
		hc.pmu.Lock()
		if hc.pending != nil {
			delete(hc.pending, req.ID)
		}
		hc.pmu.Unlock()
	}()
	line, err := json.Marshal(req)
	if err != nil {
		return inbound{}, err // a plain struct: cannot happen
	}
	hc.wmu.Lock()
	_ = hc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = hc.conn.Write(append(line, '\n'))
	hc.wmu.Unlock()
	if err != nil {
		return inbound{}, err
	}
	select {
	case in, ok := <-ch:
		if !ok {
			return inbound{}, errors.New("host connection closed")
		}
		return in, nil
	case <-ctx.Done():
		return inbound{}, ctx.Err()
	}
}

// Close stops the accept loop, closes the listener (unlinking the
// socket) and every connection, and waits for the per-connection
// goroutines. Idempotent and nil-safe (the ipc.Server contract).
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conns := make([]*hostConn, 0, len(s.conns))
		for _, hc := range s.conns {
			conns = append(conns, hc)
		}
		s.mu.Unlock()
		s.closeErr = s.ln.Close()
		for _, hc := range conns {
			_ = hc.conn.Close()
		}
		s.wg.Wait()
	})
	return s.closeErr
}
