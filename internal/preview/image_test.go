package preview

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/bmp"

	"github.com/stretchr/testify/require"
)

// testImage builds a gradient image so encoders have real content.
func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	return img
}

// writeImage encodes img at path in the format its extension names.
func writeImage(tb testing.TB, path string, img image.Image) {
	tb.Helper()
	f, err := os.Create(path)
	require.NoError(tb, err)
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		require.NoError(tb, png.Encode(f, img))
	case ".jpg", ".jpeg":
		require.NoError(tb, jpeg.Encode(f, img, &jpeg.Options{Quality: 90}))
	case ".gif":
		require.NoError(tb, gif.Encode(f, img, nil))
	case ".bmp":
		require.NoError(tb, bmp.Encode(f, img))
	default:
		tb.Fatalf("no encoder for %s", path)
	}
}

func TestThumbnailSmallPNGKeepsSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.png")
	writeImage(t, path, testImage(64, 48))
	fi, err := os.Stat(path)
	require.NoError(t, err)

	ip, err := Thumbnail(context.Background(), path, 800)
	require.NoError(t, err)
	require.Equal(t, 64, ip.W)
	require.Equal(t, 48, ip.H)
	require.Equal(t, 64, ip.OrigW)
	require.Equal(t, 48, ip.OrigH)
	require.Equal(t, fi.Size(), ip.SizeBytes)
	require.True(t, strings.HasPrefix(ip.DataURI, "data:image/png;base64,"), ip.DataURI[:40])
}

func TestThumbnailDownscalesJPEGPreservingAspect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.jpg")
	writeImage(t, path, testImage(400, 300))

	ip, err := Thumbnail(context.Background(), path, 100)
	require.NoError(t, err)
	require.Equal(t, 100, ip.W)
	require.Equal(t, 75, ip.H)
	require.Equal(t, 400, ip.OrigW)
	require.Equal(t, 300, ip.OrigH)
	require.True(t, strings.HasPrefix(ip.DataURI, "data:image/jpeg;base64,"), "JPEG sources re-encode as JPEG")
}

func TestThumbnailGIFAndBMPEncodeAsPNG(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"anim.gif", "old.bmp"} {
		path := filepath.Join(dir, name)
		writeImage(t, path, testImage(120, 40))
		ip, err := Thumbnail(context.Background(), path, 60)
		require.NoError(t, err, name)
		require.Equal(t, 60, ip.W, name)
		require.Equal(t, 20, ip.H, name)
		require.True(t, strings.HasPrefix(ip.DataURI, "data:image/png;base64,"), name)
	}
}

func TestThumbnailRejectsNonImageExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("text"), 0o644))
	_, err := Thumbnail(context.Background(), path, 800)
	require.ErrorIs(t, err, errNotAnImage)
}

func TestThumbnailRejectsOversizeSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxImageSourceBytes+1)) // sparse; no real IO
	require.NoError(t, f.Close())
	_, err = Thumbnail(context.Background(), path, 800)
	require.ErrorContains(t, err, "preview limit")
}

// hugePNGHeader crafts a valid PNG signature + IHDR chunk declaring a
// 50000x50000 image (2500 megapixels) so the pixel gate fires without
// a real decompression bomb on disk.
func hugePNGHeader() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 50000)
	binary.BigEndian.PutUint32(ihdr[4:], 50000)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: truecolor
	var lenb [4]byte
	binary.BigEndian.PutUint32(lenb[:], 13)
	buf.Write(lenb[:])
	buf.WriteString("IHDR")
	buf.Write(ihdr)
	crc := crc32.NewIEEE()
	crc.Write([]byte("IHDR"))
	crc.Write(ihdr)
	var crcb [4]byte
	binary.BigEndian.PutUint32(crcb[:], crc.Sum32())
	buf.Write(crcb[:])
	return buf.Bytes()
}

func TestThumbnailRejectsOverMegapixelImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bomb.png")
	require.NoError(t, os.WriteFile(path, hugePNGHeader(), 0o644))
	_, err := Thumbnail(context.Background(), path, 800)
	require.ErrorContains(t, err, "megapixel")
}

func TestThumbnailRejectsCorruptImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.png")
	require.NoError(t, os.WriteFile(path, []byte("this is not a png at all"), 0o644))
	_, err := Thumbnail(context.Background(), path, 800)
	require.ErrorContains(t, err, "decoding image header")
}

func TestThumbnailMissingFile(t *testing.T) {
	_, err := Thumbnail(context.Background(), filepath.Join(t.TempDir(), "missing.png"), 800)
	require.Error(t, err)
}

func TestFitWithin(t *testing.T) {
	cases := []struct {
		w, h, max    int
		wantW, wantH int
	}{
		{64, 48, 800, 64, 48},     // small enough: unchanged
		{800, 800, 800, 800, 800}, // exactly at the edge: unchanged
		{400, 300, 100, 100, 75},  // landscape
		{300, 400, 100, 75, 100},  // portrait
		{1000, 10, 100, 100, 1},   // extreme aspect
		{10, 1000, 100, 1, 100},   // extreme aspect, portrait
		{5000, 1, 100, 100, 1},    // rounding floor never hits zero
		{640, 480, 0, 640, 480},   // non-positive max: unchanged
	}
	for _, tc := range cases {
		w, h := fitWithin(tc.w, tc.h, tc.max)
		require.Equal(t, tc.wantW, w, "fitWithin(%d,%d,%d) width", tc.w, tc.h, tc.max)
		require.Equal(t, tc.wantH, h, "fitWithin(%d,%d,%d) height", tc.w, tc.h, tc.max)
	}
}
