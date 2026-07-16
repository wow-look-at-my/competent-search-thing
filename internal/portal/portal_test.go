package portal

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

func TestDialAndAvailable(t *testing.T) {
	addr := spawnBus(t)
	newFakePortal(t, addr, fakeConfig{})
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	conn, err := Dial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	version, err := Available(conn)
	require.NoError(t, err)
	require.Equal(t, uint32(2), version)
}

func TestAvailableReturnsVersionOne(t *testing.T) {
	addr := spawnBus(t)
	newFakePortal(t, addr, fakeConfig{version: 1})
	conn := connectBus(t, addr)

	version, err := Available(conn)
	require.NoError(t, err)
	require.Equal(t, uint32(1), version)
}

func TestAvailableNoPortal(t *testing.T) {
	addr := spawnBus(t)
	conn := connectBus(t, addr) // nothing owns the portal name on this bus

	_, err := Available(conn)
	require.ErrorIs(t, err, ErrNoPortal)
	require.NotErrorIs(t, err, ErrNoGlobalShortcuts)
}

func TestAvailableNoGlobalShortcuts(t *testing.T) {
	addr := spawnBus(t)
	newFakePortal(t, addr, fakeConfig{noVersion: true})
	conn := connectBus(t, addr)

	_, err := Available(conn)
	require.ErrorIs(t, err, ErrNoGlobalShortcuts)
	require.NotErrorIs(t, err, ErrNoPortal)
}

func TestAvailableClosedConn(t *testing.T) {
	addr := spawnBus(t)
	conn := connectBus(t, addr)
	require.NoError(t, conn.Close())

	_, err := Available(conn)
	require.Error(t, err)
}

func TestSenderElementAndRequestPath(t *testing.T) {
	require.Equal(t, "1_42", senderElement(":1.42"))
	require.Equal(t, "1_0", senderElement(":1.0"))
	require.Equal(t,
		dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_42/tok"),
		requestPath("1_42", "tok"))
}

func TestNewToken(t *testing.T) {
	a, err := newToken()
	require.NoError(t, err)
	b, err := newToken()
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	require.Len(t, a, len("cst")+16)
	require.Regexp(t, "^cst[0-9a-f]{16}$", a)
}

func TestSessionHandleFrom(t *testing.T) {
	h, err := sessionHandleFrom(map[string]dbus.Variant{
		"session_handle": dbus.MakeVariant("/s/p"),
	})
	require.NoError(t, err)
	require.Equal(t, "/s/p", h)

	h, err = sessionHandleFrom(map[string]dbus.Variant{
		"session_handle": dbus.MakeVariant(dbus.ObjectPath("/s/o")),
	})
	require.NoError(t, err)
	require.Equal(t, "/s/o", h)

	_, err = sessionHandleFrom(map[string]dbus.Variant{})
	require.Error(t, err)

	_, err = sessionHandleFrom(map[string]dbus.Variant{
		"session_handle": dbus.MakeVariant(uint32(7)),
	})
	require.Error(t, err)
}

func TestParseResponseMalformed(t *testing.T) {
	_, _, err := parseResponse(&dbus.Signal{Body: []interface{}{}})
	require.Error(t, err)

	_, _, err = parseResponse(&dbus.Signal{Body: []interface{}{"x", map[string]dbus.Variant{}}})
	require.Error(t, err)

	_, _, err = parseResponse(&dbus.Signal{Body: []interface{}{uint32(0), "x"}})
	require.Error(t, err)

	code, results, err := parseResponse(&dbus.Signal{Body: []interface{}{uint32(1), map[string]dbus.Variant{}}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), code)
	require.Empty(t, results)
}

func TestShortcutsFromMalformed(t *testing.T) {
	require.Nil(t, shortcutsFrom(map[string]dbus.Variant{}))
	require.Nil(t, shortcutsFrom(map[string]dbus.Variant{
		"shortcuts": dbus.MakeVariant("not an array"),
	}))
}

func TestFindShortcut(t *testing.T) {
	scs := []shortcut{
		{ID: "other", Data: map[string]dbus.Variant{"trigger_description": dbus.MakeVariant("X")}},
		{ID: "toggle", Data: map[string]dbus.Variant{"trigger_description": dbus.MakeVariant("Alt+Space")}},
		{ID: "bare", Data: map[string]dbus.Variant{}},
	}
	found, desc := findShortcut(scs, "toggle")
	require.True(t, found)
	require.Equal(t, "Alt+Space", desc)

	found, desc = findShortcut(scs, "bare")
	require.True(t, found)
	require.Equal(t, "", desc)

	found, desc = findShortcut(scs, "missing")
	require.False(t, found)
	require.Equal(t, "", desc)
}
