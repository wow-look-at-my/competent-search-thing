package tray

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// appMenu builds the menu shape the app ships (4 actions + separator)
// with counting callbacks, and returns the counters keyed by label.
func appMenu() ([]MenuItem, map[string]*counter) {
	counters := map[string]*counter{
		"Show/Hide":   newCounter(),
		"Rescan now":  newCounter(),
		"Open config": newCounter(),
		"Quit":        newCounter(),
	}
	items := []MenuItem{
		{Label: "Show/Hide", OnClick: counters["Show/Hide"].inc},
		{Label: "Rescan now", OnClick: counters["Rescan now"].inc},
		{Label: "Open config", OnClick: counters["Open config"].inc},
		{Separator: true},
		{Label: "Quit", OnClick: counters["Quit"].inc},
	}
	return items, counters
}

// menuObj returns the dbusmenu bus object for a started rig.
func (rig *trayRig) menuObj() dbus.BusObject {
	return rig.host.Object(rig.dest, menuPath)
}

// storeLayout unpacks a GetLayout reply.
func storeLayout(t *testing.T, call *dbus.Call) (uint32, layoutNode) {
	t.Helper()
	require.NoError(t, call.Err)
	var revision uint32
	var root layoutNode
	require.NoError(t, call.Store(&revision, &root))
	return revision, root
}

// childNodes unpacks the av children of a layout node.
func childNodes(t *testing.T, n layoutNode) []layoutNode {
	t.Helper()
	out := make([]layoutNode, 0, len(n.Children))
	for _, c := range n.Children {
		var child layoutNode
		require.NoError(t, dbus.Store([]interface{}{c.Value()}, &child))
		out = append(out, child)
	}
	return out
}

func propStr(t *testing.T, props map[string]dbus.Variant, name string) string {
	t.Helper()
	v, ok := props[name]
	if !ok {
		return ""
	}
	s, ok := v.Value().(string)
	require.True(t, ok, "menu property %s is %T", name, v.Value())
	return s
}

// TestGetLayoutAsTheExtensionCallsIt drives the exact call the GNOME
// extension makes on attach: GetLayout(0, -1, ["type",
// "children-display"]).
func TestGetLayoutAsTheExtensionCallsIt(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	revision, root := storeLayout(t, rig.menuObj().Call(
		menuIface+".GetLayout", 0, int32(0), int32(-1), []string{"type", "children-display"}))
	require.Equal(t, layoutRevision, revision)
	require.Equal(t, int32(0), root.ID)
	require.Equal(t, "submenu", propStr(t, root.Props, "children-display"))
	require.NotContains(t, root.Props, "label", "the property filter is honored")

	children := childNodes(t, root)
	require.Len(t, children, 5)
	for i, c := range children {
		require.Equal(t, int32(i+1), c.ID, "stable sequential ids")
		require.Empty(t, c.Children)
	}
	require.Equal(t, "separator", propStr(t, children[3].Props, "type"))
	require.Equal(t, "standard", propStr(t, children[0].Props, "type"))
	require.NotContains(t, children[0].Props, "label", "filter applies to children too")
}

func TestGetLayoutUnfilteredAndSubtrees(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	// No filter: full properties, labels included.
	_, root := storeLayout(t, rig.menuObj().Call(
		menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}))
	children := childNodes(t, root)
	require.Len(t, children, 5)
	wantLabels := []string{"Show/Hide", "Rescan now", "Open config", "", "Quit"}
	for i, c := range children {
		require.Equal(t, wantLabels[i], propStr(t, c.Props, "label"))
	}

	// Depth 0: the root arrives childless.
	_, root = storeLayout(t, rig.menuObj().Call(
		menuIface+".GetLayout", 0, int32(0), int32(0), []string{}))
	require.Empty(t, root.Children)

	// A leaf as the parent: its own node, no children.
	_, leaf := storeLayout(t, rig.menuObj().Call(
		menuIface+".GetLayout", 0, int32(2), int32(-1), []string{}))
	require.Equal(t, int32(2), leaf.ID)
	require.Equal(t, "Rescan now", propStr(t, leaf.Props, "label"))
	require.Empty(t, leaf.Children)

	// Unknown parent: a D-Bus error, not a crash.
	require.Error(t, rig.menuObj().Call(
		menuIface+".GetLayout", 0, int32(99), int32(-1), []string{}).Err)
}

func TestGetGroupProperties(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	// Explicit ids (what the extension sends), empty filter = all
	// properties.
	var got []idProps
	require.NoError(t, rig.menuObj().Call(
		menuIface+".GetGroupProperties", 0, []int32{1, 4, 5}, []string{}).Store(&got))
	require.Len(t, got, 3)
	require.Equal(t, int32(1), got[0].ID)
	require.Equal(t, "Show/Hide", propStr(t, got[0].Props, "label"))
	require.Equal(t, "separator", propStr(t, got[1].Props, "type"))
	require.Equal(t, "Quit", propStr(t, got[2].Props, "label"))

	enabled, ok := got[0].Props["enabled"].Value().(bool)
	require.True(t, ok)
	require.True(t, enabled)
	visible, ok := got[0].Props["visible"].Value().(bool)
	require.True(t, ok)
	require.True(t, visible)

	// A property filter narrows the maps.
	require.NoError(t, rig.menuObj().Call(
		menuIface+".GetGroupProperties", 0, []int32{1}, []string{"label"}).Store(&got))
	require.Len(t, got, 1)
	require.Equal(t, map[string]dbus.Variant{"label": dbus.MakeVariant("Show/Hide")}, got[0].Props)

	// Empty ids = every node (dbusmenu spec); unknown ids are skipped.
	require.NoError(t, rig.menuObj().Call(
		menuIface+".GetGroupProperties", 0, []int32{}, []string{}).Store(&got))
	require.Len(t, got, 6, "root + 5 items")
	require.NoError(t, rig.menuObj().Call(
		menuIface+".GetGroupProperties", 0, []int32{2, 99}, []string{}).Store(&got))
	require.Len(t, got, 1)
}

