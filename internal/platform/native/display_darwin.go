//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

import (
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// maxDisplays bounds the shim's output array; nobody drives more.
const maxDisplays = 16

// CursorDisplays returns the cursor position and monitor layout in
// top-left-origin virtual-desktop coordinates (the Cocoa shim converts
// from AppKit's bottom-left-origin global coordinates; the cursor from
// CGEventGetLocation is already top-left). Work carries each screen's
// visibleFrame (menu bar and dock excluded). ok is false when the
// cursor or screen list cannot be read. Untested in CI (linux/amd64
// only).
func CursorDisplays() (cx, cy int, ds []platform.Display, ok bool) {
	var x, y C.double
	if C.csCursorPos(&x, &y) == 0 {
		return 0, 0, nil, false
	}
	var out [maxDisplays]C.csDisplay
	n := int(C.csGetDisplays(&out[0], maxDisplays))
	if n == 0 {
		return 0, 0, nil, false
	}
	ds = make([]platform.Display, 0, n)
	for i := 0; i < n; i++ {
		d := out[i]
		ds = append(ds, platform.Display{
			Rect:    platform.Rect{X: int(d.x), Y: int(d.y), W: int(d.w), H: int(d.h)},
			Work:    platform.Rect{X: int(d.wx), Y: int(d.wy), W: int(d.ww), H: int(d.wh)},
			Primary: d.primary != 0,
		})
	}
	return int(x), int(y), ds, true
}
