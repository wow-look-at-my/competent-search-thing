package tray

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMagnifierRGBA(t *testing.T) {
	const size = 128
	px := MagnifierRGBA(size)
	require.Len(t, px, size*size*4)

	opaque, blank := 0, 0
	for i := 0; i < len(px); i += 4 {
		r, g, b, a := px[i], px[i+1], px[i+2], px[i+3]
		// Premultiplied invariant: no channel may exceed alpha.
		require.LessOrEqual(t, r, a, "premultiplied red at %d", i)
		require.LessOrEqual(t, g, a, "premultiplied green at %d", i)
		require.LessOrEqual(t, b, a, "premultiplied blue at %d", i)
		switch a {
		case 0xFF:
			opaque++
			require.Equal(t, byte(iconGray), r, "fully opaque pixels keep the icon gray")
		case 0:
			blank++
			require.Zero(t, r)
		}
	}
	require.Greater(t, opaque, 0, "the drawing has fully opaque stroke pixels")
	require.Greater(t, blank, size*size/2, "most of the square stays transparent")
}

func TestMagnifierRGBADeterministic(t *testing.T) {
	require.Equal(t, MagnifierRGBA(64), MagnifierRGBA(64))
}
