package tray

import (
	"fmt"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// layoutRevision is the com.canonical.dbusmenu layout revision. The
// menu is static, so it never changes; if items ever mutate at
// runtime, bump it and emit LayoutUpdated(revision, parent) so hosts
// re-fetch.
const layoutRevision uint32 = 1

// dbusmenuVersion is the Version property. libdbusmenu -- the
// reference implementation this interface comes from -- reports 3
// (DBUSMENU_VERSION_NUMBER in libdbusmenu-glib/server.c); the GNOME
// extension never checks it.
const dbusmenuVersion uint32 = 3

// eventClicked is the only dbusmenu event id acted on; "opened",
// "closed", and "hovered" arrive too and are deliberately ignored.
const eventClicked = "clicked"

// layoutNode is the (ia{sv}av) wire shape of one GetLayout node: id,
// properties, children (each child packed as a variant holding
// another layoutNode). Field order is the wire order; do not reorder.
type layoutNode struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// idProps is one (ia{sv}) element of the GetGroupProperties reply.
type idProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

// menuEvent is one (isvu) element of an EventGroup call.
type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// menuNode is one materialized menu entry (root or item).
type menuNode struct {
	id        int32
	label     string
	separator bool
	onClick   func()
}

// menu is the static dbusmenu model: the root node 0 plus one node
// per MenuItem, ids 1..n in display order. Everything is immutable
// after newMenu, so reads need no locking.
type menu struct {
	nodes []menuNode // nodes[0] is the root
	logf  func(format string, v ...interface{})
}

func newMenu(items []MenuItem, logf func(format string, v ...interface{})) *menu {
	nodes := make([]menuNode, 0, len(items)+1)
	nodes = append(nodes, menuNode{id: 0})
	for i, it := range items {
		nodes = append(nodes, menuNode{
			id:        int32(i + 1),
			label:     it.Label,
			separator: it.Separator,
			onClick:   it.OnClick,
		})
	}
	return &menu{nodes: nodes, logf: logf}
}

// node returns the entry for id, or nil.
func (m *menu) node(id int32) *menuNode {
	if id < 0 || int(id) >= len(m.nodes) {
		return nil
	}
	return &m.nodes[id]
}

// properties builds a node's dbusmenu property map, filtered to
// propertyNames when non-empty (the GNOME extension asks GetLayout for
// just ["type","children-display"] and fetches the rest through
// GetGroupProperties with an empty filter). Defaults the host assumes
// anyway (enabled=true, visible=true, type=standard) are still sent
// explicitly -- they are cheap and unambiguous.
func (m *menu) properties(n *menuNode, propertyNames []string) map[string]dbus.Variant {
	all := map[string]dbus.Variant{}
	if n.id == 0 {
		all["children-display"] = dbus.MakeVariant("submenu")
	} else if n.separator {
		all["type"] = dbus.MakeVariant("separator")
		all["visible"] = dbus.MakeVariant(true)
	} else {
		all["type"] = dbus.MakeVariant("standard")
		all["label"] = dbus.MakeVariant(n.label)
		all["enabled"] = dbus.MakeVariant(true)
		all["visible"] = dbus.MakeVariant(true)
	}
	if len(propertyNames) == 0 {
		return all
	}
	filtered := map[string]dbus.Variant{}
	for _, name := range propertyNames {
		if v, ok := all[name]; ok {
			filtered[name] = v
		}
	}
	return filtered
}

// layout builds the (ia{sv}av) node for id. Only the root has
// children; recursionDepth 0 omits them (any other value includes the
// single level this menu has, matching the spec's "-1 = full tree").
func (m *menu) layout(n *menuNode, recursionDepth int32, propertyNames []string) layoutNode {
	out := layoutNode{
		ID:       n.id,
		Props:    m.properties(n, propertyNames),
		Children: []dbus.Variant{},
	}
	if n.id == 0 && recursionDepth != 0 {
		for i := 1; i < len(m.nodes); i++ {
			out.Children = append(out.Children,
				dbus.MakeVariant(m.layout(&m.nodes[i], 0, propertyNames)))
		}
	}
	return out
}

// click dispatches a "clicked" event on id; false means the id does
// not exist. Clicking a separator or the root is valid and does
// nothing.
func (m *menu) click(id int32) bool {
	n := m.node(id)
	if n == nil {
		return false
	}
	if n.onClick != nil {
		n.onClick()
	}
	return true
}

// menuHandler implements the com.canonical.dbusmenu methods over a
// menu. Every exported method runs on its own godbus handler
// goroutine.
type menuHandler struct {
	m *menu
}

// GetLayout returns the layout revision plus the node subtree rooted
// at parentId. recursionDepth and propertyNames are honored
// tolerantly: unknown property names are skipped, depth 0 returns a
// childless node, everything else returns the full single level.
func (h menuHandler) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, layoutNode, *dbus.Error) {
	n := h.m.node(parentID)
	if n == nil {
		return 0, layoutNode{}, errUnknownID(parentID)
	}
	return layoutRevision, h.m.layout(n, recursionDepth, propertyNames), nil
}

