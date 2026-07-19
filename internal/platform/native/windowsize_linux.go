//go:build linux

package native

/*
#cgo linux pkg-config: gtk+-3.0
#include "launchmint_linux.h"
*/
import "C"

import "time"

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
