package icons

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

/* --- test-only icns builder ----------------------------------------- */

// icnsEntry is one container entry under construction.
type icnsEntry struct {
	typ     string
	payload []byte
}

// buildIcns assembles a container from entries, with the header length
// covering exactly the built bytes.
func buildIcns(t *testing.T, entries ...icnsEntry) []byte {
	t.Helper()
	var body []byte
	for _, e := range entries {
		require.Len(t, e.typ, 4)
		body = append(body, e.typ...)
		body = binary.BigEndian.AppendUint32(body, uint32(8+len(e.payload)))
		body = append(body, e.payload...)
	}
	out := []byte("icns")
	out = binary.BigEndian.AppendUint32(out, uint32(8+len(body)))
	return append(out, body...)
}

// tinyPNG encodes a real solid-color PNG via image/png so payloads
// carry the genuine magic and decode structure.
func tinyPNG(t *testing.T, size int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// jp2Payload mimics a JPEG 2000 signature box (the non-PNG modern
// payload icnsBestPNG must skip).
func jp2Payload() []byte {
	return []byte{0x00, 0x00, 0x00, 0x0C, 'j', 'P', ' ', ' ', 0x0D, 0x0A, 0x87, 0x0A, 1, 2, 3}
}

/* --- tests ---------------------------------------------------------- */

func TestIcnsBestPNGPicksCoveringSize(t *testing.T) {
	small := tinyPNG(t, 2, color.White)
	mid := tinyPNG(t, 3, color.Black)
	big := tinyPNG(t, 4, color.White)
	data := buildIcns(t,
		icnsEntry{"ic11", small}, // nominal 32
		icnsEntry{"ic07", mid},   // nominal 128
		icnsEntry{"ic08", big},   // nominal 256
	)
	got, ok := icnsBestPNG(data, 64, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, mid, got, "smallest nominal >= 64 is ic07 (128)")

	got, ok = icnsBestPNG(data, 16, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, small, got, "smallest nominal >= 16 is ic11 (32)")

	got, ok = icnsBestPNG(data, 512, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, big, got, "nothing covers 512: the largest below wins")
}

func TestIcnsBestPNGSkipsNonPNGEntries(t *testing.T) {
	wanted := tinyPNG(t, 2, color.White)
	data := buildIcns(t,
		icnsEntry{"TOC ", []byte{0, 1, 2, 3}},                    // table of contents
		icnsEntry{"is32", []byte{1, 2, 3, 4}},                    // legacy RLE
		icnsEntry{"s8mk", bytes.Repeat([]byte{0xFF}, 16)},        // legacy mask
		icnsEntry{"ic07", jp2Payload()},                          // JPEG 2000 payload
		icnsEntry{"icnV", []byte{4, 0, 0, 0}},                    // version metadata
		icnsEntry{"ic12", wanted},                                // the one real PNG
		icnsEntry{"name", append([]byte("junkjunk"), 0x89, 'P')}, // not a PNG either
	)
	got, ok := icnsBestPNG(data, 64, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, wanted, got)
}

func TestIcnsBestPNGLegacyOnlyMisses(t *testing.T) {
	data := buildIcns(t,
		icnsEntry{"is32", []byte{1, 2, 3}},
		icnsEntry{"il32", []byte{4, 5, 6}},
		icnsEntry{"ic07", jp2Payload()},
	)
	_, ok := icnsBestPNG(data, 64, maxIcnsEntryBytes)
	require.False(t, ok, "no PNG payload anywhere: the glyph fallback stands")
}

func TestIcnsBestPNGUnknownTypeIsLastResort(t *testing.T) {
	unknown := tinyPNG(t, 2, color.White)
	known := tinyPNG(t, 3, color.Black)
	both := buildIcns(t,
		icnsEntry{"zzzz", unknown}, // PNG magic under an unknown 4CC
		icnsEntry{"icp4", known},   // nominal 16, below want
	)
	got, ok := icnsBestPNG(both, 64, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, known, got, "a known-but-small size outranks an unknown one")

	onlyUnknown := buildIcns(t, icnsEntry{"zzzz", unknown})
	got, ok = icnsBestPNG(onlyUnknown, 64, maxIcnsEntryBytes)
	require.True(t, ok)
	require.Equal(t, unknown, got, "an unknown-size PNG still beats nothing")
}

func TestIcnsBestPNGEntryByteCap(t *testing.T) {
	big := tinyPNG(t, 16, color.White)
	small := tinyPNG(t, 2, color.Black)
	data := buildIcns(t,
		icnsEntry{"ic07", big},   // preferred size but over the cap below
		icnsEntry{"icp5", small}, // nominal 32, under the cap
	)
	got, ok := icnsBestPNG(data, 64, len(big)-1)
	require.True(t, ok)
	require.Equal(t, small, got, "over-cap entries are skipped, not decoded or shipped")

	_, ok = icnsBestPNG(data, 64, len(small)-1)
	require.False(t, ok, "everything over the cap: miss")
}

func TestIcnsBestPNGCorruption(t *testing.T) {
	good := buildIcns(t, icnsEntry{"ic07", tinyPNG(t, 2, color.White)})
	cases := map[string][]byte{
		"empty":     {},
		"short":     []byte("icns"),
		"bad magic": append([]byte("nope"), good[4:]...),
		"declared total over data": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(d[4:8], uint32(len(d)+1))
			return d
		}(),
		"total below header": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(d[4:8], 4)
			return d
		}(),
		"entry length escapes container": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(d[12:16], uint32(len(d))) // entry len > remaining
			return d
		}(),
		"entry length below header": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint32(d[12:16], 4)
			return d
		}(),
	}
	for name, data := range cases {
		_, ok := icnsBestPNG(data, 64, maxIcnsEntryBytes)
		require.False(t, ok, "case %q", name)
	}
}

func TestIcnsTrailingBytesTolerated(t *testing.T) {
	// Bytes past the declared total are ignored (some tools pad).
	data := append(buildIcns(t, icnsEntry{"ic07", tinyPNG(t, 2, color.White)}), 0xAA, 0xBB)
	_, ok := icnsBestPNG(data, 64, maxIcnsEntryBytes)
	require.True(t, ok)
}