// GetGroupProperties returns the property maps for ids (empty ids
// means every node, per the dbusmenu spec); unknown ids are skipped.
func (h menuHandler) GetGroupProperties(ids []int32, propertyNames []string) ([]idProps, *dbus.Error) {
	if len(ids) == 0 {
		ids = make([]int32, len(h.m.nodes))
		for i := range h.m.nodes {
			ids[i] = h.m.nodes[i].id
		}
	}
	out := make([]idProps, 0, len(ids))
	for _, id := range ids {
		n := h.m.node(id)
		if n == nil {
			continue
		}
		out = append(out, idProps{ID: id, Props: h.m.properties(n, propertyNames)})
	}
	return out, nil
}

// GetProperty returns one property of one node.
func (h menuHandler) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	n := h.m.node(id)
	if n == nil {
		return dbus.Variant{}, errUnknownID(id)
	}
	v, ok := h.m.properties(n, nil)[name]
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("no menu item property %q", name))
	}
	return v, nil
}

// Event delivers one host event. Only "clicked" does anything; the
// open/close/hover chatter is acknowledged and dropped. An unknown id
// is an error, so hosts notice stale layouts.
func (h menuHandler) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != eventClicked {
		if h.m.node(id) == nil {
			return errUnknownID(id)
		}
		return nil
	}
	if !h.m.click(id) {
		return errUnknownID(id)
	}
	return nil
}

// EventGroup delivers a batch of events; the reply lists the ids that
// do not exist.
func (h menuHandler) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	idErrors := []int32{}
	for _, ev := range events {
		if h.m.node(ev.ID) == nil {
			idErrors = append(idErrors, ev.ID)
			continue
		}
		if ev.EventID == eventClicked {
			h.m.click(ev.ID)
		}
	}
	return idErrors, nil
}

// AboutToShow reports whether the host should re-fetch the layout
// before showing id: never, the menu is static.
func (h menuHandler) AboutToShow(id int32) (bool, *dbus.Error) {
	if h.m.node(id) == nil {
		return false, errUnknownID(id)
	}
	return false, nil
}

// AboutToShowGroup is the batch AboutToShow: nothing needs updates,
// unknown ids are reported back.
func (h menuHandler) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	idErrors := []int32{}
	for _, id := range ids {
		if h.m.node(id) == nil {
			idErrors = append(idErrors, id)
		}
	}
	return []int32{}, idErrors, nil
}

// menuPropSpec builds the com.canonical.dbusmenu property set.
func menuPropSpec() map[string]*prop.Prop {
	return map[string]*prop.Prop{
		"Version":       ro(dbusmenuVersion),
		"TextDirection": ro("ltr"),
		"Status":        ro("normal"),
		"IconThemePath": ro([]string{}),
	}
}

// errUnknownID builds the D-Bus error every method returns for a menu
// id that does not exist.
func errUnknownID(id int32) *dbus.Error {
	return dbus.MakeFailedError(fmt.Errorf("no menu item with id %d", id))
}
