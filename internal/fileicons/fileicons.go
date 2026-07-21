// Package fileicons decodes data.bin, the committed per-file-type
// icon mapping artifact behind the frontend's result-row glyphs. The
// artifact is a binpazer container (the block-based binary format
// from github.com/wow-look-at-my/bin-file-fmt; that repo's SPEC.md is
// the interop contract) holding ONE user block -- IconRules, GUID
// IconRulesGUID -- whose payload carries the rule table in the
// compact encoding v1 written by
// frontend/src/fileicons/tools/emitbin.mjs (the writer half of this
// package; keep the two in lockstep). The container walk itself uses
// the format's FIRST-PARTY Go reader -- this package deliberately
// implements only the payload layer, never the container.
//
// The app hands the decoded table to the frontend once at startup
// over the GetFileIcons bound method (internal/app fileicons.go);
// frontend/src/fileicons/fileicons.ts compiles it into match tables.
// Load never fails: a decode error (impossible for the committed
// artifact while the package tests pass, but the guard costs
// nothing) logs once and degrades to the pack's uncolored octicon
// defaults, so a damaged artifact costs icons, never the app.
package fileicons

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"

	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

//go:embed data.bin
var dataBin []byte

// IconRulesGUID is the IconRules block type's global identity --
// tools/emitbin.mjs writes the same constant into the container's
// Block Type Table, and the integrity tests decode the committed
// artifact through this package, so a drift between the two fails CI.
const IconRulesGUID = "ea78f6bb-edf3-4b73-b114-a607c767ce0f"

// The pack's uncolored fallback glyphs (octicons file-text and
// file-directory, Atom's own defaults) -- the defaults of last
// resort when the artifact cannot be decoded.
const (
	fallbackFont   = "oct"
	fallbackFileCP = 0xf011
	fallbackDirCP  = 0xf016
)

// Icon is one glyph reference; the frontend renders the codepoint
// through the icon font named by Font (a fileicons.css class).
type Icon struct {
	Font string `json:"font"`
	CP   int    `json:"cp"`
}

// Rule is one match rule. Exactly one of Suffix (case-insensitive
// basename suffix) and Regex (tested against the raw basename with
// Flags) is set; Dark/Light are the pack's per-motif hex colours,
// both set or both empty.
type Rule struct {
	Font   string `json:"font"`
	CP     int    `json:"cp"`
	Suffix string `json:"suffix,omitempty"`
	Regex  string `json:"regex,omitempty"`
	Flags  string `json:"flags,omitempty"`
	Dark   string `json:"dark,omitempty"`
	Light  string `json:"light,omitempty"`
}

// Table is the decoded artifact: rules in pack-priority order (first
// match wins) plus the two uncolored defaults.
type Table struct {
	FileRules []Rule `json:"fileRules"`
	DirRules  []Rule `json:"dirRules"`
	DefFile   Icon   `json:"defFile"`
	DefDir    Icon   `json:"defDir"`
}

// fallbackTable is what Load degrades to when the embedded artifact
// cannot be decoded: no rules, the pack defaults.
func fallbackTable() *Table {
	return &Table{
		DefFile: Icon{Font: fallbackFont, CP: fallbackFileCP},
		DefDir:  Icon{Font: fallbackFont, CP: fallbackDirCP},
	}
}

var (
	loadOnce  sync.Once
	loadTable *Table
)

// Load decodes the embedded artifact once and caches it. It never
// fails: decode errors log once and yield the fallback table.
func Load() *Table {
	loadOnce.Do(func() {
		t, err := DecodeTable(dataBin)
		if err != nil {
			log.Printf("fileicons: embedded data.bin unusable, falling back to default icons: %v", err)
			t = fallbackTable()
		}
		loadTable = t
	})
	return loadTable
}

// DecodeTable decodes a data.bin byte image: the binpazer container
// walk (first-party reader, CRC verified), then the IconRules
// payload. Exposed for the integrity tests; Load is the cached
// production entry.
func DecodeTable(data []byte) (*Table, error) {
	payload, err := iconRulesPayload(data)
	if err != nil {
		return nil, err
	}
	return decodePayload(payload)
}

// iconRulesPayload walks the container and returns the IconRules
// block's payload. Unknown ancillary blocks are skipped; an unknown
// block flagged critical is refused (the spec's MUST-stop rule).
func iconRulesPayload(data []byte) ([]byte, error) {
	br, err := binpazer.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("container: %w", err)
	}
	want, err := binpazer.ParseGUID(IconRulesGUID)
	if err != nil {
		return nil, err
	}
	for {
		b, err := br.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("container: no IconRules block")
		}
		if err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
		if b.Known && b.GUID == want {
			payload, err := br.ReadPayload(b)
			if err != nil {
				return nil, fmt.Errorf("container: IconRules payload: %w", err)
			}
			return payload, nil
		}
		if !b.Known && b.Flags&binpazer.FlagCritical != 0 {
			return nil, fmt.Errorf("container: unknown critical block type %d", b.TypeID)
		}
		if err := br.SkipPayload(b); err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
	}
}

/* --- IconRules payload encoding v1 (see tools/emitbin.mjs) --------- */

// cursor is a bounds-checked little-endian reader over the payload.
type cursor struct {
	b   []byte
	off int
}

func (c *cursor) need(n int) error {
	if n < 0 || c.off+n > len(c.b) {
		return fmt.Errorf("payload truncated at offset %d (need %d bytes)", c.off, n)
	}
	return nil
}