func TestGetProperty(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	var v dbus.Variant
	require.NoError(t, rig.menuObj().Call(menuIface+".GetProperty", 0, int32(5), "label").Store(&v))
	require.Equal(t, "Quit", v.Value())

	require.Error(t, rig.menuObj().Call(menuIface+".GetProperty", 0, int32(99), "label").Err, "unknown id")
	require.Error(t, rig.menuObj().Call(menuIface+".GetProperty", 0, int32(5), "nope").Err, "unknown property")
}

// TestEventClickedFiresEachCallback sends the exact Event the
// extension sends on activation: (id, "clicked", variant(int32 0),
// timestamp).
func TestEventClickedFiresEachCallback(t *testing.T) {
	items, counters := appMenu()
	rig := startedTray(t, Options{Menu: items})

	click := func(id int32) *dbus.Call {
		return rig.menuObj().Call(menuIface+".Event", 0,
			id, eventClicked, dbus.MakeVariant(int32(0)), uint32(0))
	}

	for id, label := range map[int32]string{1: "Show/Hide", 2: "Rescan now", 3: "Open config", 5: "Quit"} {
		require.NoError(t, click(id).Err)
		counters[label].await(t)
		require.Equal(t, 1, counters[label].count(), "%s fired once", label)
	}

	// The separator accepts the event and does nothing; unknown ids
	// error; open/close chatter is ignored.
	require.NoError(t, click(4).Err)
	require.Error(t, click(99).Err)
	require.NoError(t, rig.menuObj().Call(menuIface+".Event", 0,
		int32(0), "opened", dbus.MakeVariant(int32(0)), uint32(0)).Err)
	require.NoError(t, rig.menuObj().Call(menuIface+".Event", 0,
		int32(1), "hovered", dbus.MakeVariant(int32(0)), uint32(0)).Err)
	require.Error(t, rig.menuObj().Call(menuIface+".Event", 0,
		int32(99), "opened", dbus.MakeVariant(int32(0)), uint32(0)).Err)

	for label, c := range counters {
		want := 1
		require.Equal(t, want, c.count(), "%s total", label)
	}
}

func TestEventGroup(t *testing.T) {
	items, counters := appMenu()
	rig := startedTray(t, Options{Menu: items})

	events := []menuEvent{
		{ID: 1, EventID: eventClicked, Data: dbus.MakeVariant(int32(0)), Timestamp: 0},
		{ID: 99, EventID: eventClicked, Data: dbus.MakeVariant(int32(0)), Timestamp: 0},
		{ID: 3, EventID: "opened", Data: dbus.MakeVariant(int32(0)), Timestamp: 0},
	}
	var idErrors []int32
	require.NoError(t, rig.menuObj().Call(menuIface+".EventGroup", 0, events).Store(&idErrors))
	require.Equal(t, []int32{99}, idErrors)
	counters["Show/Hide"].await(t)
	require.Equal(t, 1, counters["Show/Hide"].count())
	require.Equal(t, 0, counters["Open config"].count(), "'opened' is not a click")
}

func TestAboutToShow(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	var needUpdate bool
	require.NoError(t, rig.menuObj().Call(menuIface+".AboutToShow", 0, int32(0)).Store(&needUpdate))
	require.False(t, needUpdate, "static menu: never a pending update")
	require.Error(t, rig.menuObj().Call(menuIface+".AboutToShow", 0, int32(99)).Err)

	var updatesNeeded, idErrors []int32
	require.NoError(t, rig.menuObj().Call(
		menuIface+".AboutToShowGroup", 0, []int32{0, 1, 99}).Store(&updatesNeeded, &idErrors))
	require.Empty(t, updatesNeeded)
	require.Equal(t, []int32{99}, idErrors)
}

func TestMenuProperties(t *testing.T) {
	items, _ := appMenu()
	rig := startedTray(t, Options{Menu: items})

	props := rig.getAll(t, menuPath, menuIface)
	version, ok := props["Version"].Value().(uint32)
	require.True(t, ok)
	require.Equal(t, dbusmenuVersion, version)
	require.Equal(t, "normal", propStr(t, props, "Status"))
	require.Equal(t, "ltr", propStr(t, props, "TextDirection"))
	themePath, ok := props["IconThemePath"].Value().([]string)
	require.True(t, ok)
	require.Empty(t, themePath)
}

// Pure model tests (no bus), for the corners host calls do not reach.
func TestMenuModel(t *testing.T) {
	m := newMenu([]MenuItem{{Label: "A"}, {Separator: true}}, func(string, ...interface{}) {})

	require.Nil(t, m.node(-1))
	require.Nil(t, m.node(3))
	require.NotNil(t, m.node(0))

	require.False(t, m.click(-5))
	require.True(t, m.click(0), "clicking the root is valid and does nothing")
	require.True(t, m.click(2), "clicking a separator is valid and does nothing")

	// Root properties carry children-display only.
	rootProps := m.properties(m.node(0), nil)
	require.Equal(t, "submenu", propStrLoose(rootProps, "children-display"))
	require.NotContains(t, rootProps, "label")

	// Filters keep unknown names out without erroring.
	filtered := m.properties(m.node(1), []string{"label", "bogus"})
	require.Len(t, filtered, 1)
	require.Equal(t, "A", propStrLoose(filtered, "label"))
}

func propStrLoose(props map[string]dbus.Variant, name string) string {
	v, ok := props[name]
	if !ok {
		return ""
	}
	s, _ := v.Value().(string)
	return s
}
