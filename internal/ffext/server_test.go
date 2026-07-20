package ffext

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testSocket returns a fresh, short socket path (the internal/ipc
// pattern: t.TempDir names overflow darwin's ~104-byte sun_path).
func testSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ffext")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

// fakeHost is a scripted host-relay double on a raw unix connection:
// it answers listTabs with its tab list and activate per its script.
type fakeHost struct {
	t    *testing.T
	conn net.Conn
	rd   *bufio.Reader
	// tabs answers listTabs; activateErr != "" refuses activations.
	tabs        []map[string]any
	activateErr string
	// gotActivate records the activate requests seen.
	gotActivate chan request
}

func dialFakeHost(t *testing.T, path string) *fakeHost {
	t.Helper()
	conn, err := net.Dial("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeHost{
		t:           t,
		conn:        conn,
		rd:          bufio.NewReader(conn),
		gotActivate: make(chan request, 4),
	}
	return f
}

// serveOne answers exactly one request line.
func (f *fakeHost) serveOne() {
	f.t.Helper()
	require.NoError(f.t, f.conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	line, err := f.rd.ReadString('\n')
	require.NoError(f.t, err)
	var req request
	require.NoError(f.t, json.Unmarshal([]byte(line), &req))
	switch req.Type {
	case MsgListTabs:
		f.reply(map[string]any{"id": req.ID, "ok": true, "tabs": f.tabs})
	case MsgActivate:
		f.gotActivate <- req
		if f.activateErr != "" {
			f.reply(map[string]any{"id": req.ID, "ok": false, "error": f.activateErr})
			return
		}
		f.reply(map[string]any{"id": req.ID, "ok": true})
	default:
		f.t.Fatalf("unexpected request type %q", req.Type)
	}
}

func (f *fakeHost) reply(m map[string]any) {
	f.t.Helper()
	raw, err := json.Marshal(m)
	require.NoError(f.t, err)
	_, err = f.conn.Write(append(raw, '\n'))
	require.NoError(f.t, err)
}

func (f *fakeHost) push(m map[string]any) { f.reply(m) }

func wireTabRow(id, win int64, title, url string) map[string]any {
	return map[string]any{
		"id": id, "windowId": win, "title": title, "url": url,
		"pinned": false, "lastAccessed": 1000, "active": false,
	}
}

// awaitTabs polls the merged snapshot until want rows arrive.
func awaitTabs(t *testing.T, s *Server, want int) []Tab {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tabs, _ := s.Tabs()
		if len(tabs) == want {
			return tabs
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot never reached %d tabs (have %d)", want, len(tabs))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func listenBridge(t *testing.T) (*Server, string) {
	t.Helper()
	path := testSocket(t)
	s, err := Listen(path, ServerOptions{Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestServerConnectListsTabs(t *testing.T) {
	s, path := listenBridge(t)
	require.False(t, s.Connected())

	f := dialFakeHost(t, path)
	f.tabs = []map[string]any{
		wireTabRow(1, 10, "One", "https://one.example/"),
		wireTabRow(2, 10, "Two", "https://two.example/"),
	}
	// The server asks a fresh connection for its tabs unprompted.
	f.serveOne()
	tabs := awaitTabs(t, s, 2)
	require.True(t, s.Connected())
	require.Equal(t, int64(1), tabs[0].Conn)
	require.Equal(t, int64(1), tabs[0].ID)
	require.Equal(t, int64(10), tabs[0].WindowID)
	require.Equal(t, "One", tabs[0].Title)
	require.Equal(t, "https://one.example/", tabs[0].URL)
	require.Equal(t, int64(1000), tabs[0].LastAccessed)
	_, at := s.Tabs()
	require.WithinDuration(t, time.Now(), at, 3*time.Second)
}

func TestServerKickRefresh(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.tabs = nil
	f.serveOne() // the on-connect list: empty
	require.Empty(t, mustTabs(s))

	f.tabs = []map[string]any{wireTabRow(7, 1, "T", "https://t.example/")}
	s.KickRefresh()
	f.serveOne()
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, int64(7), tabs[0].ID)
}

func mustTabs(s *Server) []Tab {
	tabs, _ := s.Tabs()
	return tabs
}

func TestServerTabsChangedPush(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.serveOne() // on-connect list (empty)
	f.push(map[string]any{
		"type": MsgTabsChanged,
		"tabs": []map[string]any{wireTabRow(3, 2, "Pushed", "https://p.example/")},
	})
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, "Pushed", tabs[0].Title)
}

func TestServerActivateRoutesToOwningConn(t *testing.T) {
	s, path := listenBridge(t)
	f1 := dialFakeHost(t, path)
	f1.serveOne()
	f2 := dialFakeHost(t, path)
	f2.serveOne()

	done := make(chan error, 1)
	go func() { done <- s.Activate(2, 42, 7) }()
	f2.serveOne() // the activate lands on conn 2, not conn 1
	require.NoError(t, <-done)
	req := <-f2.gotActivate
	require.Equal(t, MsgActivate, req.Type)
	require.Equal(t, int64(42), req.TabID)
	require.Equal(t, int64(7), req.WindowID)
	require.Empty(t, f1.gotActivate)
}

func TestServerActivateFailures(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.serveOne()

	// Unknown connection: ErrNotConnected.
	err := s.Activate(99, 1, 1)
	require.ErrorIs(t, err, ErrNotConnected)

	// The extension refuses (stale tab id).
	f.activateErr = "Invalid tab ID: 42"
	done := make(chan error, 1)
	go func() { done <- s.Activate(1, 42, 7) }()
	f.serveOne()
	err = <-done
	require.Error(t, err)
	require.Contains(t, err.Error(), "Invalid tab ID")

	// A nil server is a plain not-connected.
	var nilS *Server
	require.ErrorIs(t, nilS.Activate(1, 1, 1), ErrNotConnected)
	require.False(t, nilS.Connected())
	nilS.KickRefresh()
	require.NoError(t, nilS.Close())
}

func TestServerActivateTimesOutOnSilentHost(t *testing.T) {
	if testing.Short() {
		t.Skip("timeout test")
	}
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.serveOne()
	// The fake never answers the activate.
	start := time.Now()
	err := s.Activate(1, 1, 1)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestServerConnDeathDropsItsTabs(t *testing.T) {
	s, path := listenBridge(t)
	f1 := dialFakeHost(t, path)
	f1.tabs = []map[string]any{wireTabRow(1, 1, "A", "https://a.example/")}
	f1.serveOne()
	awaitTabs(t, s, 1)

	f2 := dialFakeHost(t, path)
	f2.tabs = []map[string]any{wireTabRow(2, 1, "B", "https://b.example/")}
	f2.serveOne()
	awaitTabs(t, s, 2)

	_ = f1.conn.Close()
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, "B", tabs[0].Title, "the dead conn's tabs left the snapshot")
	require.True(t, s.Connected())
}

func TestServerToleratesGarbageLines(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.serveOne()
	_, err := f.conn.Write([]byte("not json\n{\"unknownField\":true}\n"))
	require.NoError(t, err)
	f.push(map[string]any{
		"type": MsgTabsChanged,
		"tabs": []map[string]any{wireTabRow(5, 1, "Still works", "https://x.example/")},
	})
	awaitTabs(t, s, 1)
}

func TestServerNegativeTabIDsSkipped(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	f.tabs = []map[string]any{
		wireTabRow(-1, 1, "None", "about:devtools"),
		wireTabRow(4, 1, "Real", "https://r.example/"),
	}
	f.serveOne()
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, int64(4), tabs[0].ID)
}

func TestServerFractionalLastAccessed(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	row := wireTabRow(1, 1, "F", "https://f.example/")
	row["lastAccessed"] = 1721456789123.75
	f.tabs = []map[string]any{row}
	f.serveOne()
	tabs := awaitTabs(t, s, 1)
	require.Equal(t, int64(1721456789123), tabs[0].LastAccessed)
}

func TestServerSecondListenerAlreadyRunning(t *testing.T) {
	_, path := listenBridge(t)
	_, err := Listen(path, ServerOptions{})
	require.ErrorIs(t, err, ErrAlreadyRunning)
}

func TestServerStaleSocketRecovered(t *testing.T) {
	path := testSocket(t)
	// A dead socket file nobody answers.
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	// Close WITHOUT unlinking (simulating a crash): net's Close
	// unlinks, so re-create the file the crude way.
	require.NoError(t, ln.Close())
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	s, err := Listen(path, ServerOptions{})
	require.NoError(t, err, "stale socket file is recovered")
	defer s.Close()
}

func TestServerSocketIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix perms")
	}
	_, path := listenBridge(t)
	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestServerCloseIsIdempotentAndUnlinks(t *testing.T) {
	path := testSocket(t)
	s, err := Listen(path, ServerOptions{})
	require.NoError(t, err)
	f := dialFakeHost(t, path)
	_ = f // a live conn must not wedge Close
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "socket unlinked")
}

func TestServerActivateAfterClose(t *testing.T) {
	path := testSocket(t)
	s, err := Listen(path, ServerOptions{})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.ErrorIs(t, s.Activate(1, 1, 1), ErrNotConnected)
	s.KickRefresh() // must not panic or spawn anything
}

func TestTokenForServerTabs(t *testing.T) {
	// The token round-trips a server-reported tab identity.
	tab := Tab{Conn: 3, ID: 17, WindowID: 4}
	conn, id, win, err := ParseToken(Token(tab.Conn, tab.ID, tab.WindowID))
	require.NoError(t, err)
	require.Equal(t, tab.Conn, conn)
	require.Equal(t, tab.ID, id)
	require.Equal(t, tab.WindowID, win)
}

func TestServerRequestIDsAreUniqueAcrossConns(t *testing.T) {
	s, path := listenBridge(t)
	f1 := dialFakeHost(t, path)
	f2 := dialFakeHost(t, path)
	// Answer both on-connect lists and capture the request ids.
	ids := map[int64]bool{}
	for _, f := range []*fakeHost{f1, f2} {
		require.NoError(t, f.conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		line, err := f.rd.ReadString('\n')
		require.NoError(t, err)
		var req request
		require.NoError(t, json.Unmarshal([]byte(line), &req))
		require.False(t, ids[req.ID], "request ids must never collide")
		ids[req.ID] = true
		f.reply(map[string]any{"id": req.ID, "ok": true, "tabs": []map[string]any{}})
	}
	_ = s
}

func TestServerListenErrorsOnUnusablePath(t *testing.T) {
	_, err := Listen(filepath.Join(testSocket(t), "sub", "s.sock"), ServerOptions{})
	require.Error(t, err)
}

func TestServerLogfNilIsSafe(t *testing.T) {
	path := testSocket(t)
	s, err := Listen(path, ServerOptions{}) // nil Logf
	require.NoError(t, err)
	f := dialFakeHost(t, path)
	f.serveOne()
	require.NoError(t, s.Close())
}

func TestServerManyTabsFitTheLineCap(t *testing.T) {
	s, path := listenBridge(t)
	f := dialFakeHost(t, path)
	rows := make([]map[string]any, 500)
	for i := range rows {
		rows[i] = wireTabRow(int64(i+1), 1,
			fmt.Sprintf("Tab %d", i), fmt.Sprintf("https://example.com/%d", i))
	}
	f.tabs = rows
	f.serveOne()
	awaitTabs(t, s, 500)
}
