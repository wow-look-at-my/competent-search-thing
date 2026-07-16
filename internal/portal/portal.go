// Package portal is a pure D-Bus client for the XDG Desktop Portal
// GlobalShortcuts interface (org.freedesktop.portal.GlobalShortcuts),
// the sanctioned way to receive a global hotkey inside a Wayland
// session, where X11-style key grabs do not exist. It carries no app
// wiring: callers Dial (or supply) a session-bus connection, probe
// Available, convert the parsed config hotkey with TriggerString and
// Register the shortcut; the portal then reports presses through
// Options.OnActivated for the lifetime of the returned Session.
//
// The package follows the portal Request/Session conventions exactly:
// Response signals are awaited on the request path predicted from the
// connection's unique name plus a random handle_token (subscribed
// BEFORE each call to dodge the race, falling back to the returned
// handle for very old portals), a session may attempt BindShortcuts
// only ONCE (ListShortcuts decides whether binding is needed at all --
// the portal remembers approvals across sessions), and dropping the
// connection is equivalent to closing the session.
package portal

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// The portal frontend's bus identity and the interfaces this package
// speaks. Every portal interface lives on the one desktop object.
const (
	portalBusName  = "org.freedesktop.portal.Desktop"
	desktopPath    = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	shortcutsIface = "org.freedesktop.portal.GlobalShortcuts"
	requestIface   = "org.freedesktop.portal.Request"
	sessionIface   = "org.freedesktop.portal.Session"

	signalResponse  = requestIface + ".Response"
	signalActivated = shortcutsIface + ".Activated"
)

// The two Available failure modes, plus the interactive bind refusal.
// All are wrapped (errors.Is) so callers can pick a user-facing hint.
var (
	// ErrNoPortal reports that nothing owns org.freedesktop.portal.Desktop
	// on the session bus: no xdg-desktop-portal is running at all.
	ErrNoPortal = errors.New("no xdg-desktop-portal on the session bus")

	// ErrNoGlobalShortcuts reports that a portal is running but has no
	// usable GlobalShortcuts backend (e.g. GNOME < 48, sway/wlroots,
	// COSMIC): the version property is absent or below 1.
	ErrNoGlobalShortcuts = errors.New("portal does not support GlobalShortcuts")

	// ErrDenied reports that the user (or backend policy) rejected the
	// BindShortcuts request -- portal Response code 1.
	ErrDenied = errors.New("global shortcut bind denied by the user")
)

// Dial opens a private session-bus connection (authenticated and
// hello'd) suitable for Available and Register. It is private on
// purpose: the portal ties session lifetime to the connection, so the
// caller owns the *dbus.Conn and closes it to tear everything down
// (Session.Close never closes it).
func Dial() (*dbus.Conn, error) {
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return nil, fmt.Errorf("portal: session bus: %w", err)
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("portal: session bus auth: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("portal: session bus hello: %w", err)
	}
	return conn, nil
}

// Available reports whether the GlobalShortcuts portal is usable on
// conn's bus and at which interface version (>= 1). It is a fast,
// non-interactive probe: it checks that org.freedesktop.portal.Desktop
// has an owner and that the GlobalShortcuts version property reads
// back sane. The failure modes stay distinguishable with errors.Is:
// ErrNoPortal (no portal at all) vs ErrNoGlobalShortcuts (a portal
// without a GlobalShortcuts backend).
func Available(conn *dbus.Conn) (uint32, error) {
	var owned bool
	err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, portalBusName).Store(&owned)
	if err != nil {
		return 0, fmt.Errorf("portal: checking for %s: %w", portalBusName, err)
	}
	if !owned {
		return 0, fmt.Errorf("portal: %s has no owner: %w", portalBusName, ErrNoPortal)
	}
	v, err := conn.Object(portalBusName, desktopPath).GetProperty(shortcutsIface + ".version")
	if err != nil {
		return 0, fmt.Errorf("portal: reading %s.version (%v): %w", shortcutsIface, err, ErrNoGlobalShortcuts)
	}
	version, ok := v.Value().(uint32)
	if !ok || version < 1 {
		return 0, fmt.Errorf("portal: unusable %s.version %v: %w", shortcutsIface, v.Value(), ErrNoGlobalShortcuts)
	}
	return version, nil
}
