//go:build linux

package native

import (
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xinerama"
	"github.com/jezek/xgb/xproto"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// CursorDisplays returns the current cursor position and the monitor
// layout, both in absolute X11 root (virtual-desktop) coordinates.
// Monitors come from the Xinerama extension (which unified RANDR
// layouts also expose); when Xinerama is missing or reports no screens,
// the whole root window is returned as a single display. ok is false
// when there is no X server to ask (e.g. a Wayland-only session) --
// callers then center the window instead of following the cursor.
//
// X11 has no work-area concept per monitor (docks are EWMH struts), so
// Work equals the full geometry, and Xinerama has no primary flag, so
// screen 0 -- which by convention is the primary output -- is marked
// primary.
func CursorDisplays() (cx, cy int, ds []platform.Display, ok bool) {
	conn, err := xgb.NewConn()
	if err != nil {
		return 0, 0, nil, false
	}
	defer conn.Close()

	screen := xproto.Setup(conn).DefaultScreen(conn)
	qp, err := xproto.QueryPointer(conn, screen.Root).Reply()
	if err != nil {
		return 0, 0, nil, false
	}

	ds = xineramaDisplays(conn)
	if len(ds) == 0 {
		root := platform.Rect{X: 0, Y: 0, W: int(screen.WidthInPixels), H: int(screen.HeightInPixels)}
		ds = []platform.Display{{Rect: root, Work: root, Primary: true}}
	}
	return int(qp.RootX), int(qp.RootY), ds, true
}

// xineramaDisplays queries the Xinerama screen list; nil when the
// extension is unavailable or lists nothing.
func xineramaDisplays(conn *xgb.Conn) []platform.Display {
	if err := xinerama.Init(conn); err != nil {
		return nil
	}
	rep, err := xinerama.QueryScreens(conn).Reply()
	if err != nil || rep == nil {
		return nil
	}
	ds := make([]platform.Display, 0, len(rep.ScreenInfo))
	for i, s := range rep.ScreenInfo {
		r := platform.Rect{X: int(s.XOrg), Y: int(s.YOrg), W: int(s.Width), H: int(s.Height)}
		ds = append(ds, platform.Display{Rect: r, Work: r, Primary: i == 0})
	}
	return ds
}
