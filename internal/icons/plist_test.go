package icons

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

/* --- test-only bplist builder --------------------------------------- */

// bplistBuilder assembles a tiny binary plist from scratch so the
// fixtures exercise the real wire format (the internal/firefox
// mozLz4-compressor precedent: build the container in the test, parse
// it with the production code).
type bplistBuilder struct {
	body    []byte   // objects, appended in index order
	offsets []uint64 // object index -> offset
}

func newBplistBuilder() *bplistBuilder {
	return &bplistBuilder{body: []byte("bplist00")}
}

// add appends one raw object and returns its index.
func (b *bplistBuilder) add(obj []byte) uint64 {
	idx := uint64(len(b.offsets))
	b.offsets = append(b.offsets, uint64(len(b.body)))
	b.body = append(b.body, obj...)
	return idx
}

// asciiObj encodes an ASCII string object (marker 0x5n; 0x5F + int
// length beyond 14).
func asciiObj(s string) []byte {
	return append(markerWithCount(0x5, len(s)), s...)
}

// utf16Obj encodes a UTF-16BE string object (marker 0x6n, n code
// units).
func utf16Obj(s string) []byte {
	runes := []rune(s)
	out := markerWithCount(0x6, len(runes))
	for _, r := range runes { // BMP-only fixtures
		out = binary.BigEndian.AppendUint16(out, uint16(r))
	}
	return out
}

// intObj encodes a one-byte int object (marker 0x10).
func intObj(v byte) []byte { return []byte{0x10, v} }

// dictObj encodes a dict object over one-byte object refs.
func dictObj(keys, vals []uint64) []byte {
	out := markerWithCount(0xD, len(keys))
	for _, k := range keys {
		out = append(out, byte(k))
	}
	for _, v := range vals {
		out = append(out, byte(v))
	}
	return out
}

// markerWithCount emits a marker byte for type typ with count n,
// spilling to a trailing int object when n >= 15.
func markerWithCount(typ byte, n int) []byte {
	if n < 15 {
		return []byte{typ<<4 | byte(n)}
	}
	// 0xF nibble + a two-byte int object (0x11 = 2^1 bytes).
	out := []byte{typ<<4 | 0x0F, 0x11}
	return binary.BigEndian.AppendUint16(out, uint16(n))
}

// finish appends the offset table (one-byte offsets; the fixtures stay
// tiny) and the 32-byte trailer, rooting the document at top.
func (b *bplistBuilder) finish(top uint64) []byte {
	tableOff := uint64(len(b.body))
	for _, off := range b.offsets {
		b.body = append(b.body, byte(off))
	}
	trailer := make([]byte, 32)
	trailer[6] = 1 // offset int size
	trailer[7] = 1 // object ref size
	binary.BigEndian.PutUint64(trailer[8:16], uint64(len(b.offsets)))
	binary.BigEndian.PutUint64(trailer[16:24], top)
	binary.BigEndian.PutUint64(trailer[24:32], tableOff)
	return append(b.body, trailer...)
}

// buildIconPlist assembles the canonical fixture: a root dict carrying
// CFBundleName plus the requested icon keys ("" = omit).
func buildIconPlist(t *testing.T, iconFile, iconName string) []byte {
	t.Helper()
	b := newBplistBuilder()
	keys := []uint64{b.add(asciiObj("CFBundleName"))}
	vals := []uint64{b.add(asciiObj("Fixture"))}
	if iconFile != "" {
		keys = append(keys, b.add(asciiObj("CFBundleIconFile")))
		vals = append(vals, b.add(asciiObj(iconFile)))
	}
	if iconName != "" {
		keys = append(keys, b.add(asciiObj("CFBundleIconName")))
		vals = append(vals, b.add(asciiObj(iconName)))
	}
	root := b.add(dictObj(keys, vals))
	return b.finish(root)
}

/* --- binary plist tests --------------------------------------------- */

func TestBplistIconRefs(t *testing.T) {
	file, name := plistIconRefs(buildIconPlist(t, "AppIcon", "AppIconAsset"))
	require.Equal(t, "AppIcon", file)
	require.Equal(t, "AppIconAsset", name)
}

func TestBplistIconFileOnly(t *testing.T) {
	file, name := plistIconRefs(buildIconPlist(t, "electron.icns", ""))
	require.Equal(t, "electron.icns", file)
	require.Empty(t, name)
}

func TestBplistIconNameOnly(t *testing.T) {
	file, name := plistIconRefs(buildIconPlist(t, "", "AssetsOnly"))
	require.Empty(t, file)
	require.Equal(t, "AssetsOnly", name)
}

func TestBplistUTF16Value(t *testing.T) {
	b := newBplistBuilder()
	k := b.add(asciiObj("CFBundleIconFile"))
	v := b.add(utf16Obj("Ikonchen"))
	root := b.add(dictObj([]uint64{k}, []uint64{v}))
	file, _ := plistIconRefs(b.finish(root))
	require.Equal(t, "Ikonchen", file)
}

