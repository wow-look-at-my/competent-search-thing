package tray

import "math"

// iconSizes are the pixmap sizes shipped: the extension picks the
// smallest pixmap >= its scaled panel size (16/22/24 px panels at
// scale 1, doubled under HiDPI), so 22/24 cover 1x panels and 48
// covers 2x.
var iconSizes = []int{22, 24, 48}

// iconPixmaps renders the tray icon -- a simple magnifier, drawn in
// code so the binary needs no image assets -- at every size in
// iconSizes, encoded as the SNI wire pixmaps (ARGB32, network byte
// order, straight alpha). The drawing is light-on-transparent
// (#f2f2f2), readable on the dark panels every GNOME theme ships.
// Output is deterministic: pure math over pixel centers.
func iconPixmaps() []pixmap {
	out := make([]pixmap, 0, len(iconSizes))
	for _, s := range iconSizes {
		out = append(out, drawMagnifier(s))
	}
	return out
}

// Icon geometry, as fractions of the square size: a lens (ring)
// centered upper-left and a round-capped handle running to the
// lower-right corner area.
const (
	iconGray    = 0xf2 // the light gray all opaque pixels share
	lensCenter  = 0.40 // lens center x = y
	lensRadius  = 0.26 // lens outer ring center-line radius
	strokeWidth = 0.12 // ring and handle thickness
	handleEnd   = 0.82 // handle far end x = y
	minStrokePx = 1.6  // stroke floor so tiny sizes stay visible
	aaBand      = 1.0  // antialiasing band, in pixels
)

// drawMagnifier rasterizes the magnifier at size s. Coverage per
// pixel comes from the distance to the two primitives (ring +
// segment), smoothed over a one-pixel band -- cheap analytic
// antialiasing with no image/draw compositing needed.
func drawMagnifier(s int) pixmap {
	fs := float64(s)
	cx, cy := lensCenter*fs, lensCenter*fs
	r := lensRadius * fs
	half := math.Max(strokeWidth*fs, minStrokePx) / 2

	// The handle starts on the ring's outer edge along the diagonal
	// and ends short of the corner.
	diag := math.Sqrt2 / 2
	hx0, hy0 := cx+(r+half)*diag, cy+(r+half)*diag
	hx1, hy1 := handleEnd*fs, handleEnd*fs

	data := make([]byte, s*s*4)
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5

			// Distance from the ring center line.
			dRing := math.Abs(math.Hypot(px-cx, py-cy) - r)
			// Distance from the handle segment.
			dHandle := segmentDistance(px, py, hx0, hy0, hx1, hy1)

			d := math.Min(dRing, dHandle)
			a := coverage(d, half)
			if a == 0 {
				continue
			}
			i := (y*s + x) * 4
			data[i+0] = a        // A
			data[i+1] = iconGray // R
			data[i+2] = iconGray // G
			data[i+3] = iconGray // B
		}
	}
	return pixmap{Width: int32(s), Height: int32(s), Data: data}
}

// coverage maps a center-line distance to an alpha byte: opaque
// inside the stroke, fading linearly to transparent across aaBand.
func coverage(d, half float64) byte {
	switch {
	case d <= half-aaBand/2:
		return 0xff
	case d >= half+aaBand/2:
		return 0
	default:
		f := (half + aaBand/2 - d) / aaBand
		return byte(math.Round(f * 0xff))
	}
}

// segmentDistance returns the distance from point (px,py) to the
// segment (x0,y0)-(x1,y1).
func segmentDistance(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	lenSq := dx*dx + dy*dy
	t := 0.0
	if lenSq > 0 {
		t = ((px-x0)*dx + (py-y0)*dy) / lenSq
		t = math.Max(0, math.Min(1, t))
	}
	return math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
}
