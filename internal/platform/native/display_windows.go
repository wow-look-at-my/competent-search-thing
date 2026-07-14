//go:build windows

package native

import (
	"syscall"
	"unsafe"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

var (
	user32                  = syscall.NewLazySystemDLL("user32.dll")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
)

// winPoint is the Win32 POINT struct.
type winPoint struct {
	X, Y int32
}

// winRect is the Win32 RECT struct.
type winRect struct {
	Left, Top, Right, Bottom int32
}

// winMonitorInfo is the Win32 MONITORINFO struct; CbSize must be set
// before calling GetMonitorInfoW.
type winMonitorInfo struct {
	CbSize  uint32
	Monitor winRect
	Work    winRect
	Flags   uint32
}

// monitorinfofPrimary flags the primary monitor in winMonitorInfo.Flags.
const monitorinfofPrimary = 0x1

// enumMonitorsCallback is created ONCE: syscall.NewCallback allocations
// are permanent for the process's lifetime, so per-call creation would
// leak the (small, fixed) callback table. The result slice travels
// through the LPARAM.
var enumMonitorsCallback = syscall.NewCallback(func(hMonitor, hdc, lprcMonitor, lparam uintptr) uintptr {
	ds := (*[]platform.Display)(unsafe.Pointer(lparam)) //nolint:govet // standard EnumDisplayMonitors LPARAM round-trip
	var mi winMonitorInfo
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); r != 0 {
		*ds = append(*ds, platform.Display{
			Rect:    toRect(mi.Monitor),
			Work:    toRect(mi.Work),
			Primary: mi.Flags&monitorinfofPrimary != 0,
		})
	}
	return 1 // TRUE: continue enumeration
})

func toRect(r winRect) platform.Rect {
	return platform.Rect{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}
}

// CursorDisplays returns the cursor position and the monitor layout in
// absolute virtual-screen coordinates (GetCursorPos and
// MONITORINFO.rcMonitor/rcWork already are). ok is false when either
// user32 call fails.
func CursorDisplays() (cx, cy int, ds []platform.Display, ok bool) {
	var pt winPoint
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); r == 0 {
		return 0, 0, nil, false
	}
	if r, _, _ := procEnumDisplayMonitors.Call(0, 0, enumMonitorsCallback, uintptr(unsafe.Pointer(&ds))); r == 0 || len(ds) == 0 {
		return 0, 0, nil, false
	}
	return int(pt.X), int(pt.Y), ds, true
}
