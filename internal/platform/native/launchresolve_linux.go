//go:build linux

package native

/*
#cgo linux pkg-config: gio-2.0 gio-unix-2.0
#include <stdlib.h>
#include <gio/gio.h>
#include <gio/gdesktopappinfo.h>

static GDesktopAppInfo *cs_as_desktop_app(GAppInfo *info) {
	return (info != NULL && G_IS_DESKTOP_APP_INFO(info)) ? G_DESKTOP_APP_INFO(info) : NULL;
}
static GAppInfo *cs_as_app_info(GDesktopAppInfo *d) {
	return G_APP_INFO(d);
}
static char *cs_desktop_exec(GDesktopAppInfo *d) {
	return g_desktop_app_info_get_string(d, G_KEY_FILE_DESKTOP_KEY_EXEC);
}
static int cs_desktop_dbus_activatable(GDesktopAppInfo *d) {
	return g_desktop_app_info_get_boolean(d, G_KEY_FILE_DESKTOP_KEY_DBUS_ACTIVATABLE) ? 1 : 0;
}
static int cs_desktop_startup_notify(GDesktopAppInfo *d) {
	return g_desktop_app_info_get_boolean(d, G_KEY_FILE_DESKTOP_KEY_STARTUP_NOTIFY) ? 1 : 0;
}
static int cs_desktop_terminal(GDesktopAppInfo *d) {
	return g_desktop_app_info_get_boolean(d, G_KEY_FILE_DESKTOP_KEY_TERMINAL) ? 1 : 0;
}
*/
import "C"

import (
	"unsafe"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
)

// ResolveHandler looks up the default application for a launch
// target through gio: by URI scheme for URLs, by guessed content
// type for files (name-based; directories use inode/directory).
// These are thread-safe cache reads -- no GTK thread involved.
// ok=false (no handler registered) sends the launch down the
// xdg-open fallback.
func ResolveHandler(t launch.Target) (launch.Handler, bool) {
	var info *C.GAppInfo
	if t.IsURL {
		cs := C.CString(t.Scheme)
		defer C.free(unsafe.Pointer(cs))
		info = C.g_app_info_get_default_for_uri_scheme(cs)
	} else {
		ctype := contentTypeFor(t)
		if ctype == nil {
			return launch.Handler{}, false
		}
		defer C.g_free(C.gpointer(unsafe.Pointer(ctype)))
		info = C.g_app_info_get_default_for_type(ctype, C.FALSE)
	}
	if info == nil {
		return launch.Handler{}, false
	}
	defer C.g_object_unref(C.gpointer(unsafe.Pointer(info)))
	return handlerFromAppInfo(info), true
}

// HandlerByDesktopID resolves one desktop entry by its id
// ("code.desktop") -- the run_command launch path, where the builtin
// provider already knows which entry it offers.
func HandlerByDesktopID(id string) (launch.Handler, bool) {
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	d := C.g_desktop_app_info_new(cid)
	if d == nil {
		return launch.Handler{}, false
	}
	defer C.g_object_unref(C.gpointer(unsafe.Pointer(d)))
	return handlerFromAppInfo(C.cs_as_app_info(d)), true
}

// contentTypeFor guesses a path target's content type:
// inode/directory for directories, else g_content_type_guess on the
// file name alone (extension-driven, no data sniffing -- fast, and
// exactly what handler resolution needs). Returns a g_malloc'd
// string the caller g_frees.
func contentTypeFor(t launch.Target) *C.gchar {
	if t.IsDir {
		cdir := C.CString("inode/directory")
		defer C.free(unsafe.Pointer(cdir))
		return C.g_strdup((*C.gchar)(cdir))
	}
	cname := C.CString(t.Raw)
	defer C.free(unsafe.Pointer(cname))
	return C.g_content_type_guess((*C.gchar)(cname), nil, 0, nil)
}

// handlerFromAppInfo extracts the launch-relevant fields of one
// GAppInfo (the desktop-entry extras only when it is one).
func handlerFromAppInfo(info *C.GAppInfo) launch.Handler {
	var h launch.Handler
	if id := C.g_app_info_get_id(info); id != nil {
		h.DesktopID = C.GoString(id)
	}
	if exe := C.g_app_info_get_executable(info); exe != nil {
		h.Exe = C.GoString(exe)
	}
	d := C.cs_as_desktop_app(info)
	if d == nil {
		return h
	}
	if exec := C.cs_desktop_exec(d); exec != nil {
		h.Exec = C.GoString(exec)
		C.g_free(C.gpointer(unsafe.Pointer(exec)))
	}
	if wm := C.g_desktop_app_info_get_startup_wm_class(d); wm != nil {
		h.WMClass = C.GoString(wm)
	}
	h.DBusActivatable = C.cs_desktop_dbus_activatable(d) != 0
	h.StartupNotify = C.cs_desktop_startup_notify(d) != 0
	h.Terminal = C.cs_desktop_terminal(d) != 0
	return h
}
