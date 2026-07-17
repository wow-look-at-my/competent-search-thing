package tray

import (
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// pixmap is one (iiay) icon image: width, height, and ARGB32 pixel
// data in network byte order (bytes A,R,G,B per pixel, straight
// alpha) -- the byte order the freedesktop spec mandates and the
// AppIndicator extension's parser reads (v42 argbToRgba: src[0]=alpha,
// src[1]=red, src[2]=green, src[3]=blue).
type pixmap struct {
	Width  int32
	Height int32
	Data   []byte
}

// tooltip is the SNI ToolTip property, wire type (sa(iiay)ss): icon
// name, icon pixmaps, title, descriptive text.
type tooltip struct {
	IconName   string
	IconPixmap []pixmap
	Title      string
	Text       string
}

// ro builds a static read-only property (no PropertiesChanged
// emission; SNI hosts listen to the New* signals instead).
func ro(v interface{}) *prop.Prop {
	return &prop.Prop{Value: v, Writable: false, Emit: prop.EmitFalse}
}

// itemProps wraps the exported property set of the item object so the
// tooltip text can be refreshed after export.
type itemProps struct {
	mu       sync.Mutex
	props    *prop.Properties
	lastText string
	title    string
}

// setTooltipText updates the ToolTip property; true means the value
// changed (the caller then emits NewToolTip).
func (p *itemProps) setTooltipText(text string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if text == p.lastText {
		return false
	}
	p.lastText = text
	p.props.SetMust(itemIface, "ToolTip", tooltip{
		IconPixmap: []pixmap{},
		Title:      p.title,
		Text:       text,
	})
	return true
}

// sniHandler implements the org.kde.StatusNotifierItem methods. Every
// exported method runs on its own godbus handler goroutine.
type sniHandler struct {
	t *Tray
}

// Activate is the host's primary activation (double-click under
// GNOME's extension, plain click on hosts where ItemIsMenu matters).
func (h sniHandler) Activate(x, y int32) *dbus.Error {
	h.t.activate()
	return nil
}

// SecondaryActivate is the middle-click action.
func (h sniHandler) SecondaryActivate(x, y int32) *dbus.Error {
	h.t.activate()
	return nil
}

// XAyatanaSecondaryActivate is the ayatana middle-click variant; the
// GNOME extension tries it FIRST and only falls back to
// SecondaryActivate when the method is unknown, so implementing it
// skips a round trip.
func (h sniHandler) XAyatanaSecondaryActivate(timestamp uint32) *dbus.Error {
	h.t.activate()
	return nil
}

// ContextMenu is a no-op: the host renders the dbusmenu itself.
func (h sniHandler) ContextMenu(x, y int32) *dbus.Error { return nil }

// Scroll is a no-op: the searchbar has nothing to scroll.
func (h sniHandler) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// activate dispatches Options.OnActivate.
func (t *Tray) activate() {
	if t.opts.OnActivate != nil {
		t.opts.OnActivate()
	}
}

// export puts the item and menu objects, their properties, and
// introspection data on conn. The property export matters most: the
// AppIndicator extension reads everything through
// org.freedesktop.DBus.Properties.GetAll and refuses to show an item
// until Id and Menu read back sane.
func (t *Tray) export(conn *dbus.Conn) error {
	tooltipText := ""
	if t.opts.Tooltip != nil {
		tooltipText = t.opts.Tooltip()
	}

	if err := conn.Export(sniHandler{t}, itemPath, itemIface); err != nil {
		return err
	}
	itemProperties, err := prop.Export(conn, itemPath, prop.Map{
		itemIface: sniPropSpec(t.opts.ID, t.opts.Title, tooltipText),
	})
	if err != nil {
		return err
	}

	m := newMenu(t.opts.Menu, t.opts.Logf)
	if err := conn.Export(menuHandler{m}, menuPath, menuIface); err != nil {
		return err
	}
	menuProperties, err := prop.Export(conn, menuPath, prop.Map{
		menuIface: menuPropSpec(),
	})
	if err != nil {
		return err
	}

	if err := exportIntrospection(conn, itemProperties, menuProperties); err != nil {
		return err
	}

	t.mu.Lock()
	t.props = &itemProps{props: itemProperties, lastText: tooltipText, title: t.opts.Title}
	t.menu = m
	t.mu.Unlock()
	return nil
}

// sniPropSpec builds the StatusNotifierItem property set. Everything
// is read-only and static except ToolTip (refreshed via setTooltipText
// + NewToolTip). The icon is pixmap-only on purpose: the v42 extension
// prefers IconName whenever it is set and resolves it with a "-panel"
// suffix against the icon theme, falling back to the pixmap only when
// that lookup fails (with a logged warning per attempt) -- a name
// would either shadow the drawn icon or spam lookup warnings, while a
// pixmap renders deterministically on every host.
func sniPropSpec(id, title, tooltipText string) map[string]*prop.Prop {
	return map[string]*prop.Prop{
		"Category":            ro("ApplicationStatus"),
		"Id":                  ro(id),
		"Title":               ro(title),
		"Status":              ro("Active"),
		"WindowId":            ro(int32(0)),
		"IconThemePath":       ro(""),
		"Menu":                ro(menuPath),
		"ItemIsMenu":          ro(false),
		"IconName":            ro(""),
		"IconPixmap":          ro(iconPixmaps()),
		"OverlayIconName":     ro(""),
		"OverlayIconPixmap":   ro([]pixmap{}),
		"AttentionIconName":   ro(""),
		"AttentionIconPixmap": ro([]pixmap{}),
		"AttentionMovieName":  ro(""),
		"ToolTip": {
			Value:    tooltip{IconPixmap: []pixmap{}, Title: title, Text: tooltipText},
			Writable: false,
			Emit:     prop.EmitFalse,
		},
	}
}

// exportIntrospection publishes org.freedesktop.DBus.Introspectable
// for the two objects and a root node naming them, so D-Bus browsers
// and the extension's brute-force item scan (which walks the
// introspection tree from "/") can find the item even without a
// registration.
func exportIntrospection(conn *dbus.Conn, itemProperties, menuProperties *prop.Properties) error {
	itemNode := &introspect.Node{
		Name: string(itemPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name: itemIface,
				Methods: []introspect.Method{
					{Name: "Activate", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
					{Name: "SecondaryActivate", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
					{Name: "XAyatanaSecondaryActivate", Args: []introspect.Arg{{Name: "timestamp", Type: "u", Direction: "in"}}},
					{Name: "ContextMenu", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
					{Name: "Scroll", Args: []introspect.Arg{{Name: "delta", Type: "i", Direction: "in"}, {Name: "orientation", Type: "s", Direction: "in"}}},
				},
				Properties: itemProperties.Introspection(itemIface),
				Signals: []introspect.Signal{
					{Name: "NewTitle"},
					{Name: "NewIcon"},
					{Name: "NewAttentionIcon"},
					{Name: "NewOverlayIcon"},
					{Name: "NewToolTip"},
					{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s", Direction: "out"}}},
					{Name: "NewIconThemePath", Args: []introspect.Arg{{Name: "icon_theme_path", Type: "s", Direction: "out"}}},
					{Name: "NewMenu"},
				},
			},
		},
	}
	menuNode := &introspect.Node{
		Name: string(menuPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{
				Name: menuIface,
				Methods: []introspect.Method{
					{Name: "GetLayout", Args: []introspect.Arg{
						{Name: "parentId", Type: "i", Direction: "in"},
						{Name: "recursionDepth", Type: "i", Direction: "in"},
						{Name: "propertyNames", Type: "as", Direction: "in"},
						{Name: "revision", Type: "u", Direction: "out"},
						{Name: "layout", Type: "(ia{sv}av)", Direction: "out"},
					}},
					{Name: "GetGroupProperties", Args: []introspect.Arg{
						{Name: "ids", Type: "ai", Direction: "in"},
						{Name: "propertyNames", Type: "as", Direction: "in"},
						{Name: "properties", Type: "a(ia{sv})", Direction: "out"},
					}},
					{Name: "GetProperty", Args: []introspect.Arg{
						{Name: "id", Type: "i", Direction: "in"},
						{Name: "name", Type: "s", Direction: "in"},
						{Name: "value", Type: "v", Direction: "out"},
					}},
					{Name: "Event", Args: []introspect.Arg{
						{Name: "id", Type: "i", Direction: "in"},
						{Name: "eventId", Type: "s", Direction: "in"},
						{Name: "data", Type: "v", Direction: "in"},
						{Name: "timestamp", Type: "u", Direction: "in"},
					}},
					{Name: "EventGroup", Args: []introspect.Arg{
						{Name: "events", Type: "a(isvu)", Direction: "in"},
						{Name: "idErrors", Type: "ai", Direction: "out"},
					}},
					{Name: "AboutToShow", Args: []introspect.Arg{
						{Name: "id", Type: "i", Direction: "in"},
						{Name: "needUpdate", Type: "b", Direction: "out"},
					}},
					{Name: "AboutToShowGroup", Args: []introspect.Arg{
						{Name: "ids", Type: "ai", Direction: "in"},
						{Name: "updatesNeeded", Type: "ai", Direction: "out"},
						{Name: "idErrors", Type: "ai", Direction: "out"},
					}},
				},
				Properties: menuProperties.Introspection(menuIface),
				Signals: []introspect.Signal{
					{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{
						{Name: "updatedProps", Type: "a(ia{sv})", Direction: "out"},
						{Name: "removedProps", Type: "a(ias)", Direction: "out"},
					}},
					{Name: "LayoutUpdated", Args: []introspect.Arg{
						{Name: "revision", Type: "u", Direction: "out"},
						{Name: "parent", Type: "i", Direction: "out"},
					}},
					{Name: "ItemActivationRequested", Args: []introspect.Arg{
						{Name: "id", Type: "i", Direction: "out"},
						{Name: "timestamp", Type: "u", Direction: "out"},
					}},
				},
			},
		},
	}
	root := &introspect.Node{
		Children: []introspect.Node{
			{Name: "StatusNotifierItem"},
			{Name: "MenuBar"},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(itemNode), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	if err := conn.Export(introspect.NewIntrospectable(menuNode), menuPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	return conn.Export(introspect.NewIntrospectable(root), "/", "org.freedesktop.DBus.Introspectable")
}
