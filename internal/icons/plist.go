package icons

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"strings"
	"unicode/utf16"
)

// Minimal Info.plist reader for the macOS bundle-icon path: it answers
// exactly one question -- the string values of CFBundleIconFile and
// CFBundleIconName in the ROOT dictionary -- for both serializations
// Xcode produces. Release builds convert Info.plist to BINARY plist
// (bplist00) by default, so a large share of /Applications bundles are
// binary, while Electron/CEF apps often ship XML; parsing must handle
// both. The binary reader is hand-rolled (the internal/firefox mozLz4
// precedent: a small bounded decoder beats a dependency), covering
// only what the question needs: header + trailer, the offset table,
// and dict / ASCII-string / UTF-16 string / int (extended counts)
// objects. Every read is bounds-checked; anything malformed answers
// "no icon refs" and the caller falls back to the glyph.

// Binary-plist bounds. Info.plist files are small (an Electron app's
// is a few KB, Xcode's ~100KB); the caps reject pathological files
// without constraining real ones.
const (
	maxBplistObjects = 1 << 16 // offset-table + dict entry cap
	maxBplistString  = 4096    // dict keys and the two icon values are short
)

// plistIconRefs extracts CFBundleIconFile and CFBundleIconName from an
// Info.plist in either serialization. Missing keys, non-string values,
// and malformed input all yield "".
func plistIconRefs(data []byte) (iconFile, iconName string) {
	if len(data) >= 8 && string(data[:8]) == "bplist00" {
		return bplistIconRefs(data)
	}
	return xmlPlistIconRefs(data)
}

/* --- binary plist --------------------------------------------------- */

// bplist is one parsed binary plist: the raw bytes plus the validated
// offset table (object index -> byte offset) and the object-ref width
// used inside container objects.
type bplist struct {
	data    []byte
	offsets []uint64
	refSize int
}

// bplistIconRefs reads the two icon keys out of the root dict of a
// bplist00 document; "" everywhere on any structural problem.
func bplistIconRefs(data []byte) (iconFile, iconName string) {
	p, root, ok := parseBplist(data)
	if !ok {
		return "", ""
	}
	keys, vals, ok := p.dictAt(root)
	if !ok {
		return "", ""
	}
	for i, kref := range keys {
		k, ok := p.stringAt(kref)
		if !ok {
			continue
		}
		switch k {
		case "CFBundleIconFile":
			if v, ok := p.stringAt(vals[i]); ok {
				iconFile = v
			}
		case "CFBundleIconName":
			if v, ok := p.stringAt(vals[i]); ok {
				iconName = v
			}
		}
	}
	return iconFile, iconName
}

// parseBplist validates the header and the 32-byte trailer (offset
// width, object-ref width, object count, top object, offset-table
// start) and loads the offset table with every entry range-checked.
func parseBplist(data []byte) (p *bplist, root uint64, ok bool) {
	if len(data) < 8+32 || string(data[:8]) != "bplist00" {
		return nil, 0, false
	}
	tr := data[len(data)-32:]
	offSize := int(tr[6])
	refSize := int(tr[7])
	numObjects := binary.BigEndian.Uint64(tr[8:16])
	topObject := binary.BigEndian.Uint64(tr[16:24])
	tableOff := binary.BigEndian.Uint64(tr[24:32])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 {
		return nil, 0, false
	}
	if numObjects == 0 || numObjects > maxBplistObjects || topObject >= numObjects {
		return nil, 0, false
	}
	bodyEnd := uint64(len(data) - 32)
	tableEnd := tableOff + numObjects*uint64(offSize) // cannot overflow: both factors capped
	if tableOff < 8 || tableEnd > bodyEnd || tableEnd < tableOff {
		return nil, 0, false
	}
	offsets := make([]uint64, numObjects)
	for i := range offsets {
		start := tableOff + uint64(i)*uint64(offSize)
		v := readBEUint(data[start : start+uint64(offSize)])
		if v < 8 || v >= bodyEnd {
			return nil, 0, false
		}
		offsets[i] = v
	}
	return &bplist{data: data, offsets: offsets, refSize: refSize}, topObject, true
}