func (c *cursor) u8() (int, error) {
	if err := c.need(1); err != nil {
		return 0, err
	}
	v := int(c.b[c.off])
	c.off++
	return v, nil
}

func (c *cursor) u16() (int, error) {
	if err := c.need(2); err != nil {
		return 0, err
	}
	v := int(c.b[c.off]) | int(c.b[c.off+1])<<8
	c.off += 2
	return v, nil
}

func (c *cursor) u32() (int, error) {
	if err := c.need(4); err != nil {
		return 0, err
	}
	v := int(c.b[c.off]) | int(c.b[c.off+1])<<8 | int(c.b[c.off+2])<<16 | int(c.b[c.off+3])<<24
	c.off += 4
	if v < 0 {
		return 0, fmt.Errorf("u32 out of range at offset %d", c.off-4)
	}
	return v, nil
}

func (c *cursor) str(n int) (string, error) {
	if err := c.need(n); err != nil {
		return "", err
	}
	v := string(c.b[c.off : c.off+n])
	c.off += n
	return v, nil
}

func (c *cursor) codepoint() (int, error) {
	cp, err := c.u32()
	if err != nil {
		return 0, err
	}
	if cp > 0x10ffff {
		return 0, fmt.Errorf("codepoint out of range: %d", cp)
	}
	return cp, nil
}

func (c *cursor) hexColor() (string, error) {
	if err := c.need(3); err != nil {
		return "", err
	}
	v := fmt.Sprintf("#%02x%02x%02x", c.b[c.off], c.b[c.off+1], c.b[c.off+2])
	c.off += 3
	return v, nil
}

func decodeRule(c *cursor, fonts []string) (Rule, error) {
	kind, err := c.u8()
	if err != nil {
		return Rule{}, err
	}
	if kind&0xc0 != 0 {
		return Rule{}, fmt.Errorf("reserved kind bits set: %#x", kind)
	}
	fontIdx := kind & 0x07
	if fontIdx >= len(fonts) {
		return Rule{}, fmt.Errorf("font index %d out of range", fontIdx)
	}
	cp, err := c.codepoint()
	if err != nil {
		return Rule{}, err
	}
	r := Rule{Font: fonts[fontIdx], CP: cp}
	if kind&0x10 != 0 {
		if r.Dark, err = c.hexColor(); err != nil {
			return Rule{}, err
		}
		r.Light = r.Dark
		if kind&0x20 == 0 {
			if r.Light, err = c.hexColor(); err != nil {
				return Rule{}, err
			}
		}
	}
	n := 0
	if kind&0x08 != 0 {
		if n, err = c.u8(); err != nil {
			return Rule{}, err
		}
		if r.Flags, err = c.str(n); err != nil {
			return Rule{}, err
		}
	}
	if n, err = c.u16(); err != nil {
		return Rule{}, err
	}
	if n == 0 {
		return Rule{}, errors.New("empty match pattern")
	}
	pattern, err := c.str(n)
	if err != nil {
		return Rule{}, err
	}
	if kind&0x08 != 0 {
		r.Regex = pattern
	} else {
		r.Suffix = pattern
	}
	return r, nil
}

func decodePayload(payload []byte) (*Table, error) {
	c := &cursor{b: payload}
	version, err := c.u32()
	if err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, fmt.Errorf("unsupported IconRules payload version %d", version)
	}
	fontCount, err := c.u8()
	if err != nil {
		return nil, err
	}
	fonts := make([]string, 0, fontCount)
	for i := 0; i < fontCount; i++ {
		n, err := c.u8()
		if err != nil {
			return nil, err
		}
		name, err := c.str(n)
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, errors.New("empty font class name")
		}
		fonts = append(fonts, name)
	}
	iconRef := func() (Icon, error) {
		idx, err := c.u8()
		if err != nil {
			return Icon{}, err
		}
		if idx >= len(fonts) {
			return Icon{}, fmt.Errorf("font index %d out of range", idx)
		}
		cp, err := c.codepoint()
		if err != nil {
			return Icon{}, err
		}
		return Icon{Font: fonts[idx], CP: cp}, nil
	}
	t := &Table{}
	if t.DefFile, err = iconRef(); err != nil {
		return nil, err
	}
	if t.DefDir, err = iconRef(); err != nil {
		return nil, err
	}
	fileCount, err := c.u32()
	if err != nil {
		return nil, err
	}
	dirCount, err := c.u32()
	if err != nil {
		return nil, err
	}
	// The smallest possible rule is 8 bytes (kind + codepoint + u16
	// length + a 1-byte pattern); bound the claimed counts against the
	// remaining payload before allocating.
	const minRuleSize = 8
	if remaining := len(payload) - c.off; (fileCount+dirCount)*minRuleSize > remaining {
		return nil, fmt.Errorf("rule counts %d+%d exceed payload size", fileCount, dirCount)
	}
	t.FileRules = make([]Rule, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		r, err := decodeRule(c, fonts)
		if err != nil {
			return nil, err
		}
		t.FileRules = append(t.FileRules, r)
	}
	t.DirRules = make([]Rule, 0, dirCount)
	for i := 0; i < dirCount; i++ {
		r, err := decodeRule(c, fonts)
		if err != nil {
			return nil, err
		}
		t.DirRules = append(t.DirRules, r)
	}
	if c.off != len(payload) {
		return nil, fmt.Errorf("trailing garbage: %d bytes", len(payload)-c.off)
	}
	return t, nil
}
