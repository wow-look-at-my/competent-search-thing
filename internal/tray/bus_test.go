package tray

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// spawnBus starts a throwaway private dbus-daemon for one test and
// returns its address. The daemon is stopped in cleanup by killing the
// exact PID we spawned -- never by pattern matching. Same rig as
// internal/portal's tests.
func spawnBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed; skipping tray bus test")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	type read struct {
		line string
		err  error
	}
	lineCh := make(chan read, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		lineCh <- read{line: line, err: err}
	}()
	select {
	case r := <-lineCh:
		require.NoError(t, r.err)
		addr := strings.TrimSpace(r.line)
		require.NotEmpty(t, addr)
		return addr
	case <-time.After(10 * time.Second):
		t.Fatal("dbus-daemon did not print an address in time")
		return ""
	}
}

// connectBus opens a connection to the test bus, closed in cleanup.
func connectBus(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// registration records one RegisterStatusNotifierItem call.
type registration struct {
	service string
	sender  string
}

// fakeWatcher owns org.kde.StatusNotifierWatcher on the test bus and
// records item registrations, feeding each into a buffered channel so
// tests can await them. Own/Release let re-registration tests bounce
// the name ownership like a GNOME Shell restart does.
type fakeWatcher struct {
	t    *testing.T
	conn *dbus.Conn

	mu   sync.Mutex
	regs []registration
	ch   chan registration
}

// newFakeWatcher connects and owns the watcher name.
func newFakeWatcher(t *testing.T, addr string) *fakeWatcher {
	t.Helper()
	f := &fakeWatcher{t: t, conn: connectBus(t, addr), ch: make(chan registration, 16)}
	require.NoError(t, f.conn.Export(watcherHandler{f}, watcherPath, watcherIface))
	f.Own()
	return f
}

// Own acquires the watcher well-known name.
func (f *fakeWatcher) Own() {
	f.t.Helper()
	reply, err := f.conn.RequestName(watcherBusName, dbus.NameFlagDoNotQueue)
	require.NoError(f.t, err)
	require.Equal(f.t, dbus.RequestNameReplyPrimaryOwner, reply)
}

// Release drops the watcher well-known name (the connection and the
// exported object stay).
func (f *fakeWatcher) Release() {
	f.t.Helper()
	reply, err := f.conn.ReleaseName(watcherBusName)
	require.NoError(f.t, err)
	require.Equal(f.t, dbus.ReleaseNameReplyReleased, reply)
}

// Await returns the next registration, failing the test after
// timeout.
func (f *fakeWatcher) Await(timeout time.Duration) registration {
	f.t.Helper()
	select {
	case r := <-f.ch:
		return r
	case <-time.After(timeout):
		f.t.Fatal("no RegisterStatusNotifierItem call arrived in time")
		return registration{}
	}
}

// AwaitNone asserts that no registration arrives within d.
func (f *fakeWatcher) AwaitNone(d time.Duration) {
	f.t.Helper()
	select {
	case r := <-f.ch:
		f.t.Fatalf("unexpected registration %+v", r)
	case <-time.After(d):
	}
}

// Registrations returns a copy of everything recorded so far.
func (f *fakeWatcher) Registrations() []registration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]registration(nil), f.regs...)
}

// watcherHandler implements org.kde.StatusNotifierWatcher.
type watcherHandler struct{ f *fakeWatcher }

func (h watcherHandler) RegisterStatusNotifierItem(sender dbus.Sender, service string) *dbus.Error {
	reg := registration{service: service, sender: string(sender)}
	h.f.mu.Lock()
	h.f.regs = append(h.f.regs, reg)
	h.f.mu.Unlock()
	h.f.ch <- reg
	return nil
}

// setupTray builds a Tray whose dial lands on the test bus, with a
// recording log; Close runs in cleanup (idempotence is tested
// explicitly too).
func setupTray(t *testing.T, addr string, opts Options) (*Tray, *logRecorder) {
	t.Helper()
	rec := &logRecorder{}
	opts.Logf = rec.logf
	if opts.ID == "" {
		opts.ID = "competent-search-thing"
	}
	if opts.Title == "" {
		opts.Title = "Competent Search"
	}
	tr := New(opts)
	tr.dial = func() (*dbus.Conn, error) {
		conn, err := dbus.Connect(addr)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr, rec
}

// logRecorder captures the tray's log lines for assertions.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, v ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, v...))
}

func (r *logRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func (r *logRecorder) containing(substr string) int {
	n := 0
	for _, l := range r.all() {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}
