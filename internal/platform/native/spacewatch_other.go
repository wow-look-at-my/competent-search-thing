//go:build !darwin

package native

// WatchSpaceChanges is darwin-only (Spaces are a macOS concept); on
// every other platform nothing installs and the app never arms the
// dismiss -- the app-layer seam is nil there anyway (see
// internal/app's defaultSpaceWatch), this stub only keeps the symbol
// buildable everywhere.
func WatchSpaceChanges(func()) bool { return false }
