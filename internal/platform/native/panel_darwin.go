//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

import (
	"sync"
	"unsafe"

	"github.com/wow-look-at-my/competent-search-thing/internal/tray"
)

// dockIconOnce guards the one-time Dock icon install ConfigurePanel
// performs alongside the panel behavior.
var dockIconOnce sync.Once

// dockIconSize is the rendered Dock icon edge: the Dock displays up to
// 128 pt and scales down, and the analytic rasterizer stays crisp at
// that size.
const dockIconSize = 128

// ConfigurePanel applies Spotlight-style window behavior to the app's
// first NSWindow: join all Spaces (so a summon appears on the ACTIVE
// Space -- Wails only sets the floating level, and a hidden
// always-on-top window otherwise orders back in on the Space it was
// created on), show over fullscreen apps (FullScreenAuxiliary), and
// stay out of the window cycle; hide-on-deactivate stays off because
// the frontend's blur handler owns hiding. It also sets the app's
// Dock/Cmd-Tab icon once, from the same in-code magnifier rasterizer
// the tray uses (premultiplied RGBA at 128 px): the raw-binary
// distribution ships no .app bundle, so without this a running
// process shows the generic executable icon. Returns false when no
// window exists yet, and the caller may retry after one is up.
// Compiled by the darwin CI job but never exercised there (no GUI
// run).
func ConfigurePanel() bool {
	dockIconOnce.Do(func() {
		rgba := tray.MagnifierRGBA(dockIconSize)
		// The shim copies the pixels before returning; passing the Go
		// slice's backing array is within the cgo pointer rules.
		C.csSetDockIcon((*C.uint8_t)(unsafe.Pointer(&rgba[0])), C.int(dockIconSize))
	})
	return C.csConfigurePanel() != 0
}
