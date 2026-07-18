package launch

import (
	"context"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

// DBusCall describes one org.freedesktop.Application activation call,
// fully derived so the transport (and its tests) need no policy.
type DBusCall struct {
	// Dest is the well-known bus name (the desktop id sans .desktop).
	Dest string
	// Path is the application object path derived from Dest per the
	// Desktop Entry spec ("." -> "/", "-" -> "_", leading "/").
	Path string
	// Method is "Open" (with URIs) or "Activate" (bare app launch).
	Method string
	// URIs is the Open payload (nil for Activate).
	URIs []string
	// PlatformData carries the launch credential under BOTH spec'd
	// keys ("desktop-startup-id" and "activation-token"), so X11 and
	// Wayland consumers alike can redeem it; empty (but non-nil in
	// the wire call) without a credential.
	PlatformData map[string]string
}

// ApplicationDBusCall derives the activation call for handler h: Open
// with the target's URI when t is non-nil, Activate for a bare
// application launch. ok is false when h is not DBus-activatable or
// its desktop id does not reverse into a valid bus name (a broken
// DBusActivatable=true entry; the exec transport then applies).
func ApplicationDBusCall(h Handler, t *Target, cred Credential) (DBusCall, bool) {
	if !h.DBusActivatable {
		return DBusCall{}, false
	}
	name := strings.TrimSuffix(h.DesktopID, ".desktop")
	if name == h.DesktopID || !validBusName(name) {
		return DBusCall{}, false
	}
	call := DBusCall{
		Dest:         name,
		Path:         "/" + strings.NewReplacer(".", "/", "-", "_").Replace(name),
		Method:       "Activate",
		PlatformData: platformData(cred),
	}
	if t != nil {
		call.Method = "Open"
		call.URIs = []string{t.URI}
	}
	return call, true
}

// platformData builds the platform-data dictionary carrying cred.
func platformData(cred Credential) map[string]string {
	data := map[string]string{}
	if cred.ID != "" {
		data["desktop-startup-id"] = cred.ID
		data["activation-token"] = cred.ID
	}
	return data
}

// validBusName reports whether name is a valid well-known D-Bus name:
// two or more dot-separated elements, each of [A-Za-z0-9_-] not
// starting with a digit, total length within the spec's 255 bytes.
func validBusName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	elems := strings.Split(name, ".")
	if len(elems) < 2 {
		return false
	}
	for _, e := range elems {
		if e == "" {
			return false
		}
		for i := 0; i < len(e); i++ {
			c := e[i]
			ok := c == '_' || c == '-' ||
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9' && i > 0)
			if !ok {
				return false
			}
		}
	}
	return true
}

// DBusActivate performs call against the session bus over a private,
// never-autolaunched connection (the tray precedent: a missing bus is
// a normal degraded state, not a reason to spawn a daemon). The method
// call itself D-Bus-activates the service when it is not yet running
// -- that IS the launch. ctx bounds the whole exchange; the caller
// owns the timeout.
func DBusActivate(ctx context.Context, call DBusCall) error {
	conn, err := dbus.SessionBusPrivateNoAutoStartup(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Auth(nil); err != nil {
		return fmt.Errorf("session bus auth: %w", err)
	}
	if err := conn.Hello(); err != nil {
		return fmt.Errorf("session bus hello: %w", err)
	}
	data := make(map[string]dbus.Variant, len(call.PlatformData))
	for k, v := range call.PlatformData {
		data[k] = dbus.MakeVariant(v)
	}
	obj := conn.Object(call.Dest, dbus.ObjectPath(call.Path))
	method := "org.freedesktop.Application." + call.Method
	var c *dbus.Call
	switch call.Method {
	case "Open":
		c = obj.CallWithContext(ctx, method, 0, call.URIs, data)
	case "Activate":
		c = obj.CallWithContext(ctx, method, 0, data)
	default:
		return fmt.Errorf("unsupported application method %q", call.Method)
	}
	if c.Err != nil {
		return fmt.Errorf("%s %s: %w", call.Dest, call.Method, c.Err)
	}
	return nil
}
