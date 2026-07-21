// The cmap half of the committed-data integrity gate: every rule
// codepoint in data.bin (decoded through this package, the runtime
// reader) must exist in its committed font's character map, so a
// re-vendored font or a hand-edited artifact cannot ship tofu
// glyphs. This is the Go port of the generator's
// frontend/src/fileicons/tools/woff2cmap.mjs (same WOFF2 subset: the
// variable table directory, one brotli stream of concatenated
// tables, cmap formats 4 and 12; glyph-aware format-4 reads so
// .notdef fillers never count as coverage) -- test-only code, kept in
// lockstep with the .mjs by both sides validating the same committed
// artifacts.
package fileicons

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// knownTags is the WOFF2 known-table-tag list (spec section 5.2), in
// flag order; flag value 0x3f means an explicit 4-byte tag follows.
var knownTags = []string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post",
	"cvt ", "fpgm", "glyf", "loca", "prep", "CFF ", "VORG", "EBDT",
	"EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea",
	"vmtx", "BASE", "GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH",
	"CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar",
	"gvar", "hsty", "just", "lcar", "mort", "morx", "opbd", "prop",
	"trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

func readUIntBase128(b []byte, off *int) (int, error) {
	acc := 0
	for i := 0; i < 5; i++ {
		if *off >= len(b) {
			return 0, io.ErrUnexpectedEOF
		}
		v := b[*off]
		*off++
		acc = acc*128 + int(v&0x7f)
		if v&0x80 == 0 {
			return acc, nil
		}
	}
	return 0, fmt.Errorf("UIntBase128 overlong")
}

// cmapCodepoints extracts the set of codepoints a WOFF2 font's cmap
// maps to a real glyph.
func cmapCodepoints(t *testing.T, woff2 []byte) map[int]bool {
	t.Helper()
	require.GreaterOrEqual(t, len(woff2), 48)
	require.Equal(t, uint32(0x774f4632), binary.BigEndian.Uint32(woff2[0:4]), "not a WOFF2 file")
	numTables := int(binary.BigEndian.Uint16(woff2[12:14]))
	off := 48
	type tableEnt struct {
		tag          string
		streamLength int
	}
	tables := make([]tableEnt, 0, numTables)
	for i := 0; i < numTables; i++ {
		require.Less(t, off, len(woff2))
		flags := woff2[off]
		off++
		var tag string
		if flags&0x3f == 0x3f {
			tag = string(woff2[off : off+4])
			off += 4
		} else {
			tag = knownTags[flags&0x3f]
		}
		transform := (flags >> 6) & 0x03
		origLength, err := readUIntBase128(woff2, &off)
		require.NoError(t, err)
		streamLength := origLength
		transformed := transform != 0
		if tag == "glyf" || tag == "loca" {
			transformed = transform != 3
		}
		if transformed {
			streamLength, err = readUIntBase128(woff2, &off)
			require.NoError(t, err)
		}
		tables = append(tables, tableEnt{tag: tag, streamLength: streamLength})
	}
	stream, err := io.ReadAll(brotli.NewReader(strings.NewReader(string(woff2[off:]))))
	require.NoError(t, err, "brotli stream")
	at := 0
	for _, ent := range tables {
		if ent.tag == "cmap" {
			require.LessOrEqual(t, at+ent.streamLength, len(stream))
			return parseCmap(t, stream[at:at+ent.streamLength])
		}
		at += ent.streamLength
	}
	t.Fatal("no cmap table")
	return nil
}

func parseCmap(t *testing.T, cmap []byte) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	be16 := func(o int) int { return int(binary.BigEndian.Uint16(cmap[o : o+2])) }
	be32 := func(o int) int { return int(binary.BigEndian.Uint32(cmap[o : o+4])) }
	numSub := be16(2)
	for i := 0; i < numSub; i++ {
		off := be32(8 + i*8)
		switch format := be16(off); format {
		case 4:
			// Glyph-aware: a code resolving to glyph 0 (.notdef) is
			// NOT mapped -- range-only reads would count trailing
			// 0xFFFD filler segments as coverage.
			segX2 := be16(off + 6)
			ends := off + 14
			starts := ends + segX2 + 2
			deltas := starts + segX2
			rangeOffs := deltas + segX2
			for s := 0; s < segX2; s += 2 {
				end := be16(ends + s)
				start := be16(starts + s)
				if start == 0xffff {
					continue
				}
				delta := int(int16(binary.BigEndian.Uint16(cmap[deltas+s : deltas+s+2])))
				rangeOff := be16(rangeOffs + s)
				for cp := start; cp <= end; cp++ {
					glyph := 0
					if rangeOff == 0 {
						glyph = (cp + delta) & 0xffff
					} else {
						gi := rangeOffs + s + rangeOff + (cp-start)*2
						glyph = be16(gi)
						if glyph != 0 {
							glyph = (glyph + delta) & 0xffff
						}
					}
					if glyph != 0 {
						out[cp] = true
					}
				}
			}
		case 12:
			nGroups := be32(off + 12)
			for g := 0; g < nGroups; g++ {
				base := off + 16 + g*12
				for cp := be32(base); cp <= be32(base + 4); cp++ {
					out[cp] = true
				}
			}
		}
	}
	return out
}

// fontsDir is the committed woff2 home -- the fonts stay a frontend
// asset (vite bundles them), while the mapping artifact lives beside
// its Go decoder.
var fontFiles = map[string]string{
	"fi":  "file-icons.woff2",
	"fa":  "fontawesome.woff2",
	"mf":  "mfixx.woff2",
	"oct": "octicons.woff2",
}

func loadCmaps(t *testing.T) map[string]map[int]bool {
	t.Helper()
	dir := filepath.Join("..", "..", "frontend", "src", "fileicons", "fonts")
	out := map[string]map[int]bool{}
	for cls, name := range fontFiles {
		woff2, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		out[cls] = cmapCodepoints(t, woff2)
	}
	return out
}

func TestEveryCodepointExistsInItsFontCmap(t *testing.T) {
	cmaps := loadCmaps(t)
	tab := decodeCommitted(t)
	var missing []string
	check := func(font string, cp int, what string) {
		cmap, ok := cmaps[font]
		if !ok {
			missing = append(missing, fmt.Sprintf("unknown font class %s (%s)", font, what))
			return
		}
		if !cmap[cp] {
			missing = append(missing, fmt.Sprintf("%s U+%X (%s)", font, cp, what))
		}
	}
	for _, r := range tab.FileRules {
		check(r.Font, r.CP, r.Suffix+r.Regex)
	}
	for _, r := range tab.DirRules {
		check(r.Font, r.CP, r.Suffix+r.Regex)
	}
	check(tab.DefFile.Font, tab.DefFile.CP, "defFile")
	check(tab.DefDir.Font, tab.DefDir.CP, "defDir")
	assert.Empty(t, missing)
}
