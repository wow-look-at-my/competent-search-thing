package portal

import (
	"bufio"
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
// exact PID we spawned -- never by pattern matching.
func spawnBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed; skipping portal bus test")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The daemon prints "unix:path=...,guid=...\n" once it listens.
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

// fakeConfig scripts one fakePortal. The zero value is the happy path:
// GlobalShortcuts version 2, empty ListShortcuts, every request
// answered with response code 0.
type fakeConfig struct {
	version       uint32     // GlobalShortcuts version property (0 = default 2)
	noVersion     bool       // Properties.Get errors: portal without the backend
	createCode    uint32     // CreateSession response code
	listCode      uint32     // ListShortcuts response code
	bindCode      uint32     // BindShortcuts response code (1 = user denied)
	listShortcuts []shortcut // ListShortcuts response payload
	triggerDesc   string     // trigger_description in bind results ("" = "Alt+Space")
	muteCreate    bool       // never answer CreateSession (ctx-cancel tests)
	mangleRequest bool       // return + respond on a request path that differs from the prediction
}

// fakePortal is a scriptable in-process stand-in for
// xdg-desktop-portal's GlobalShortcuts frontend. It owns
// org.freedesktop.portal.Desktop on a private test bus, computes
// request/session paths with the same sender+token rule real portals
// use, and emits Request.Response from a goroutine after the method
// returned, mirroring the spec sequence. All state is guarded by mu
// because godbus runs every exported method on a fresh goroutine.
type fakePortal struct {
	t    *testing.T
	conn *dbus.Conn

	mu  sync.Mutex
	cfg fakeConfig

	bindCalls       int
	lastBind        []shortcut
	lastParent      string
	lastBindSession string
	lastListSession string
	sessions        []string
	sessionCloses   int
}

func newFakePortal(t *testing.T, addr string, cfg fakeConfig) *fakePortal {
	t.Helper()
	if cfg.version == 0 && !cfg.noVersion {
		cfg.version = 2
	}
	if cfg.triggerDesc == "" {
		cfg.triggerDesc = "Alt+Space"
	}
	f := &fakePortal{t: t, conn: connectBus(t, addr)}
	f.mu.Lock()
	f.cfg = cfg
	f.mu.Unlock()
	require.NoError(t, f.conn.Export(shortcutsHandler{f}, desktopPath, shortcutsIface))
	require.NoError(t, f.conn.Export(propsHandler{f}, desktopPath, "org.freedesktop.DBus.Properties"))
	reply, err := f.conn.RequestName(portalBusName, dbus.NameFlagDoNotQueue)
	require.NoError(t, err)
	require.Equal(t, dbus.RequestNameReplyPrimaryOwner, reply)
	return f
}

// setupPortalTest is the common per-test rig: private bus, fake portal
// service, and a separate client connection for the code under test.
func setupPortalTest(t *testing.T, cfg fakeConfig) (*fakePortal, *dbus.Conn) {
	t.Helper()
	addr := spawnBus(t)
	return newFakePortal(t, addr, cfg), connectBus(t, addr)
}

// senderElem mirrors the portal's SENDER path-element rule,
// independently of the client helper under test.
func senderElem(sender string) string {
	return strings.ReplaceAll(strings.TrimPrefix(sender, ":"), ".", "_")
}

// stringOption reads a string value out of a vardict, "" when absent.
func stringOption(options map[string]dbus.Variant, key string) string {
	v, ok := options[key]
	if !ok {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}

// replyPathLocked computes the request handle to return (and respond
// on): the spec prediction, or a deliberately different path when the
// config exercises the client's fallback subscription.
func (f *fakePortal) replyPathLocked(sender string, options map[string]dbus.Variant) dbus.ObjectPath {
	p := requestPath(senderElem(sender), stringOption(options, "handle_token"))
	if f.cfg.mangleRequest {
		p = dbus.ObjectPath(string(p) + "_srv")
	}
	return p
}

// respond emits Request.Response from a goroutine shortly after the
// in-flight method returns, like a real portal. Errors are ignored on
// purpose: the emit races test teardown in cancel-style tests.
func (f *fakePortal) respond(path dbus.ObjectPath, code uint32, results map[string]dbus.Variant) {
	conn := f.conn
	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = conn.Emit(path, signalResponse, code, results)
	}()
}

// EmitActivated broadcasts GlobalShortcuts.Activated for a session
// handle + shortcut id, with the spec body shape (o s t a{sv}).
func (f *fakePortal) EmitActivated(sessionHandle, shortcutID string) {
	f.t.Helper()
	require.NoError(f.t, f.conn.Emit(desktopPath, signalActivated,
		dbus.ObjectPath(sessionHandle), shortcutID, uint64(1234), map[string]dbus.Variant{}))
}

// EmitDeactivated broadcasts GlobalShortcuts.Deactivated, which
// clients must ignore.
func (f *fakePortal) EmitDeactivated(sessionHandle, shortcutID string) {
	f.t.Helper()
	require.NoError(f.t, f.conn.Emit(desktopPath, shortcutsIface+".Deactivated",
		dbus.ObjectPath(sessionHandle), shortcutID, uint64(1234), map[string]dbus.Variant{}))
}

// Recorded-state accessors, all mutex-guarded.

func (f *fakePortal) BindCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bindCalls
}

