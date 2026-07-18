// Package launch holds the pure decision logic behind "raise and
// focus on launch": classifying launch targets, the credential-minting
// policy, desktop-entry Exec expansion, org.freedesktop.Application
// activation call derivation, per-child environment composition, the
// X-side raise watcher state machine, and the libstartup-notification
// "remove:" wire format. Everything OS- and display-server-specific
// (GTK/GDK credential minting, gio handler resolution, X property
// reads) lives behind seams in internal/platform/native; this package
// only decides, so it stays headless-testable.
package launch

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Target classifies one launchable thing: a filesystem path or an
// http(s) URL (the only two shapes the app's open paths accept).
type Target struct {
	// Raw is the path or URL exactly as given.
	Raw string
	// URI is the file:// form for paths and Raw for URLs -- what %u/%U
	// field codes and D-Bus Open calls carry.
	URI string
	// IsURL reports that Raw parsed as a scheme://host URL.
	IsURL bool
	// Scheme is the lowercased URL scheme ("" for paths); handler
	// resolution keys off it (g_app_info_get_default_for_uri_scheme).
	Scheme string
	// IsDir marks a directory path; handler resolution then uses the
	// inode/directory content type instead of guessing from the name.
	IsDir bool
}

// ClassifyTarget builds a Target from a raw open argument. isDir is
// the caller's stat verdict for path targets (ignored for URLs).
func ClassifyTarget(raw string, isDir bool) Target {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return Target{Raw: raw, URI: raw, IsURL: true, Scheme: strings.ToLower(u.Scheme)}
	}
	uri := url.URL{Scheme: "file", Path: raw}
	return Target{Raw: raw, URI: uri.String(), IsDir: isDir}
}

// Handler describes the resolved default application for a target (a
// freedesktop .desktop entry, read via gio in the native layer).
type Handler struct {
	// DesktopID is the desktop entry id ("code.desktop"); empty when
	// the handler is not a desktop entry.
	DesktopID string
	// Exec is the raw Exec= line, field codes intact.
	Exec string
	// WMClass is the StartupWMClass= value ("" when unset) -- the
	// raise watcher's strongest window-identity hint.
	WMClass string
	// Exe is the handler's executable (g_app_info_get_executable).
	Exe string
	// DBusActivatable reports DBusActivatable=true: the handler
	// prefers org.freedesktop.Application activation over exec.
	DBusActivatable bool
	// StartupNotify reports StartupNotify=true: the app completes a
	// startup sequence, so minting a credential cannot leave a
	// dangling busy cursor.
	StartupNotify bool
	// Terminal reports Terminal=true: the Exec line needs a terminal
	// emulator, which we do not provide -- such handlers fall back to
	// xdg-open.
	Terminal bool
}

// Credential kinds, named after the mechanism that minted the id.
const (
	// KindNone: no credential could be minted (no GTK loop, no
	// backend support); launches proceed uncredentialed, exactly like
	// before this feature existed.
	KindNone = "none"
	// KindX11SN: an X11 libstartup-notification id, "new:"-broadcast
	// by GDK, carrying a _TIME<user-time> suffix.
	KindX11SN = "x11-sn"
	// KindWaylandGDK: GDK minted the id on Wayland -- a gtk_shell1
	// notify_launch uuid on GTK <= 3.24.34 (GNOME), a real
	// xdg-activation token on newer GTK.
	KindWaylandGDK = "wayland-gdk"
	// KindWaylandXDG: our own xdg_activation_v1 token (non-GNOME
	// compositors, where GDK 3.24.33 has no minting path).
	KindWaylandXDG = "wayland-xdg"
)

// Credential is one minted launch credential. The zero value (or any
// empty ID) means "no credential".
type Credential struct {
	ID   string
	Kind string
}

// ShouldMint decides whether a launch credential is minted for a
// resolved handler. Mirrors GLib's spinner-avoidance gating: a handler
// that declares neither StartupNotify=true nor DBusActivatable=true
// will never complete (or forward) a startup sequence, so minting one
// would only leave a dangling busy cursor. An unresolved handler
// (resolved=false, the xdg-open fallback) mints anyway: the real
// handler is unknown and the credential rides the child environment
// for whatever consumes it.
func ShouldMint(h Handler, resolved bool) bool {
	if !resolved {
		return true
	}
	return h.StartupNotify || h.DBusActivatable
}

// CredentialEnv composes the per-child environment entries that carry
// cred to the launched process: DESKTOP_STARTUP_ID (X11 consumers,
// GTK3) and XDG_ACTIVATION_TOKEN (Wayland consumers, GTK4/Qt6/
// Electron) both carry the same id -- on mutter 42 a notify_launch
// uuid redeemed through the xdg-activation path is accepted by the
// startup-sequence match, and a launchee picks whichever variable its
// toolkit understands. Nil for an empty credential.
func CredentialEnv(cred Credential) []string {
	if cred.ID == "" {
		return nil
	}
	return []string{
		"DESKTOP_STARTUP_ID=" + cred.ID,
		"XDG_ACTIVATION_TOKEN=" + cred.ID,
	}
}

// Launch transport names, as they appear in the per-launch log line.
const (
	// TransportDBus: org.freedesktop.Application Open/Activate.
	TransportDBus = "dbus"
	// TransportExec: the handler's own Exec line, spawned by us.
	TransportExec = "exec"
	// TransportXdgOpen: the pre-existing xdg-open candidate table.
	TransportXdgOpen = "xdg-open"
	// TransportShowItems: the reveal path's FileManager1 ShowItems
	// candidates (dbus-send with the minted startup id, then the
	// xdg-open parent-directory fallback).
	TransportShowItems = "showitems"
)

// credentialIDPrefix is how many id characters the log line shows.
const credentialIDPrefix = 8

// LogLine renders the one-per-launch log line:
//
//	launch: open <target> handler=<id|-> credential=<kind>[:<id8>] transport=<t> watcher=<on|off>
func LogLine(verb, target string, h Handler, resolved bool, cred Credential, transport string, watcher bool) string {
	handler := "-"
	if resolved && h.DesktopID != "" {
		handler = h.DesktopID
	}
	credential := KindNone
	if cred.ID != "" {
		id := cred.ID
		if len(id) > credentialIDPrefix {
			id = id[:credentialIDPrefix]
		}
		credential = cred.Kind + ":" + id
	}
	w := "off"
	if watcher {
		w = "on"
	}
	return fmt.Sprintf("launch: %s %s handler=%s credential=%s transport=%s watcher=%s",
		verb, target, handler, credential, transport, w)
}

// ValidDesktopID re-validates a plugin action's desktop_id as defense
// in depth: a bare .desktop file name (no path separators, a non-empty
// stem) is the only shape the builtin providers produce and the only
// one HandlerByDesktopID accepts.
func ValidDesktopID(id string) error {
	if id == "" {
		return fmt.Errorf("empty desktop id")
	}
	if filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("desktop id %q contains path separators", id)
	}
	if !strings.HasSuffix(id, ".desktop") || len(id) == len(".desktop") {
		return fmt.Errorf("desktop id %q is not a .desktop file name", id)
	}
	return nil
}
