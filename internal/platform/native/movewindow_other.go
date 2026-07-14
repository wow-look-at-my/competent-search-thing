//go:build !darwin

package native

// MoveWindow reports that native window moves are unavailable: only
// macOS needs them (see movewindow_darwin.go); Linux and Windows
// position through the Wails runtime after translating coordinates
// with platform.WailsPosition.
func MoveWindow(x, y int) bool {
	return false
}