func (f *fakePortal) LastBind() []shortcut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]shortcut(nil), f.lastBind...)
}

func (f *fakePortal) LastParent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastParent
}

func (f *fakePortal) LastBindSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBindSession
}

func (f *fakePortal) LastListSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastListSession
}

func (f *fakePortal) Sessions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sessions...)
}

func (f *fakePortal) SessionCloses() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionCloses
}

// shortcutsHandler implements org.freedesktop.portal.GlobalShortcuts.
type shortcutsHandler struct{ f *fakePortal }

func (h shortcutsHandler) CreateSession(sender dbus.Sender, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := h.f
	f.mu.Lock()
	defer f.mu.Unlock()
	ret := f.replyPathLocked(string(sender), options)
	sess := "/org/freedesktop/portal/desktop/session/" +
		senderElem(string(sender)) + "/" + stringOption(options, "session_handle_token")
	f.sessions = append(f.sessions, sess)
	// Export a Session object at the handle so the client's
	// Session.Close reaches a real Close method.
	_ = f.conn.Export(sessionHandler{f}, dbus.ObjectPath(sess), sessionIface)
	if !f.cfg.muteCreate {
		f.respond(ret, f.cfg.createCode, map[string]dbus.Variant{
			// Typed "s" on purpose: the documented portal erratum.
			"session_handle": dbus.MakeVariant(sess),
		})
	}
	return ret, nil
}

func (h shortcutsHandler) ListShortcuts(sender dbus.Sender, sessionHandle dbus.ObjectPath, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := h.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastListSession = string(sessionHandle)
	ret := f.replyPathLocked(string(sender), options)
	f.respond(ret, f.cfg.listCode, map[string]dbus.Variant{
		"shortcuts": dbus.MakeVariant(append([]shortcut{}, f.cfg.listShortcuts...)),
	})
	return ret, nil
}

func (h shortcutsHandler) BindShortcuts(sender dbus.Sender, sessionHandle dbus.ObjectPath, shortcuts []shortcut, parentWindow string, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := h.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindCalls++
	f.lastBind = append([]shortcut(nil), shortcuts...)
	f.lastParent = parentWindow
	f.lastBindSession = string(sessionHandle)
	ret := f.replyPathLocked(string(sender), options)
	results := map[string]dbus.Variant{}
	if f.cfg.bindCode == 0 {
		echoed := make([]shortcut, 0, len(shortcuts))
		for _, sc := range shortcuts {
			data := map[string]dbus.Variant{
				"trigger_description": dbus.MakeVariant(f.cfg.triggerDesc),
			}
			if v, ok := sc.Data["description"]; ok {
				data["description"] = v
			}
			echoed = append(echoed, shortcut{ID: sc.ID, Data: data})
		}
		results["shortcuts"] = dbus.MakeVariant(echoed)
	}
	f.respond(ret, f.cfg.bindCode, results)
	return ret, nil
}

// sessionHandler implements org.freedesktop.portal.Session at each
// session path the fake hands out.
type sessionHandler struct{ f *fakePortal }

func (h sessionHandler) Close() *dbus.Error {
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	h.f.sessionCloses++
	return nil
}

// propsHandler implements org.freedesktop.DBus.Properties.Get for the
// GlobalShortcuts version property.
type propsHandler struct{ f *fakePortal }

func (h propsHandler) Get(iface, prop string) (dbus.Variant, *dbus.Error) {
	f := h.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if iface == shortcutsIface && prop == "version" && !f.cfg.noVersion {
		return dbus.MakeVariant(f.cfg.version), nil
	}
	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
}
