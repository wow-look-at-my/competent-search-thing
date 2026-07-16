package platform

import "strings"

// SessionKind classifies the display-server session the app runs
// under.
type SessionKind int

// The session kinds DetectSession can report.
const (
	// SessionUnknown means neither a Wayland nor an X11 session could
	// be identified (headless, or a stripped environment).
	SessionUnknown SessionKind = iota
	// SessionX11 is a classic X11 session (global hotkeys work via
	// XGrabKey).
	SessionX11
	// SessionWayland is a Wayland session (compositors refuse key
	// grabs; summoning needs the IPC/CLI path or a desktop-specific
	// keybinding backend).
	SessionWayland
)

// String returns "x11", "wayland" or "unknown".
func (k SessionKind) String() string {
	switch k {
	case SessionX11:
		return "x11"
	case SessionWayland:
		return "wayland"
	default:
		return "unknown"
	}
}

// Session describes the detected desktop session.
type Session struct {
	// Kind is the display-server flavor.
	Kind SessionKind
	// Desktop is the raw XDG_CURRENT_DESKTOP value (a colon-separated
	// list, e.g. "ubuntu:GNOME"); empty when unset.
	Desktop string
}

// DetectSession classifies the current desktop session from the
// environment: XDG_SESSION_TYPE wins when it names wayland or x11,
// else a non-empty WAYLAND_DISPLAY means Wayland, else a non-empty
// DISPLAY means X11, else unknown. getenv is injectable for tests;
// production passes os.Getenv.
func DetectSession(getenv func(string) string) Session {
	s := Session{Desktop: getenv("XDG_CURRENT_DESKTOP")}
	switch getenv("XDG_SESSION_TYPE") {
	case "wayland":
		s.Kind = SessionWayland
		return s
	case "x11":
		s.Kind = SessionX11
		return s
	}
	if getenv("WAYLAND_DISPLAY") != "" {
		s.Kind = SessionWayland
		return s
	}
	if getenv("DISPLAY") != "" {
		s.Kind = SessionX11
		return s
	}
	return s // Kind zero value = SessionUnknown
}

// IsGNOME reports whether any colon-separated segment of the desktop
// name equals "gnome", case-insensitively ("ubuntu:GNOME",
// "GNOME-Classic:GNOME", ...).
func (s Session) IsGNOME() bool {
	for _, part := range strings.Split(s.Desktop, ":") {
		if strings.EqualFold(part, "gnome") {
			return true
		}
	}
	return false
}
