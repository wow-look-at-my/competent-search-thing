package launch

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

func TestApplicationDBusCall(t *testing.T) {
	cred := Credential{ID: "tok-1", Kind: KindWaylandGDK}
	target := ClassifyTarget("/tmp/a.txt", false)

	t.Run("open with target", func(t *testing.T) {
		h := Handler{DesktopID: "org.gnome.gedit.desktop", DBusActivatable: true}
		call, ok := ApplicationDBusCall(h, &target, cred)
		require.True(t, ok)
		require.Equal(t, "org.gnome.gedit", call.Dest)
		require.Equal(t, "/org/gnome/gedit", call.Path)
		require.Equal(t, "Open", call.Method)
		require.Equal(t, []string{"file:///tmp/a.txt"}, call.URIs)
		require.Equal(t, map[string]string{
			"desktop-startup-id": "tok-1",
			"activation-token":   "tok-1",
		}, call.PlatformData)
	})
	t.Run("activate without target", func(t *testing.T) {
		h := Handler{DesktopID: "org.gnome.Nautilus.desktop", DBusActivatable: true}
		call, ok := ApplicationDBusCall(h, nil, Credential{})
		require.True(t, ok)
		require.Equal(t, "Activate", call.Method)
		require.Nil(t, call.URIs)
		require.NotNil(t, call.PlatformData, "platform-data dict is present even without a credential")
		require.Empty(t, call.PlatformData)
	})
	t.Run("hyphens map to underscores in the path", func(t *testing.T) {
		h := Handler{DesktopID: "com.example.my-app.desktop", DBusActivatable: true}
		call, ok := ApplicationDBusCall(h, nil, Credential{})
		require.True(t, ok)
		require.Equal(t, "com.example.my-app", call.Dest)
		require.Equal(t, "/com/example/my_app", call.Path)
	})
	t.Run("not dbus-activatable", func(t *testing.T) {
		_, ok := ApplicationDBusCall(Handler{DesktopID: "org.gnome.gedit.desktop"}, nil, Credential{})
		require.False(t, ok)
	})
	for name, id := range map[string]string{
		"no .desktop suffix":     "org.gnome.gedit",
		"single element":         "gedit.desktop",
		"empty element":          "org..gedit.desktop",
		"digit-leading element":  "org.1gedit.desktop",
		"invalid character":      "org.ged it.desktop",
		"empty id":               "",
		"suffix only":            ".desktop",
		"bad id despite the cap": "code.desktop", // single element after trim
	} {
		t.Run("irreversible id: "+name, func(t *testing.T) {
			_, ok := ApplicationDBusCall(Handler{DesktopID: id, DBusActivatable: true}, nil, Credential{})
			require.False(t, ok, "id %q must not derive a bus name", id)
		})
	}
}

func TestValidBusName(t *testing.T) {
	require.True(t, validBusName("org.gnome.gedit"))
	require.True(t, validBusName("com.example.my-app"))
	require.True(t, validBusName("a._x9"))
	require.False(t, validBusName("org"))
	require.False(t, validBusName(""))
	require.False(t, validBusName("org.9x"))
	require.False(t, validBusName("org."))
	require.False(t, validBusName(strings.Repeat("a.", 200)+"b"))
}

// spawnBus starts a throwaway private dbus-daemon for one test and
// returns its address (the portal/tray test pattern: killed by exact
// PID, never by name).
func spawnBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed; skipping bus test")
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

// fakeApplication records org.freedesktop.Application calls.
type fakeApplication struct {
	mu       sync.Mutex
	opens    [][]string
	data     []map[string]dbus.Variant
	activate int
}

func (f *fakeApplication) Open(uris []string, data map[string]dbus.Variant) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, uris)
	f.data = append(f.data, data)
	return nil
}

func (f *fakeApplication) Activate(data map[string]dbus.Variant) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activate++
	f.data = append(f.data, data)
	return nil
}

// serveApplication exports a fake org.freedesktop.Application at dest
// on the test bus.
func serveApplication(t *testing.T, addr, dest, path string) *fakeApplication {
	t.Helper()
	conn, err := dbus.Connect(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	app := &fakeApplication{}
	require.NoError(t, conn.Export(app, dbus.ObjectPath(path), "org.freedesktop.Application"))
	reply, err := conn.RequestName(dest, 0)
	require.NoError(t, err)
	require.Equal(t, dbus.RequestNameReplyPrimaryOwner, reply)
	return app
}

func TestDBusActivateOpen(t *testing.T) {
	addr := spawnBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	app := serveApplication(t, addr, "org.example.editor", "/org/example/editor")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := DBusActivate(ctx, DBusCall{
		Dest:   "org.example.editor",
		Path:   "/org/example/editor",
		Method: "Open",
		URIs:   []string{"file:///tmp/x.txt"},
		PlatformData: map[string]string{
			"desktop-startup-id": "sid-9",
			"activation-token":   "sid-9",
		},
	})
	require.NoError(t, err)

	app.mu.Lock()
	defer app.mu.Unlock()
	require.Equal(t, [][]string{{"file:///tmp/x.txt"}}, app.opens)
	require.Len(t, app.data, 1)
	require.Equal(t, dbus.MakeVariant("sid-9"), app.data[0]["desktop-startup-id"])
	require.Equal(t, dbus.MakeVariant("sid-9"), app.data[0]["activation-token"])
}

func TestDBusActivateActivate(t *testing.T) {
	addr := spawnBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	app := serveApplication(t, addr, "org.example.tool", "/org/example/tool")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := DBusActivate(ctx, DBusCall{
		Dest:         "org.example.tool",
		Path:         "/org/example/tool",
		Method:       "Activate",
		PlatformData: map[string]string{},
	})
	require.NoError(t, err)
	app.mu.Lock()
	defer app.mu.Unlock()
	require.Equal(t, 1, app.activate)
	require.Empty(t, app.data[0])
}

func TestDBusActivateMissingService(t *testing.T) {
	addr := spawnBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := DBusActivate(ctx, DBusCall{
		Dest:         "org.example.absent",
		Path:         "/org/example/absent",
		Method:       "Activate",
		PlatformData: map[string]string{},
	})
	require.Error(t, err, "an unactivatable name must surface as an error, not hang")
}

func TestDBusActivateBadMethod(t *testing.T) {
	addr := spawnBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	err := DBusActivate(context.Background(), DBusCall{Dest: "a.b", Path: "/a/b", Method: "Explode"})
	require.ErrorContains(t, err, "unsupported application method")
}

func TestDBusActivateNoBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/competent-search-test.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := DBusActivate(ctx, DBusCall{Dest: "a.b", Path: "/a/b", Method: "Activate"})
	require.Error(t, err)
}
