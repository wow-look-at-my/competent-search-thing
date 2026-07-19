package tray

// MagnifierRGBA renders the magnifier icon at size s as PREMULTIPLIED
// RGBA pixels, row-major -- the layout NSBitmapImageRep interprets by
// default, consumed by the macOS Dock icon (internal/platform/native
// panel_darwin.go): the raw-binary distribution ships no .app bundle
// (and so no .icns), so the running process sets its Dock/Cmd-Tab
// icon from the same analytic rasterizer the tray pixmaps use, no
// image assets involved. The tray's own SNI pixmaps stay straight
// alpha ARGB (the wire format); this is the one premultiplied
// variant.
func MagnifierRGBA(s int) []byte {
	pm := drawMagnifier(s)
	out := make([]byte, len(pm.Data))
	for i := 0; i < len(pm.Data); i += 4 {
		a := uint32(pm.Data[i])
		out[i+0] = byte(uint32(pm.Data[i+1]) * a / 255) // R
		out[i+1] = byte(uint32(pm.Data[i+2]) * a / 255) // G
		out[i+2] = byte(uint32(pm.Data[i+3]) * a / 255) // B
		out[i+3] = byte(a)
	}
	return out
}