func TestBplistExtendedCountString(t *testing.T) {
	long := "AVeryLongIconFileNameThatNeedsAnExtendedCountMarkerBecauseItIsWayOverFourteenBytes"
	b := newBplistBuilder()
	k := b.add(asciiObj("CFBundleIconFile"))
	v := b.add(asciiObj(long))
	root := b.add(dictObj([]uint64{k}, []uint64{v}))
	file, _ := plistIconRefs(b.finish(root))
	require.Equal(t, long, file)
}

func TestBplistNonStringValueIgnored(t *testing.T) {
	b := newBplistBuilder()
	k := b.add(asciiObj("CFBundleIconFile"))
	v := b.add(intObj(42)) // an int where a string belongs
	root := b.add(dictObj([]uint64{k}, []uint64{v}))
	file, name := plistIconRefs(b.finish(root))
	require.Empty(t, file)
	require.Empty(t, name)
}

func TestBplistCorruptionRejected(t *testing.T) {
	good := buildIconPlist(t, "AppIcon", "")
	cases := map[string][]byte{
		"empty":             {},
		"short":             []byte("bplist00"),
		"bad magic":         append([]byte("xplist00"), good[8:]...),
		"truncated trailer": good[:len(good)-5],
		"root is a string": func() []byte {
			b := newBplistBuilder()
			top := b.add(asciiObj("not a dict"))
			return b.finish(top)
		}(),
		"top object out of range": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint64(d[len(d)-16:len(d)-8], 999)
			return d
		}(),
		"offset table escapes": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint64(d[len(d)-8:], uint64(len(d)))
			return d
		}(),
		"zero objects": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint64(d[len(d)-24:len(d)-16], 0)
			return d
		}(),
		"object count over cap": func() []byte {
			d := append([]byte(nil), good...)
			binary.BigEndian.PutUint64(d[len(d)-24:len(d)-16], maxBplistObjects+1)
			return d
		}(),
		"offset width zero": func() []byte {
			d := append([]byte(nil), good...)
			d[len(d)-26] = 0
			return d
		}(),
		"ref width nine": func() []byte {
			d := append([]byte(nil), good...)
			d[len(d)-25] = 9
			return d
		}(),
	}
	for name, data := range cases {
		file, iconName := plistIconRefs(data)
		require.Empty(t, file, "case %q", name)
		require.Empty(t, iconName, "case %q", name)
	}
}

func TestBplistDanglingRefsInDict(t *testing.T) {
	// A dict whose value ref points past the object table: the key is
	// simply skipped, nothing panics.
	b := newBplistBuilder()
	k := b.add(asciiObj("CFBundleIconFile"))
	root := b.add(dictObj([]uint64{k}, []uint64{200}))
	file, name := plistIconRefs(b.finish(root))
	require.Empty(t, file)
	require.Empty(t, name)
}

/* --- XML plist tests ------------------------------------------------ */

const xmlPlistFixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>Fixture</string>
	<key>CFBundleDocumentTypes</key>
	<array>
		<dict>
			<key>CFBundleIconFile</key>
			<string>nested-decoy</string>
		</dict>
	</array>
	<key>CFBundleIconFile</key>
	<string>electron</string>
	<key>LSMinimumSystemVersion</key>
	<string>10.15</string>
</dict>
</plist>
`

func TestXMLPlistIconRefs(t *testing.T) {
	file, name := plistIconRefs([]byte(xmlPlistFixture))
	require.Equal(t, "electron", file, "root-level value wins; the nested decoy is ignored")
	require.Empty(t, name)
}

func TestXMLPlistIconNameOnly(t *testing.T) {
	xml := `<plist><dict><key>CFBundleIconName</key><string>AppIcon</string></dict></plist>`
	file, name := plistIconRefs([]byte(xml))
	require.Empty(t, file)
	require.Equal(t, "AppIcon", name)
}

func TestXMLPlistNonStringValueClearsKey(t *testing.T) {
	// The pending key is consumed by the <true/> value; the following
	// stray string must not attach to it.
	xml := `<plist><dict>
		<key>CFBundleIconFile</key><true/>
		<string>stray</string>
	</dict></plist>`
	file, _ := plistIconRefs([]byte(xml))
	require.Empty(t, file)
}

func TestXMLPlistArrayStringsNeverMatch(t *testing.T) {
	xml := `<plist><dict>
		<key>CFBundleIconFile</key>
		<array><string>inside-array</string></array>
	</dict></plist>`
	file, _ := plistIconRefs([]byte(xml))
	require.Empty(t, file)
}

func TestXMLPlistGarbage(t *testing.T) {
	file, name := plistIconRefs([]byte("not a plist at all"))
	require.Empty(t, file)
	require.Empty(t, name)
	file, name = plistIconRefs([]byte("<plist><dict><key>CFBundleIconFile</key>"))
	require.Empty(t, file)
	require.Empty(t, name)
}
