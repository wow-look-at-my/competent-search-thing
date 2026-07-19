//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include "platform_darwin.h"
*/
import "C"

// ConfigurePanel applies Spotlight-style window behavior to the app's
// first NSWindow: join all Spaces (so a summon appears on the ACTIVE
// Space -- Wails only sets the floating level, and a hidden
// always-on-top window otherwise orders back in on the Space it was
// created on), show over fullscreen apps (FullScreenAuxiliary), and
// stay out of the window cycle; hide-on-deactivate stays off because
// the frontend's blur handler owns hiding. Returns false when no
// window exists yet, and the caller may retry after one is up.
// Compiled by the darwin CI job but never exercised there (no GUI
// run).
func ConfigurePanel() bool {
	return C.csConfigurePanel() != 0
}
