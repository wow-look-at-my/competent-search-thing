package ffext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// extensionPipes builds the native-messaging stdio pair for a RunHost
// under test: the test plays Firefox's extension side, writing frames
// into hostIn and reading frames from hostOut.
type extensionPipes struct {
	t *testing.T
	// toHost feeds the host's stdin; fromHost is the host's stdout.
	toHostW  *io.PipeWriter
	fromHost *io.PipeReader
	done     chan error
	doneOnce sync.Once
	doneErr  error
}

// wait blocks until RunHost returned (idempotent) and yields its
// error.
func (p *extensionPipes) wait() error {
	p.doneOnce.Do(func() {
		select {
		case p.doneErr = <-p.done:
		case <-time.After(3 * time.Second):
			p.t.Error("RunHost never returned")
			p.doneErr = errors.New("RunHost timeout")
		}
	})
	return p.doneErr
}

func startHost(t *testing.T, socketPath string, min, max time.Duration) *extensionPipes {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &extensionPipes{t: t, toHostW: inW, fromHost: outR, done: make(chan error, 1)}
	go func() {
		p.done <- RunHost(HostOptions{
			In:           inR,
			Out:          outW,
			SocketPath:   socketPath,
			Logf:         t.Logf,
			reconnectMin: min,
			reconnectMax: max,
		})
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = p.wait()
	})
	return p
}

// sendFrame writes one extension->host frame (a message toward the
// bridge).
func (p *extensionPipes) sendFrame(m map[string]any) {
	p.t.Helper()
	raw, err := json.Marshal(m)
	require.NoError(p.t, err)
	require.NoError(p.t, WriteFrame(p.toHostW, raw, MaxOutFrame))
}

// readFrame reads one host->extension frame (a bridge request).
func (p *extensionPipes) readFrame() map[string]any {
	p.t.Helper()
	type res struct {
		body []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		body, err := ReadFrame(p.fromHost, MaxInFrame)
		ch <- res{body, err}
	}()
	select {
	case r := <-ch:
		require.NoError(p.t, r.err)
		var m map[string]any
		require.NoError(p.t, json.Unmarshal(r.body, &m))
		return m
	case <-time.After(3 * time.Second):
		p.t.Fatal("no frame from the host")
		return nil
	}
}

func TestHostRelaysEndToEnd(t *testing.T) {
	s, path := listenBridge(t)
	p := startHost(t, path, 5*time.Millisecond, 20*time.Millisecond)

	// The server asks the fresh connection for its tabs; the request
	// frame reaches the extension side.
	req := p.readFrame()
	require.Equal(t, MsgListTabs, req["type"])
	id := req["id"]
	require.NotNil(t, id)

	// The extension answers; the reply flows back and fills the
	// snapshot.
	p.sendFrame(map[string]any{
		"id": id, "ok": true,
		"tabs": []map[string]any{wireTabRow(11, 2, "\u00dcber tab", "https://tab.example/")},
	})
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, int64(11), tabs[0].ID)

	// An activate round-trip through the same pipes.
	done := make(chan error, 1)
	go func() { done <- s.Activate(tabs[0].Conn, tabs[0].ID, tabs[0].WindowID) }()
	req = p.readFrame()
	require.Equal(t, MsgActivate, req["type"])
	require.Equal(t, float64(11), req["tabId"])
	require.Equal(t, float64(2), req["windowId"])
	p.sendFrame(map[string]any{"id": req["id"], "ok": true})
	require.NoError(t, <-done)

	// An unsolicited push updates the snapshot too.
	p.sendFrame(map[string]any{
		"type": MsgTabsChanged,
		"tabs": []map[string]any{
			wireTabRow(11, 2, "T", "https://tab.example/"),
			wireTabRow(12, 2, "U", "https://u.example/"),
		},
	})
	awaitTabs(t, s, 2)
}

func TestHostStdinEOFExitsCleanly(t *testing.T) {
	_, path := listenBridge(t)
	p := startHost(t, path, 5*time.Millisecond, 20*time.Millisecond)
	p.readFrame() // on-connect listTabs
	require.NoError(t, p.toHostW.Close())
	require.NoError(t, p.wait(), "stdin EOF is the clean shutdown")
}

