package ffext

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Host reconnect backoff bounds (the logic.mjs constants' Go-side
// twins; tests shrink them through HostOptions).
const (
	hostReconnectMin = time.Second
	hostReconnectMax = 30 * time.Second
	hostDialTimeout  = 2 * time.Second
)

// HostOptions configures RunHost. In/Out are the native-messaging
// stdio pair Firefox owns; SocketPath is the bridge socket to relay
// to.
type HostOptions struct {
	In         io.Reader
	Out        io.Writer
	SocketPath string
	// Logf receives diagnostics (nil = silent). The host's stdout is
	// the protocol channel, so diagnostics must go to stderr or a log
	// file -- NEVER hand a stdout-backed writer to this.
	Logf func(format string, args ...any)

	// Test seams: the socket dialer and the reconnect backoff bounds.
	dial                       func(path string) (net.Conn, error)
	reconnectMin, reconnectMax time.Duration
}

// RunHost runs the native-messaging relay loop until its stdin ends:
// frames read from In (extension -> bridge: replies and pushes) are
// forwarded as JSON lines to the bridge socket, and lines read from
// the socket (bridge -> extension: requests) are framed onto Out. The
// socket side reconnects with capped exponential backoff and NEVER
// takes the process down -- the extension owns the port, and the link
// simply comes up when the app starts. stdin EOF (Firefox closed the
// port, unloaded the extension, or is quitting) is the clean-shutdown
// signal and returns nil.
func RunHost(opts HostOptions) error {
	h := &hostRelay{
		out:  opts.Out,
		path: opts.SocketPath,
		logf: opts.Logf,
		dial: opts.dial,
		min:  opts.reconnectMin,
		max:  opts.reconnectMax,
	}
	if h.logf == nil {
		h.logf = func(string, ...any) {}
	}
	if h.dial == nil {
		h.dial = func(path string) (net.Conn, error) {
			return net.DialTimeout("unix", path, hostDialTimeout)
		}
	}
	if h.min <= 0 {
		h.min = hostReconnectMin
	}
	if h.max < h.min {
		h.max = hostReconnectMax
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.socketLoop(ctx)
	}()

	var err error
	for {
		body, rerr := ReadFrame(opts.In, MaxInFrame)
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				err = rerr
			}
			break
		}
		h.forward(body)
	}
	cancel()
	h.shutdownConn() // closes a live connection, unblocking socketLoop
	wg.Wait()
	return err
}

// hostRelay is RunHost's state: the current bridge connection slot and
// the stdout framer.
type hostRelay struct {
	out  io.Writer
	path string
	logf func(string, ...any)
	dial func(string) (net.Conn, error)
	min  time.Duration
	max  time.Duration

	mu         sync.Mutex
	conn       net.Conn
	shutdown   bool
	loggedDrop bool // one dropped-frame log per disconnected episode
}

// socketLoop keeps one bridge connection alive: dial with capped
// exponential backoff, then pump its lines onto stdout as frames until
// it dies, then start over -- all bounded by ctx.
func (h *hostRelay) socketLoop(ctx context.Context) {
	delay := h.min
	loggedDown := false
	for ctx.Err() == nil {
		c, err := h.dial(h.path)
		if err != nil {
			if !loggedDown {
				h.logf("ffext-host: app socket unavailable: %v (retrying)", err)
				loggedDown = true
			}
			if !sleepCtx(ctx, delay) {
				return
			}
			delay = min(delay*2, h.max)
			continue
		}
		delay = h.min
		loggedDown = false
		if !h.setConn(c) {
			_ = c.Close()
			return // shut down while dialing
		}
		h.logf("ffext-host: connected to the app")
		h.pumpToOut(c)
		h.setConn(nil)
		if ctx.Err() == nil {
			h.logf("ffext-host: app connection lost")
		}
	}
}

// pumpToOut frames every socket line onto stdout until the connection
// dies. Requests are host->extension messages, so the 1 MB
// host-to-extension cap applies (an oversized frame would kill the
// port; requests are tiny, this is pure defense).
func (h *hostRelay) pumpToOut(c net.Conn) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := WriteFrame(h.out, line, MaxOutFrame); err != nil {
			h.logf("ffext-host: write to extension: %v", err)
			return
		}
	}
}

// forward relays one extension frame to the bridge as a JSON line;
// with no live connection the frame is dropped (a reply nobody awaits,
// or a push the app will re-request at its next summon).
func (h *hostRelay) forward(body []byte) {
	h.mu.Lock()
	c := h.conn
	dropLogged := h.loggedDrop
	if c == nil {
		h.loggedDrop = true
	}
	h.mu.Unlock()
	if c == nil {
		if !dropLogged {
			h.logf("ffext-host: app not connected; dropping extension message(s)")
		}
		return
	}
	line := body
	if bytes.IndexByte(body, '\n') >= 0 {
		// Line framing cannot carry embedded newlines; JSON only holds
		// them as formatting whitespace, so compacting is lossless.
		var buf bytes.Buffer
		if err := json.Compact(&buf, body); err != nil {
			h.logf("ffext-host: dropping unparseable extension message: %v", err)
			return
		}
		line = buf.Bytes()
	}
	if _, err := c.Write(append(line, '\n')); err != nil {
		h.logf("ffext-host: write to app: %v", err)
	}
}

// setConn swaps the live connection (closing any previous one) and
// resets the per-episode drop log. false means the relay already shut
// down -- a dial that completed after shutdownConn ran must NOT be
// kept, or pumpToOut would block forever on a connection nobody will
// ever close.
func (h *hostRelay) setConn(c net.Conn) bool {
	h.mu.Lock()
	if h.shutdown && c != nil {
		h.mu.Unlock()
		return false
	}
	prev := h.conn
	h.conn = c
	if c != nil {
		h.loggedDrop = false
	}
	h.mu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	return true
}

// shutdownConn marks the relay closed and closes any live connection;
// a dial racing this hands its fresh connection to setConn, which now
// refuses it.
func (h *hostRelay) shutdownConn() {
	h.mu.Lock()
	h.shutdown = true
	c := h.conn
	h.conn = nil
	h.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// sleepCtx sleeps d or until ctx is done; false = ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
