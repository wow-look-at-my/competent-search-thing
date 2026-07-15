//go:build linux

package native

import (
	"bytes"
	"os"
	"sort"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/wow-look-at-my/competent-search-thing/internal/appctx"
)

// maxRunningApps caps one RunningApps snapshot; plugin payloads never
// need more.
const maxRunningApps = 64

// propReadLen is the GetProperty long_length (in 32-bit units) used
// for every property read: 16 KiB covers window titles, WM_CLASS
// pairs, and busy _NET_CLIENT_LIST arrays alike.
const propReadLen = 4096

// appSource is the Linux appctx.Source: EWMH window properties over
// the same conn-per-call jezek/xgb pattern the rest of this package
// uses, plus /proc for process identity and XDG .desktop scans for
// installed apps. No X server (e.g. a Wayland-only session) means
// ok=false and the app-context feature degrades silently.
type appSource struct{}

// AppSource returns this OS's implementation of appctx.Source.
func AppSource() appctx.Source { return appSource{} }

// FocusedApp identifies the application owning the EWMH active window
// (_NET_ACTIVE_WINDOW on the root window). Per-window pieces are each
// best-effort with fallbacks (see windowAppInfo); ok is false when
// there is no X server, no active window, or the window yields no
// identity at all.
func (appSource) FocusedApp() (appctx.AppInfo, bool) {
	conn, err := xgb.NewConn()
	if err != nil {
		return appctx.AppInfo{}, false
	}
	defer conn.Close()

	root := xproto.Setup(conn).DefaultScreen(conn).Root
	v := windowProp(conn, root, internAtom(conn, "_NET_ACTIVE_WINDOW"))
	if len(v) < 4 {
		return appctx.AppInfo{}, false
	}
	win := xproto.Window(xgb.Get32(v))
	if win == 0 {
		return appctx.AppInfo{}, false
	}
	info := windowAppInfo(conn, win, internAppAtoms(conn))
	if info.Name == "" && info.PID == 0 {
		return appctx.AppInfo{}, false
	}
	return info, true
}

// RunningApps lists the applications with windows in the EWMH client
// list (_NET_CLIENT_LIST on the root window), deduplicated by pid
// keeping the first window's title, capped at maxRunningApps, and
// sorted by Name. Windows without a _NET_WM_PID are skipped -- an
// application cannot be identified without its process. ok is false
// when there is no X server or no client list (a non-EWMH WM).
func (appSource) RunningApps() ([]appctx.AppInfo, bool) {
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
	seen := make(map[int]bool)
	var apps []appctx.AppInfo
	for i := 0; i+4 <= len(v) && len(apps) < maxRunningApps; i += 4 {
		win := xproto.Window(xgb.Get32(v[i:]))
		if win == 0 {
			continue
		}
		info := windowAppInfo(conn, win, atoms)
		if info.PID == 0 || seen[info.PID] {
			continue
		}
		seen[info.PID] = true
		apps = append(apps, info)
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].PID < apps[j].PID
	})
	return apps, true
}

// InstalledApps scans the XDG .desktop application directories.
func (appSource) InstalledApps() ([]appctx.InstalledApp, bool) {
	return appctx.ScanDesktopDirs(appctx.DesktopDirs(os.Getenv)), true
}

// appAtoms holds the per-call interned EWMH atoms (the WM_CLASS and
// WM_NAME atoms are predefined by the core protocol).
type appAtoms struct {
	pid, netName xproto.Atom
}

func internAppAtoms(conn *xgb.Conn) appAtoms {
	return appAtoms{
		pid:     internAtom(conn, "_NET_WM_PID"),
		netName: internAtom(conn, "_NET_WM_NAME"),
	}
}

// windowAppInfo extracts one window's application identity, each
// piece best-effort: pid from _NET_WM_PID, name from WM_CLASS's class
// field (second NUL-terminated string) falling back to the /proc
// comm, title from _NET_WM_NAME (UTF-8) falling back to the legacy
// WM_NAME, exe from readlink /proc/<pid>/exe (routinely empty for
// other users' processes). Missing properties just leave zero fields.
func windowAppInfo(conn *xgb.Conn, win xproto.Window, atoms appAtoms) appctx.AppInfo {
	var info appctx.AppInfo
	if v := windowProp(conn, win, atoms.pid); len(v) >= 4 {
		info.PID = int(xgb.Get32(v))
	}
	if parts := bytes.SplitN(windowProp(conn, win, xproto.AtomWmClass), []byte{0}, 3); len(parts) >= 2 {
		info.Name = string(parts[1])
	}
	if v := windowProp(conn, win, atoms.netName); len(v) > 0 {
		info.Title = propString(v)
	} else if v := windowProp(conn, win, xproto.AtomWmName); len(v) > 0 {
		info.Title = propString(v)
	}
	if info.PID > 0 {
		exe, comm := appctx.ProcInfo("/proc", info.PID)
		info.Exe = exe
		if info.Name == "" {
			info.Name = comm
		}
	}
	return info
}

// internAtom resolves name to an existing atom; 0 when the atom does
// not exist on this server (its property then reads as absent).
func internAtom(conn *xgb.Conn, name string) xproto.Atom {
	rep, err := xproto.InternAtom(conn, true, uint16(len(name)), name).Reply()
	if err != nil || rep == nil {
		return 0
	}
	return rep.Atom
}

// windowProp reads one property (of any type) of win; nil when the
// atom or the property is absent or the read fails (e.g. the window
// vanished between listing and reading).
func windowProp(conn *xgb.Conn, win xproto.Window, prop xproto.Atom) []byte {
	if prop == 0 {
		return nil
	}
	rep, err := xproto.GetProperty(conn, false, win, prop, xproto.GetPropertyTypeAny, 0, propReadLen).Reply()
	if err != nil || rep == nil || rep.ValueLen == 0 {
		return nil
	}
	return rep.Value
}

// propString converts a string property value, dropping any trailing
// NULs some clients include.
func propString(v []byte) string {
	return strings.TrimRight(string(v), "\x00")
}
