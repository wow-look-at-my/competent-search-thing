package gsettings

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

// DaemonName is the well-known session-bus name gsd-media-keys owns
// (gsd-media-keys-manager.c: GSD_DBUS_NAME ".MediaKeys"). The daemon
// is the process that reads the custom-keybindings list and asks the
// compositor for the grabs -- a keybinding entry is inert without it.
const DaemonName = "org.gnome.SettingsDaemon.MediaKeys"

// DaemonRunning reports whether GNOME's media-keys daemon currently
// owns DaemonName on the session bus. An error means the check itself
// could not run (typically no session bus at all -- headless CI, a
// broken DBUS_SESSION_BUS_ADDRESS); callers should treat that as
// "unknown" and stay quiet, not as "daemon missing".
func DaemonRunning(ctx context.Context) (bool, error) {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return false, fmt.Errorf("gsettings: connecting to the session bus: %w", err)
	}
	defer conn.Close()
	var has bool
	err = conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, DaemonName).Store(&has)
	if err != nil {
		return false, fmt.Errorf("gsettings: asking the bus about %s: %w", DaemonName, err)
	}
	return has, nil
}
