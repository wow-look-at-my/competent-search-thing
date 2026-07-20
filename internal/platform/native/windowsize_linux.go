//go:build linux

package native

/*
#cgo linux pkg-config: gtk+-3.0
#include "launchmint_linux.h"
*/
import "C"

import (
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// windowSizeTimeout bounds the GTK-thread dispatch that applies a live
// window resize: the loop services idle callbacks between frames, so
// this only ever expires when the GTK loop is not running (headless
// tests) or wedged -- the caller then falls back to the Wails runtime
// call.
const windowSizeTimeout = 2 * time.Second

// SetWindowSize resizes the app's GTK toplevel to w x h on the GTK
// main thread, moving the non-resizable window's fixed-size floor
// with it (gtk_window_set_default_size + gtk_window_resize -- the
// Wails runtime's bare gtk_window_resize cannot shrink a
// DisableResize window below its construction-time default; see
// cs_set_window_size in launchmint_linux.c for the GTK3 mechanism).
// false when the GTK loop or the toplevel is unreachable; the caller
// falls back to the Wails runtime resize.
func SetWindowSize(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	ok := false
	done := runOnGTKThread(func() {
		ok = C.cs_set_window_size(C.int(w), C.int(h)) != 0
	}, windowSizeTimeout)
	return done && ok
}

// WindowWorkArea reports the usable area of the monitor the bar
// window currently sits on, straight from the toolkit
// (gdk_monitor_get_workarea on the GTK main thread; see
// cs_get_workarea). It is the clamp-to-screen source of last resort:
// the ONLY probe that answers on Wayland, where the app's X-based
// display list is unavailable. ok=false when the GTK loop, the
// toplevel, or its monitor is unreachable (headless, not yet
// realized) -- the caller then skips clamping.
func WindowWorkArea() (platform.Rect, bool) {
	var x, y, w, h C.int
	ok := false
	done := runOnGTKThread(func() {
		ok = C.cs_get_workarea(&x, &y, &w, &h) != 0
	}, windowSizeTimeout)
	if !done || !ok {
		return platform.Rect{}, false
	}
	return platform.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}, true
}
