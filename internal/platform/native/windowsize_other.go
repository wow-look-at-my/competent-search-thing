//go:build !linux

package native

import "github.com/wow-look-at-my/competent-search-thing/internal/platform"

// SetWindowSize is the non-linux stub: no GTK fixed-size floor exists
// there, so the caller's fallback -- the Wails runtime WindowSetSize,
// which resizes freely on darwin (setContentSize) and windows
// (SetWindowPos) regardless of the non-resizable style -- is the whole
// mechanism. Always false.
func SetWindowSize(int, int) bool { return false }

// WindowWorkArea is the non-linux stub: darwin and windows report
// per-display work areas (NSScreen visibleFrame / MONITORINFO.rcWork)
// through CursorDisplays' Work rects, so the clamp never needs this
// fallback there. Always false.
func WindowWorkArea() (platform.Rect, bool) { return platform.Rect{}, false }
