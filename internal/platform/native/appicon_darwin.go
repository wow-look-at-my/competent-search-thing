//go:build darwin

package native

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics
#include <stdlib.h>
#include "platform_darwin.h"
*/
import "C"

import "unsafe"

// AppIconPNG rasterizes the icon macOS itself displays for path --
// [NSWorkspace iconForFile:], the same image Launchpad/Finder/the
// Dock show, Assets.car asset catalogs included -- to a sizePx x
// sizePx PNG. nil on any failure (empty/missing path, no image,
// encode failure), never an error: the consumer (internal/icons'
// Options.NativeAppIcon seam) treats nil as "no icon" and keeps its
// glyph fallback. Runs entirely off the main thread on purpose (see
// csAppIconPNG in platform_darwin.h): icon resolution happens on the
// app's ResolveIcons goroutine and inside the darwin unit-test
// binary, neither of which pumps the main queue. Compiled AND
// exercised by the darwin CI job (internal/icons real_darwin_test.go
// resolves the runner's real /Applications through it).
func AppIconPNG(path string, sizePx int) []byte {
	if path == "" || sizePx <= 0 {
		return nil
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var n C.int
	buf := C.csAppIconPNG(cpath, C.int(sizePx), &n)
	if buf == nil || n <= 0 {
		return nil
	}
	defer C.free(buf)
	return C.GoBytes(buf, n)
}
