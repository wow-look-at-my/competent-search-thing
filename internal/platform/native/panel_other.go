//go:build !darwin

package native

// ConfigurePanel reports that Spotlight-style panel configuration is
// unavailable: only macOS needs it (see panel_darwin.go).
func ConfigurePanel() bool {
	return false
}
