package icons

import (
	"bytes"
	"encoding/binary"
)

// Minimal .icns reader for the macOS bundle-icon path: the container
// is an 8-byte header ("icns" + big-endian total length) followed by
// entries (4CC type + big-endian entry length including the 8-byte
// entry header + payload). Modern entries (ic07..ic14, and PNG-bearing
// icp4/icp5/icp6/ic04/ic05) hold complete PNG files, which the
// frontend renders natively -- so the reader PASSES THE BYTES THROUGH
// and never decodes an image. Legacy 32-bit RLE entries (is32/il32/
// ih32/it32 + their masks) and JPEG 2000 payloads are skipped: apps
// whose .icns holds only those (plus CFBundleIconName-only apps, whose
// icon lives in Assets.car) fall back to the builtin glyph -- an
// estimated 5-15% of a typical /Applications scan, dominated by Mac
// App Store / Catalyst-era bundles.

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// icnsNominalSizes maps the icns types that can carry PNG payloads to
// their nominal pixel sizes (the @2x types map to their PHYSICAL pixel
// count: ic11 = 16pt@2x = 32px). Types absent from the table but
// carrying a PNG payload still resolve, ranked after every known size.
var icnsNominalSizes = map[string]int{
	"icp4": 16, "icp5": 32, "icp6": 64,
	"ic04": 16, "ic05": 32,
	"ic07": 128, "ic08": 256, "ic09": 512, "ic10": 1024,
	"ic11": 32, "ic12": 64, "ic13": 256, "ic14": 512,
}

// icnsBestPNG walks an icns container and returns the best PNG entry
// payload for the wanted physical pixel size: the smallest PNG entry
// whose nominal size covers wantSize, else the largest smaller one,
// else a PNG entry of unknown nominal size. Entries larger than
// maxEntry bytes are skipped outright (row icons never ship
// megabyte-class data URIs; the glyph fallback stands), as are
// non-PNG payloads. Malformed structure (bad magic, entry lengths
// escaping the container) answers false.
func icnsBestPNG(data []byte, wantSize, maxEntry int) ([]byte, bool) {
	if len(data) < 8 || string(data[:4]) != "icns" {
		return nil, false
	}
	total := int(binary.BigEndian.Uint32(data[4:8]))
	if total < 8 || total > len(data) {
		return nil, false
	}
	var best []byte
	bestRank := int(^uint(0) >> 1)
	off := 8
	for off+8 <= total {
		typ := string(data[off : off+4])
		length := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if length < 8 || length > total-off {
			return nil, false // corrupt entry length
		}
		payload := data[off+8 : off+length]
		off += length
		if !bytes.HasPrefix(payload, pngMagic) || len(payload) > maxEntry {
			continue
		}
		if rank := icnsRank(icnsNominalSizes[typ], wantSize); rank < bestRank {
			best, bestRank = payload, rank
		}
	}
	return best, best != nil
}

// icnsRank orders PNG entries for a wanted size; lower wins, first
// entry wins ties. Three bands: nominal >= want (closest first), then
// nominal < want (largest first), then unknown nominal (0).
func icnsRank(nominal, want int) int {
	switch {
	case nominal >= want:
		return nominal - want
	case nominal > 0:
		return 1<<20 + (want - nominal)
	default:
		return 1 << 30
	}
}
