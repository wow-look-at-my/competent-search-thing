//go:build linux

package native

/*
#cgo linux pkg-config: glib-2.0
#include <glib.h>

static void cs_set_prgname(void) { g_set_prgname("competent-search-thing"); }
*/
import "C"

// Wails never sets a program name (options.Linux.ProgramName is
// unset), so g_get_prgname() is NULL and the window gets NO X11
// WM_CLASS and NO Wayland app_id -- which breaks taskbar matching,
// the startup-notification id prefix, and any per-app compositor
// rule. Package init runs at import time, strictly before wails
// creates the window, so stamping the prgname here restores both.
// (The CI screenshot script finds the window by title + geometry,
// not class, so it is unaffected.)
func init() {
	C.cs_set_prgname()
}
