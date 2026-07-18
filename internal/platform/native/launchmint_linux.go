//go:build linux

package native

/*
#cgo linux pkg-config: gtk+-3.0 gdk-3.0 gio-2.0 gio-unix-2.0 wayland-client
#include <stdint.h>
#include <stdlib.h>
#include "launchmint_linux.h"
*/
import "C"

import (
	"runtime/cgo"
	"time"
	"unsafe"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
)

// csRunOnGtk is the g_idle_add trampoline target: it runs one queued
// Go closure on the GTK main thread and frees its handle.
//
//export csRunOnGtk
func csRunOnGtk(handle C.uintptr_t) {
	h := cgo.Handle(handle)
	f := h.Value().(func())
	f()
	h.Delete()
}

// runOnGTKThread runs f on the thread that owns the default GLib main
// context (where wails runs gtk_main): inline when the caller already
// is that thread, otherwise via g_idle_add, waiting up to timeout for
// the idle callback. false means the loop never ran f in time (not
// running, or wedged); f may still run later -- its handle stays
// registered until the callback fires -- the caller just stops
// waiting.
func runOnGTKThread(f func(), timeout time.Duration) bool {
	if C.cs_on_gtk_thread() != 0 {
		f()
		return true
	}
	done := make(chan struct{}, 1)
	h := cgo.NewHandle(func() {
		f()
		done <- struct{}{} // buffered: never blocks an abandoned wait
	})
	C.cs_idle_add(C.uintptr_t(h))
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// MintLaunchCredential mints one launch credential on the GTK main
// thread (see launchmint_linux.c for the per-backend mechanism),
// describing the launch with the resolved handler's desktop id when
// there is one (GLib >= 2.76 requires a real GAppInfo; an empty id
// synthesizes one) and waiting at most timeout for the GTK loop to
// service the request. A none-credential comes back when the loop is
// not running, the wait times out, or the session has no minting
// backend (non-GNOME Wayland without xdg-activation).
func MintLaunchCredential(timeout time.Duration, desktopID string) launch.Credential {
	type mintOut struct {
		id   string
		kind C.int
	}
	ch := make(chan mintOut, 1)
	ok := runOnGTKThread(func() {
		// Allocated and freed inside the closure, which runs (and
		// therefore cleans up) even when the caller stopped waiting.
		cid := C.CString(desktopID)
		defer C.free(unsafe.Pointer(cid))
		var res C.CsMintResult
		C.cs_mint(cid, &res)
		out := mintOut{kind: res.kind}
		if res.id != nil {
			out.id = C.GoString(res.id)
			C.cs_mint_free(res.id)
		}
		ch <- out // buffered: safe when the caller timed out
	}, timeout)
	if !ok {
		return launch.Credential{Kind: launch.KindNone}
	}
	out := <-ch
	if out.id == "" {
		return launch.Credential{Kind: launch.KindNone}
	}
	switch out.kind {
	case C.CS_MINT_X11_SN:
		return launch.Credential{ID: out.id, Kind: launch.KindX11SN}
	case C.CS_MINT_WAYLAND_GDK:
		return launch.Credential{ID: out.id, Kind: launch.KindWaylandGDK}
	case C.CS_MINT_WAYLAND_XDG:
		return launch.Credential{ID: out.id, Kind: launch.KindWaylandXDG}
	default:
		return launch.Credential{Kind: launch.KindNone}
	}
}

// PrepareLaunch schedules the one-time native launch preparation on
// the GTK main thread: on Wayland it creates the keyboard listener
// whose input serials authenticate hand-rolled xdg-activation tokens
// (serials only arrive as keys are pressed, so the listener must
// exist BEFORE the first launch); on X11 it is a no-op.
// Fire-and-forget -- Startup must never block on the GTK loop.
func PrepareLaunch() {
	h := cgo.NewHandle(func() { C.cs_prepare_wayland() })
	C.cs_idle_add(C.uintptr_t(h))
}
