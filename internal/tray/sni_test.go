package tray

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// trayRig is one started Tray plus everything a "GNOME side"
// assertion needs: the bus address, the fake watcher, a host-side
// connection, and the tray's unique name.
type trayRig struct {
	addr    string
	tray    *Tray
	watcher *fakeWatcher
	host    *dbus.Conn
	dest    string
	rec     *logRecorder
}

// startedTray brings a Tray up on a fresh bus with a fake watcher and
// awaits its registration.
func startedTray(t *testing.T, opts Options) *trayRig {
	t.Helper()
	addr := spawnBus(t)
	w := newFakeWatcher(t, addr)
	tr, rec := setupTray(t, addr, opts)
	require.NoError(t, tr.Start(context.Background()))
	w.Await(awaitTimeout)
	return &trayRig{
		addr:    addr,
		tray:    tr,
		watcher: w,
		host:    connectBus(t, addr),
		dest:    trayUniqueName(t, tr),
		rec:     rec,
	}
}

// getAll reads org.freedesktop.DBus.Properties.GetAll(iface) from the
// host side -- the exact call the AppIndicator extension's GDBusProxy
// makes when it attaches.
func (rig *trayRig) getAll(t *testing.T, path dbus.ObjectPath, iface string) map[string]dbus.Variant {
	t.Helper()
	var props map[string]dbus.Variant
	require.NoError(t, rig.host.Object(rig.dest, path).Call("org.freedesktop.DBus.Properties.GetAll", 0, iface).Store(&props))
	return props
}

func str(t *testing.T, props map[string]dbus.Variant, name string) string {
	t.Helper()
	v, ok := props[name]
	require.True(t, ok, "property %s missing", name)
	s, ok := v.Value().(string)
	require.True(t, ok, "property %s is %T, want string", name, v.Value())
	return s
}

func TestItemPropertiesGetAll(t *testing.T) {
	rig := startedTray(t, Options{
		ID:      "competent-search-thing",
		Title:   "Competent Search",
		Tooltip: func() string { return "alt+space summons the searchbar" },
	})
	props := rig.getAll(t, itemPath, itemIface)

	require.Equal(t, "ApplicationStatus", str(t, props, "Category"))
	require.Equal(t, "competent-search-thing", str(t, props, "Id"))
	require.Equal(t, "Competent Search", str(t, props, "Title"))
	require.Equal(t, "Active", str(t, props, "Status"))
	require.Equal(t, "", str(t, props, "IconName"), "pixmap-only: no theme icon name")

	menuProp, ok := props["Menu"].Value().(dbus.ObjectPath)
	require.True(t, ok, "Menu is %T, want ObjectPath", props["Menu"].Value())
	require.Equal(t, menuPath, menuProp)

	itemIsMenu, ok := props["ItemIsMenu"].Value().(bool)
	require.True(t, ok)
	require.False(t, itemIsMenu)

	windowID, ok := props["WindowId"].Value().(int32)
	require.True(t, ok)
	require.Zero(t, windowID)

	// The pixmaps arrive as a(iiay) with the drawn sizes and plausible
	// ARGB payloads.
	var pixmaps []pixmap
	require.NoError(t, dbus.Store([]interface{}{props["IconPixmap"].Value()}, &pixmaps))
	require.Len(t, pixmaps, len(iconSizes))
	for i, p := range pixmaps {
		require.Equal(t, int32(iconSizes[i]), p.Width)
		require.Equal(t, int32(iconSizes[i]), p.Height)
		require.Len(t, p.Data, iconSizes[i]*iconSizes[i]*4)
		require.Positive(t, opaquePixels(p), "icon %dpx has visible pixels", p.Width)
	}

	// ToolTip carries the title and the summon hint.
	var tt tooltip
	require.NoError(t, dbus.Store([]interface{}{props["ToolTip"].Value()}, &tt))
	require.Equal(t, "Competent Search", tt.Title)
	require.Equal(t, "alt+space summons the searchbar", tt.Text)
}

func TestTooltipRefreshOnReregistration(t *testing.T) {
	text := "one"
	rig := startedTray(t, Options{Tooltip: func() string { return text }})

	props := rig.getAll(t, itemPath, itemIface)
	var tt tooltip
	require.NoError(t, dbus.Store([]interface{}{props["ToolTip"].Value()}, &tt))
	require.Equal(t, "one", tt.Text)

	// Watch for NewToolTip on the host side.
	require.NoError(t, rig.host.AddMatchSignal(
		dbus.WithMatchInterface(itemIface),
		dbus.WithMatchMember("NewToolTip"),
	))
	sigCh := make(chan *dbus.Signal, 8)
	rig.host.Signal(sigCh)

	// A watcher restart re-registers; the re-registration re-reads the
	// tooltip (a portal shortcut may have bound long after startup).
	text = "two"
	rig.watcher.Release()
	w2 := newFakeWatcher(t, rig.addr)
	w2.Await(awaitTimeout)

	select {
	case sig := <-sigCh:
		require.Equal(t, itemIface+".NewToolTip", sig.Name)
	case <-time.After(awaitTimeout):
		t.Fatal("no NewToolTip signal after the tooltip changed")
	}
	props = rig.getAll(t, itemPath, itemIface)
	require.NoError(t, dbus.Store([]interface{}{props["ToolTip"].Value()}, &tt))
	require.Equal(t, "two", tt.Text)
	require.Equal(t, "Competent Search", tt.Title, "the title survives tooltip refreshes")

	// An unchanged tooltip on the next re-registration stays silent.
	w2.Release()
	w3 := newFakeWatcher(t, rig.addr)
	w3.Await(awaitTimeout)
	select {
	case sig := <-sigCh:
		t.Fatalf("unexpected %s for an unchanged tooltip", sig.Name)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestIntrospectionTree(t *testing.T) {
	rig := startedTray(t, Options{})

	introspect := func(path dbus.ObjectPath) string {
		var xml string
		require.NoError(t, rig.host.Object(rig.dest, path).Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml))
		return xml
	}

	root := introspect("/")
	require.Contains(t, root, `node name="StatusNotifierItem"`,
		"the root introspection names the item child (the extension's brute-force scan walks this)")
	require.Contains(t, root, `node name="MenuBar"`)

	item := introspect(itemPath)
	require.Contains(t, item, itemIface)
	require.Contains(t, item, "Activate")
	require.Contains(t, item, "IconPixmap")
	require.Contains(t, item, "NewToolTip")

	menu := introspect(menuPath)
	require.Contains(t, menu, menuIface)
	for _, m := range []string{"GetLayout", "GetGroupProperties", "GetProperty", "Event", "EventGroup", "AboutToShow", "AboutToShowGroup", "LayoutUpdated", "ItemsPropertiesUpdated"} {
		require.Contains(t, menu, m)
	}
}

func TestWellKnownNameOwned(t *testing.T) {
	rig := startedTray(t, Options{})
	rig.tray.mu.Lock()
	names := rig.tray.conn.Names()
	rig.tray.mu.Unlock()
	found := ""
	for _, n := range names {
		if strings.HasPrefix(n, itemIface+"-") {
			found = n
		}
	}
	require.NotEmpty(t, found, "the KDE convention name org.kde.StatusNotifierItem-<pid>-1 is owned")
	var owned bool
	require.NoError(t, rig.host.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, found).Store(&owned))
	require.True(t, owned)
}
