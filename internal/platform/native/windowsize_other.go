//go:build !linux

package native

// SetWindowSize is the non-linux stub: no GTK fixed-size floor exists
// there, so the caller's fallback -- the Wails runtime WindowSetSize,
// which resizes freely on darwin (setContentSize) and windows
// (SetWindowPos) regardless of the non-resizable style -- is the whole
// mechanism. Always false.
func SetWindowSize(int, int) bool { return false }
