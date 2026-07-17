//go:build linux

package native

import (
	"errors"
	"os"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
)

// maxOpenWindows caps one OpenWindows snapshot; the builtin search
// shows at most a handful of results, so a busy session's tail is
// never worth more X round-trips.
const maxOpenWindows = 100

// activateSourcePager is the EWMH _NET_ACTIVE_WINDOW source-indication
// value for pagers and similar direct user tools: window managers
// honor it as an explicit user request rather than an app stealing
// focus.
const activateSourcePager = 2

// OpenWindows lists the EWMH client-list windows with their titles:
// _NET_CLIENT_LIST on the root window, then per window _NET_WM_NAME
// (falling back to WM_NAME), WM_CLASS's class field as App, and
// _NET_WM_PID -- the same conn-per-call property reads RunningApps
// uses. Windows without a title and this process's own windows are
// skipped; every window of every OTHER process is kept (no pid
// dedup -- each window is its own search result). ok is false when
// there is no X server or no client list (a non-EWMH WM). On a
// Wayland session an X connection would list only XWayland clients --
// a misleading partial view -- so the app layer never calls this
// there (session gating in internal/app).
func (appSource) OpenWindows() ([]appctx.WindowInfo, bool) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, false
	}
	defer conn.Close()

	root := xproto.Setup(conn).DefaultScreen(conn).Root
	v := windowProp(conn, root, internAtom(conn, "_NET_CLIENT_LIST"))
	if v == nil {
		return nil, false
	}
	atoms := internAppAtoms(conn)
	self := os.Getpid()
	var wins []appctx.WindowInfo
	for i := 0; i+4 <= len(v) && len(wins) < maxOpenWindows; i += 4 {
		win := xproto.Window(xgb.Get32(v[i:]))
		if win == 0 {
			continue
		}
		info := windowAppInfo(conn, win, atoms)
		if info.Title == "" || (info.PID != 0 && info.PID == self) {
			continue
		}
		wins = append(wins, appctx.WindowInfo{
			ID:    uint32(win),
			Title: info.Title,
			App:   info.Name,
			PID:   info.PID,
		})
	}
	return wins, true
}

// ActivateWindow raises and focuses the window via the EWMH
// _NET_ACTIVE_WINDOW client message: format 32, source indication
// "pager" (a direct user request), sent to the root window with the
// SubstructureRedirect|SubstructureNotify event mask so the window
// manager receives it. A vanished window id is harmless -- the WM
// ignores the message. No X server means an error and the caller
// degrades.
func ActivateWindow(id uint32) error {
	conn, err := xgb.NewConn()
	if err != nil {
		return err
	}
	defer conn.Close()

	atom := internAtom(conn, "_NET_ACTIVE_WINDOW")
	if atom == 0 {
		return errors.New("the window manager does not support _NET_ACTIVE_WINDOW")
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: xproto.Window(id),
		Type:   atom,
		// data.l = [source indication, timestamp, requestor's active
		// window, 0, 0] per the EWMH spec; 0 timestamps are accepted.
		Data: xproto.ClientMessageDataUnionData32New([]uint32{activateSourcePager, 0, 0, 0, 0}),
	}
	return xproto.SendEventChecked(conn, false, root,
		xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify,
		string(ev.Bytes())).Check()
}
