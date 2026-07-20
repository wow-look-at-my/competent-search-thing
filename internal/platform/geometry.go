// Package platform holds the pure, OS-independent logic of the
// platform layer: hotkey spec parsing, display picking and searchbar
// positioning math, and open/reveal command construction. Everything
// in this package is deterministic and headlessly testable.
//
// The thin glue that actually talks to the operating system (X11,
// Win32, Cocoa) lives in the subpackage native, which deliberately has
// no test files: it cannot run in a headless test environment, so it
// stays minimal and every decision that CAN be pure lives here instead.
package platform

// Rect is a rectangle in virtual-desktop coordinates: origin at the
// top-left, y growing downward. Displays positioned left of or above
// the primary display have negative origins.
type Rect struct {
	X, Y, W, H int
}

// Contains reports whether the point (x, y) lies inside the rectangle.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Display is one monitor of the virtual desktop.
type Display struct {
	// Rect is the full monitor geometry.
	Rect Rect
	// Work is the usable area (excluding taskbars and docks) on
	// platforms that report one; elsewhere it equals Rect.
	Work Rect
	// Primary marks the operating system's primary display.
	Primary bool
}

// UsableRect returns the display's usable area for window sizing: the
// Work rect when it has any area, else the full Rect. On platforms
// that report no work area (linux Xinerama fills Work with the full
// geometry; a zero-valued Work can only come from hand-built values)
// the full display is the usable area, so the clamp never collapses
// to zero.
func (d Display) UsableRect() Rect {
	if d.Work.W > 0 && d.Work.H > 0 {
		return d.Work
	}
	return d.Rect
}

// ClampSize limits a window size to fit inside area -- the
// clamp-to-screen rule every path that sizes the bar window applies
// (summons, the config window-size applier, the preview-pane mount,
// drag resizing). The floors win over a pathologically small area: a
// window is never sized below minW x minH, even when the display is
// smaller (it then overflows, and BarPosition's clamp pins it to the
// display origin). A zero-area dimension leaves that axis unclamped.
func ClampSize(area Rect, w, h, minW, minH int) (int, int) {
	if area.W > 0 && w > area.W {
		w = area.W
	}
	if area.H > 0 && h > area.H {
		h = area.H
	}
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	return w, h
}

// PickDisplay returns the display that should host the searchbar: the
// one containing the cursor, else the primary, else the first. ok is
// false only for an empty display list.
func PickDisplay(ds []Display, cx, cy int) (Display, bool) {
	if len(ds) == 0 {
		return Display{}, false
	}
	for _, d := range ds {
		if d.Rect.Contains(cx, cy) {
			return d, true
		}
	}
	for _, d := range ds {
		if d.Primary {
			return d, true
		}
	}
	return ds[0], true
}

// BarPosition returns the absolute virtual-desktop coordinates of the
// searchbar's top-left corner on display d: horizontally centered, the
// window's top at one third of the display height minus one third of
// the window height (the Spotlight look), clamped so the window stays
// inside the display.
func BarPosition(d Display, winW, winH int) (x, y int) {
	x = d.Rect.X + (d.Rect.W-winW)/2
	y = d.Rect.Y + d.Rect.H/3 - winH/3
	x = clamp(x, d.Rect.X, d.Rect.X+d.Rect.W-winW)
	y = clamp(y, d.Rect.Y, d.Rect.Y+d.Rect.H-winH)
	return x, y
}

// DisplayForWindow returns the display a window is considered to be on:
// the one containing the window's center point, else the one containing
// its top-left corner, else the primary, else the first. ok is false
// only for an empty display list.
func DisplayForWindow(ds []Display, wx, wy, winW, winH int) (Display, bool) {
	if d, ok := displayContaining(ds, wx+winW/2, wy+winH/2); ok {
		return d, true
	}
	return PickDisplay(ds, wx, wy)
}

// displayContaining returns the display whose geometry contains (x, y).
func displayContaining(ds []Display, x, y int) (Display, bool) {
	for _, d := range ds {
		if d.Rect.Contains(x, y) {
			return d, true
		}
	}
	return Display{}, false
}

// WailsPosition converts absolute virtual-desktop coordinates into the
// coordinates Wails v2's runtime.WindowSetPosition expects. Verified
// against the wails v2.13.0 sources, WindowSetPosition is NOT absolute:
// on Linux it offsets by the origin of the monitor the window is
// currently on (gtk gdk_monitor_get_geometry), and on Windows by the
// origin of the current monitor's WORK AREA (MONITORINFO.rcWork). cur
// must therefore be the display the window is on right now (see
// DisplayForWindow, fed with runtime.WindowGetPosition, which IS
// absolute on both platforms). macOS is not handled here: its
// conversion needs the Cocoa screen height, so the app moves the
// window natively instead (see native.MoveWindow).
func WailsPosition(goos string, cur Display, absX, absY int) (x, y int) {
	if goos == "windows" {
		return absX - cur.Work.X, absY - cur.Work.Y
	}
	return absX - cur.Rect.X, absY - cur.Rect.Y
}

// clamp limits v to [lo, hi]; lo wins when the range is inverted (a
// window larger than the display pins to the display origin).
func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}