// count resolves a container/string length: the marker's low nibble,
// or -- when that nibble is 0xF -- a following int object holding the
// real length. Returns the length and the offset just past it.
func (p *bplist) count(off uint64, info byte) (n, next uint64, ok bool) {
	if info != 0x0F {
		return uint64(info), off, true
	}
	if off >= uint64(len(p.data)) {
		return 0, 0, false
	}
	m := p.data[off]
	if m>>4 != 0x1 { // int marker
		return 0, 0, false
	}
	nbytes := uint64(1) << (m & 0x0F)
	if nbytes > 8 || off+1+nbytes > uint64(len(p.data)) {
		return 0, 0, false
	}
	return readBEUint(p.data[off+1 : off+1+nbytes]), off + 1 + nbytes, true
}

// stringAt reads object idx as a string: ASCII (0x5) or UTF-16BE
// (0x6). Anything else -- ints, data, nested containers -- answers
// not-a-string.
func (p *bplist) stringAt(idx uint64) (string, bool) {
	if idx >= uint64(len(p.offsets)) {
		return "", false
	}
	off := p.offsets[idx]
	marker := p.data[off]
	n, payload, ok := p.count(off+1, marker&0x0F)
	if !ok || n > maxBplistString {
		return "", false
	}
	switch marker >> 4 {
	case 0x5: // ASCII, n bytes
		if payload+n > uint64(len(p.data)) {
			return "", false
		}
		return string(p.data[payload : payload+n]), true
	case 0x6: // UTF-16BE, n code units
		if payload+2*n > uint64(len(p.data)) {
			return "", false
		}
		u16 := make([]uint16, n)
		for i := uint64(0); i < n; i++ {
			u16[i] = binary.BigEndian.Uint16(p.data[payload+2*i : payload+2*i+2])
		}
		return string(utf16.Decode(u16)), true
	}
	return "", false
}

// dictAt reads object idx as a dictionary: n key refs followed by n
// value refs, each refSize bytes.
func (p *bplist) dictAt(idx uint64) (keys, vals []uint64, ok bool) {
	if idx >= uint64(len(p.offsets)) {
		return nil, nil, false
	}
	off := p.offsets[idx]
	marker := p.data[off]
	if marker>>4 != 0xD {
		return nil, nil, false
	}
	n, payload, ok := p.count(off+1, marker&0x0F)
	if !ok || n > maxBplistObjects {
		return nil, nil, false
	}
	rs := uint64(p.refSize)
	if payload+2*n*rs > uint64(len(p.data)) {
		return nil, nil, false
	}
	keys = make([]uint64, n)
	vals = make([]uint64, n)
	for i := uint64(0); i < n; i++ {
		keys[i] = readBEUint(p.data[payload+i*rs : payload+(i+1)*rs])
		vals[i] = readBEUint(p.data[payload+(n+i)*rs : payload+(n+i+1)*rs])
	}
	return keys, vals, true
}

// readBEUint reads a 1..8-byte big-endian unsigned integer.
func readBEUint(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

/* --- XML plist ------------------------------------------------------ */

// xmlPlistIconRefs walks an XML plist's token stream and picks up
// <key>CFBundleIconFile/IconName</key><string>...</string> pairs in
// the ROOT dict only (dict depth 1), matching the binary reader's
// semantics. Non-string values and nested structures clear the
// pending key, so an array's strings can never be mistaken for a root
// value.
func xmlPlistIconRefs(data []byte) (iconFile, iconName string) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dictDepth := 0
	pendingKey := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return iconFile, iconName
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "plist":
				// The document wrapper: transparent.
			case "dict":
				dictDepth++
				pendingKey = ""
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return iconFile, iconName
				}
				if dictDepth == 1 {
					pendingKey = strings.TrimSpace(k)
				} else {
					pendingKey = ""
				}
			case "string":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return iconFile, iconName
				}
				if dictDepth == 1 {
					switch pendingKey {
					case "CFBundleIconFile":
						iconFile = v
					case "CFBundleIconName":
						iconName = v
					}
				}
				pendingKey = ""
			default:
				// Any other value type consumes the pending key.
				pendingKey = ""
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				dictDepth--
			}
		}
	}
}
