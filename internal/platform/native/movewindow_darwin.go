//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

// MoveWindow places the app's first NSWindow so its top-left corner
// sits at the given top-left-origin virtual-desktop coordinates. Wails
// v2's WindowSetPosition is relative to the screen the window is
// currently on (on every platform), which cannot express "move to THAT
// display" reliably -- on macOS the app therefore moves the window
// natively. Best effort: false when there is no window yet, and the
// caller falls back to centering. Untested in CI (linux/amd64 only).
func MoveWindow(x, y int) bool {
	return C.csMoveWindow(C.double(x), C.double(y)) != 0
}
