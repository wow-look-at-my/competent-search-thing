package preview

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoder registration
	"image/jpeg"
	"image/png"
	"os"

	_ "golang.org/x/image/bmp" // decoder registration
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder registration
)

// Thumbnail limits.
const (
	// maxImageSourceBytes rejects sources larger than 32 MiB.
	maxImageSourceBytes = 32 << 20
	// maxImagePixels rejects sources over 40 megapixels before the
	// full decode (a decompression-bomb guard).
	maxImagePixels = 40_000_000
	// jpegQuality re-encodes JPEG sources.
	jpegQuality = 80
)

var errNotAnImage = errors.New("not a supported image type")

// Thumbnail decodes the image at path and returns a downscaled
// preview: the longest edge is at most maxEdge (aspect preserved,
// never upscaled), re-encoded as JPEG (quality 80) when the source was
// JPEG and PNG otherwise, delivered as a base64 data URI. The gate
// order is cheap-first: extension, source size, DecodeConfig
// dimensions, then the full decode -- which runs in a goroutine raced
// against ctx, so a hung or huge decode is abandoned at the deadline.
func Thumbnail(ctx context.Context, path string, maxEdge int) (*ImagePreview, error) {
	if !isImageExt(path) {
		return nil, errNotAnImage
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxImageSourceBytes {
		return nil, fmt.Errorf("image is %s (over the %s preview limit)",
			humanSize(fi.Size()), humanSize(maxImageSourceBytes))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("decoding image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
		return nil, fmt.Errorf("image is %dx%d (over the %d megapixel preview limit)",
			cfg.Width, cfg.Height, maxImagePixels/1_000_000)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}

	type decoded struct {
		img image.Image
		err error
	}
	ch := make(chan decoded, 1)
	go func() {
		img, _, err := image.Decode(f)
		ch <- decoded{img: img, err: err}
	}()
	var img image.Image
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case d := <-ch:
		if d.err != nil {
			return nil, fmt.Errorf("decoding image: %w", d.err)
		}
		img = d.img
	}

	b := img.Bounds()
	origW, origH := b.Dx(), b.Dy()
	w, h := fitWithin(origW, origH, maxEdge)
	if w != origW || h != origH {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}

	var buf bytes.Buffer
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality})
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, fmt.Errorf("encoding thumbnail: %w", err)
	}
	uri := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return &ImagePreview{
		DataURI:   uri,
		W:         w,
		H:         h,
		OrigW:     origW,
		OrigH:     origH,
		SizeBytes: fi.Size(),
	}, nil
}

// fitWithin scales (w, h) down, preserving aspect, so the longest edge
// is at most maxEdge. Images already small enough are unchanged; the
// result never drops below 1x1.
func fitWithin(w, h, maxEdge int) (int, int) {
	if maxEdge <= 0 || (w <= maxEdge && h <= maxEdge) {
		return w, h
	}
	long := w
	if h > long {
		long = h
	}
	scale := float64(maxEdge) / float64(long)
	sw := int(float64(w)*scale + 0.5)
	sh := int(float64(h)*scale + 0.5)
	if sw < 1 {
		sw = 1
	}
	if sh < 1 {
		sh = 1
	}
	return sw, sh
}
