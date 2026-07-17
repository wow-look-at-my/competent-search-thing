package tray

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// opaquePixels counts pixels with any visible alpha.
func opaquePixels(p pixmap) int {
	n := 0
	for i := 0; i < len(p.Data); i += 4 {
		if p.Data[i] > 0 {
			n++
		}
	}
	return n
}

func TestIconPixmapsShapeAndSizes(t *testing.T) {
	pms := iconPixmaps()
	require.Len(t, pms, len(iconSizes))
	for i, p := range pms {
		s := iconSizes[i]
		require.Equal(t, int32(s), p.Width)
		require.Equal(t, int32(s), p.Height)
		require.Len(t, p.Data, s*s*4, "tightly packed rows: stride = width*4")
	}
}

func TestIconIsDrawnNotBlank(t *testing.T) {
	for _, p := range iconPixmaps() {
		total := int(p.Width * p.Height)
		visible := opaquePixels(p)
		// The magnifier stroke covers a modest, plausible share of the
		// square: enough to see, nowhere near a filled block.
		require.Greater(t, visible, total/20, "%dpx icon too empty", p.Width)
		require.Less(t, visible, total/2, "%dpx icon too full", p.Width)
	}
}

// TestIconByteOrderARGB pins the wire byte order the AppIndicator
// extension parses (v42 argbToRgba: byte 0 alpha, then R, G, B) and
// the freedesktop spec mandates ("ARGB32 ... in the network byte
// order"): every fully opaque pixel is A=0xff then the light gray in
// R,G,B, and fully transparent pixels are all-zero.
func TestIconByteOrderARGB(t *testing.T) {
	p := iconPixmaps()[0]
	sawOpaque := false
	for i := 0; i < len(p.Data); i += 4 {
		a, r, g, b := p.Data[i], p.Data[i+1], p.Data[i+2], p.Data[i+3]
		switch a {
		case 0:
			require.Zero(t, r, "transparent pixels carry no color")
			require.Zero(t, g)
			require.Zero(t, b)
		default:
			require.EqualValues(t, iconGray, r, "colored bytes follow the alpha byte")
			require.EqualValues(t, iconGray, g)
			require.EqualValues(t, iconGray, b)
			if a == 0xff {
				sawOpaque = true
			}
		}
	}
	require.True(t, sawOpaque, "the stroke core is fully opaque")

	// The corner pixel is background: transparent.
	require.Zero(t, p.Data[0], "top-left corner transparent")
}

func TestIconDeterministic(t *testing.T) {
	a := iconPixmaps()
	b := iconPixmaps()
	require.Equal(t, a, b, "identical output on every call")
}
