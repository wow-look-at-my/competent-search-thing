//go:build !darwin

package native

// AppIconPNG reports that native app-icon rasterization does not
// exist off macOS (see appicon_darwin.go): only darwin has an OS icon
// service to ask, and the .app bundle refs the seam serves only occur
// there. The stub keeps internal/app's unconditional seam wiring
// compiling everywhere; internal/icons treats a nil answer as "no
// native icon" and keeps its glyph fallback.
func AppIconPNG(path string, sizePx int) []byte {
	return nil
}