func TestHostSurvivesAppDownAndReconnects(t *testing.T) {
	// No server yet: the host must stay alive, drop extension
	// messages, and connect once the app appears.
	path := testSocket(t)
	p := startHost(t, path, 5*time.Millisecond, 20*time.Millisecond)

	// A push while the app is down is dropped without crashing.
	p.sendFrame(map[string]any{"type": MsgTabsChanged, "tabs": []map[string]any{}})

	s, err := Listen(path, ServerOptions{Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// The host reconnects; the server's on-connect list arrives.
	req := p.readFrame()
	require.Equal(t, MsgListTabs, req["type"])
	p.sendFrame(map[string]any{
		"id": req["id"], "ok": true,
		"tabs": []map[string]any{wireTabRow(1, 1, "Back", "https://b.example/")},
	})
	awaitTabs(t, s, 1)
}

func TestHostReconnectsAfterAppRestart(t *testing.T) {
	s1, path := listenBridge(t)
	p := startHost(t, path, 5*time.Millisecond, 20*time.Millisecond)
	req := p.readFrame()
	p.sendFrame(map[string]any{"id": req["id"], "ok": true, "tabs": []map[string]any{}})
	require.NoError(t, s1.Close())

	// A second app instance on the SAME path (the sequential-boot CI
	// pattern): the host reconnects to it.
	s2, err := Listen(path, ServerOptions{Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	req = p.readFrame()
	require.Equal(t, MsgListTabs, req["type"])
	p.sendFrame(map[string]any{
		"id": req["id"], "ok": true,
		"tabs": []map[string]any{wireTabRow(9, 1, "New app", "https://n.example/")},
	})
	awaitTabs(t, s2, 1)
}

func TestHostCompactsMultilineJSON(t *testing.T) {
	// A frame carrying formatted (multi-line) JSON must survive the
	// line framing: the host compacts it.
	path := testSocket(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	// The host DROPS stdin frames until its socket connection is
	// stored (by design: "app not connected"), and ln.Accept returning
	// only proves the DIAL completed -- the relay's setConn runs
	// moments later on its own goroutine. Writing the first frame on
	// Accept alone therefore races the drop path (observed as a flaky
	// 2s read timeout under load), so gate the first write on the
	// host's own connected log line, which is emitted strictly after
	// setConn.
	connected := make(chan struct{})
	var connOnce sync.Once
	logf := func(format string, args ...any) {
		if strings.Contains(fmt.Sprintf(format, args...), "connected to the app") {
			connOnce.Do(func() { close(connected) })
		}
		t.Logf(format, args...)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunHost(HostOptions{
			In: inR, Out: outW, SocketPath: path, Logf: logf,
			reconnectMin: 5 * time.Millisecond, reconnectMax: 20 * time.Millisecond,
		})
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		<-done
	})
	_ = outR

	conn, err := ln.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("the host never reported its connection")
	}

	pretty := "{\n  \"type\": \"tabsChanged\",\n  \"tabs\": []\n}"
	require.NoError(t, WriteFrame(inW, []byte(pretty), MaxOutFrame))
	buf := make([]byte, 256)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := conn.Read(buf)
	require.NoError(t, err)
	line := string(buf[:n])
	require.Contains(t, line, `"type":"tabsChanged"`)
	require.Equal(t, byte('\n'), line[len(line)-1])
	require.NotContains(t, line[:len(line)-1], "\n", "one line, newlines compacted away")

	// Garbage that cannot compact is dropped, not forwarded.
	require.NoError(t, WriteFrame(inW, []byte("{not json\nat all"), MaxOutFrame))
	require.NoError(t, WriteFrame(inW, []byte(`{"type":"tabsChanged","tabs":[]}`), MaxOutFrame))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err = conn.Read(buf)
	require.NoError(t, err)
	require.NotContains(t, string(buf[:n]), "not json")
}

func TestHostStdinErrorPropagates(t *testing.T) {
	path := testSocket(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunHost(HostOptions{
			In: inR, Out: outW, SocketPath: path, Logf: t.Logf,
			reconnectMin: 5 * time.Millisecond, reconnectMax: 20 * time.Millisecond,
		})
	}()
	// A torn frame (prefix promising more than arrives) surfaces as an
	// error, not a clean exit.
	require.NoError(t, WriteFrame(inW, []byte("{}"), MaxOutFrame))
	_, err := inW.Write([]byte{9, 0, 0, 0, 'x'})
	require.NoError(t, err)
	require.NoError(t, inW.Close())
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("RunHost did not exit on a torn stdin")
	}
}
