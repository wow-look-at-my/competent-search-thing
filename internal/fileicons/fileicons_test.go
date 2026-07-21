// The committed-data integrity gate, retargeted from the old vitest
// data.json checks: data.bin must decode through the SAME reader the
// runtime uses (this package: first-party binpazer container walk +
// the payload decoder), the rule counts and shapes must match the
// vendoring receipts, the pack-content pins the frontend relies on
// must hold, and the reader must refuse corruption. The cmap half of
// the gate (every codepoint exists in its committed font) lives in
// woff2cmap_test.go.
package fileicons

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	binpazer "github.com/wow-look-at-my/bin-file-fmt/go"
)

var knownFonts = map[string]bool{"fi": true, "fa": true, "mf": true, "oct": true}

func decodeCommitted(t *testing.T) *Table {
	t.Helper()
	tab, err := DecodeTable(dataBin)
	require.NoError(t, err, "the committed data.bin must decode")
	return tab
}

func TestLoadDecodesTheEmbeddedArtifact(t *testing.T) {
	tab := Load()
	assert.Len(t, tab.FileRules, 2158)
	assert.Len(t, tab.DirRules, 42)
	assert.Same(t, tab, Load(), "Load caches")
}

func TestVendoringReceipts(t *testing.T) {
	tab := decodeCommitted(t)
	assert.Len(t, tab.FileRules, 2158)
	assert.Len(t, tab.DirRules, 42)
	assert.Equal(t, Icon{Font: "oct", CP: 0xf011}, tab.DefFile, "octicons file-text")
	assert.Equal(t, Icon{Font: "oct", CP: 0xf016}, tab.DefDir, "octicons file-directory")
	for _, r := range append(append([]Rule{}, tab.FileRules...), tab.DirRules...) {
		assert.True(t, (r.Suffix != "") != (r.Regex != ""), "exactly one of suffix/regex: %+v", r)
		assert.True(t, knownFonts[r.Font], "unknown font %q", r.Font)
		assert.LessOrEqual(t, r.CP, 0x10ffff)
		assert.Equal(t, r.Dark != "", r.Light != "", "colours come in pairs: %+v", r)
		if r.Suffix != "" {
			assert.Equal(t, strings.ToLower(r.Suffix), r.Suffix, "suffixes are stored lowercase")
			assert.Empty(t, r.Flags, "flags are a regex-only field")
		}
		for _, col := range []string{r.Dark, r.Light} {
			if col != "" {
				assert.Regexp(t, `^#[0-9a-f]{6}$`, col)
			}
		}
	}
}

// TestPackContentPins mirrors the frontend matcher tests' real-data
// expectations (fileicons.test.ts drives the matcher over a fixture
// now, so the pack content itself is pinned here).
func TestPackContentPins(t *testing.T) {
	tab := decodeCommitted(t)
	suffixAt := func(s string) int {
		for i, r := range tab.FileRules {
			if r.Suffix == s {
				return i
			}
		}
		return -1
	}
	goAt := suffixAt(".go")
	require.GreaterOrEqual(t, goAt, 0, "a .go suffix rule exists")
	goRule := tab.FileRules[goAt]
	assert.Equal(t, "fi", goRule.Font)
	assert.Equal(t, 60078, goRule.CP)
	assert.Equal(t, "#6a9fb5", goRule.Dark)
	assert.Equal(t, "#6a9fb5", goRule.Light)

	// Compound suffixes precede the plain extension (pack priority
	// order -- the frontend's first-match semantics depend on it).
	huskyAt := suffixAt(".huskyrc.json")
	jsonAt := suffixAt(".json")
	require.GreaterOrEqual(t, huskyAt, 0)
	require.GreaterOrEqual(t, jsonAt, 0)
	assert.Less(t, huskyAt, jsonAt)
	assert.Equal(t, 128054, tab.FileRules[huskyAt].CP)

	regexWith := func(sub string) *Rule {
		for i := range tab.FileRules {
			if strings.Contains(tab.FileRules[i].Regex, sub) {
				return &tab.FileRules[i]
			}
		}
		return nil
	}
	webpack := regexWith("webpack")
	require.NotNil(t, webpack, "a webpack regex rule exists")
	assert.Equal(t, 60001, webpack.CP)
	makefile := regexWith("^Makefile")
	require.NotNil(t, makefile, "a Makefile regex rule exists")
	assert.Equal(t, "oct", makefile.Font)
	assert.Equal(t, 61558, makefile.CP)

	github := -1
	for i, r := range tab.DirRules {
		if strings.Contains(r.Regex, "github") || strings.Contains(r.Suffix, "github") {
			github = i
			break
		}
	}
	require.GreaterOrEqual(t, github, 0, "a .github dir rule exists")
	assert.Equal(t, 61450, tab.DirRules[github].CP)
}

/* --- hardening ------------------------------------------------------ */

// bw is a tiny little-endian payload builder for crafting hostile
// IconRules payloads (the write half lives in JS; tests build inputs
// by hand).
type bw struct{ b []byte }

func (w *bw) u8(v int) *bw  { w.b = append(w.b, byte(v)); return w }
func (w *bw) u16(v int) *bw { w.b = append(w.b, byte(v), byte(v>>8)); return w }
func (w *bw) u32(v int) *bw {
	w.b = append(w.b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	return w
}
func (w *bw) raw(s string) *bw { w.b = append(w.b, s...); return w }

// payloadHead writes version 1, one font ("fi"), oct-less defaults on
// that font, and the given rule counts.
func payloadHead(fileCount, dirCount int) *bw {
	w := &bw{}
	w.u32(1).u8(1).u8(2).raw("fi")
	w.u8(0).u32(0xf011) // defFile
	w.u8(0).u32(0xf016) // defDir
	w.u32(fileCount).u32(dirCount)
	return w
}

// pack wraps payloads into a real container via the first-party
// writer: type_id 1 = IconRules unless overridden per block.
type testBlock struct {
	typeID  uint16
	flags   uint16
	payload []byte
}

func pack(t *testing.T, defs []binpazer.TypeDef, blocks []testBlock) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := binpazer.NewWriter(&buf, binpazer.GUID{1}, "test", defs)
	require.NoError(t, err)
	for _, b := range blocks {
		require.NoError(t, w.Block(b.typeID, b.flags, b.payload))
	}
	require.NoError(t, w.End())
	return buf.Bytes()
}

func iconRulesDefs(t *testing.T) []binpazer.TypeDef {
	t.Helper()
	guid, err := binpazer.ParseGUID(IconRulesGUID)
	require.NoError(t, err)
	return []binpazer.TypeDef{{TypeID: 1, GUID: guid, Name: "IconRules"}}
}

func packPayload(t *testing.T, payload []byte) []byte {
	t.Helper()
	return pack(t, iconRulesDefs(t), []testBlock{{typeID: 1, flags: binpazer.FlagHasCRC, payload: payload}})
}

func TestDecodeRejectsCorruptContainers(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, err := DecodeTable(nil)
		assert.Error(t, err)
	})
	t.Run("bad magic", func(t *testing.T) {
		data := bytes.Clone(dataBin)
		data[0] ^= 0xff
		_, err := DecodeTable(data)
		assert.ErrorIs(t, err, binpazer.ErrBadMagic)
	})
	t.Run("truncated", func(t *testing.T) {
		for _, n := range []int{40, 200, len(dataBin) - 8} {
			_, err := DecodeTable(dataBin[:n])
			assert.Error(t, err, "truncated to %d bytes", n)
		}
	})
	t.Run("payload corruption fails the CRC", func(t *testing.T) {
		data := bytes.Clone(dataBin)
		data[len(data)-100] ^= 0xff
		_, err := DecodeTable(data)
		assert.ErrorIs(t, err, binpazer.ErrCRCMismatch)
	})
	t.Run("no IconRules block", func(t *testing.T) {
		other, err := binpazer.ParseGUID("11111111-2222-4333-8444-555555555555")
		require.NoError(t, err)
		data := pack(t, []binpazer.TypeDef{{TypeID: 1, GUID: other, Name: "Other"}},
			[]testBlock{{typeID: 1, payload: []byte("x")}})
		_, err = DecodeTable(data)
		assert.ErrorContains(t, err, "no IconRules block")
	})
	t.Run("unknown critical block refused", func(t *testing.T) {
		data := pack(t, nil, []testBlock{{typeID: 7, flags: binpazer.FlagCritical, payload: []byte("x")}})
		_, err := DecodeTable(data)
		assert.ErrorContains(t, err, "unknown critical block")
	})
	t.Run("unknown ancillary block skipped", func(t *testing.T) {
		payload := payloadHead(0, 0).b
		guid, err := binpazer.ParseGUID(IconRulesGUID)
		require.NoError(t, err)
		data := pack(t,
			[]binpazer.TypeDef{{TypeID: 1, GUID: guid, Name: "IconRules"}},
			[]testBlock{
				{typeID: 9, payload: []byte("opaque")},
				{typeID: 1, flags: binpazer.FlagHasCRC, payload: payload},
			})
		tab, err := DecodeTable(data)
		require.NoError(t, err)
		assert.Empty(t, tab.FileRules)
		assert.Equal(t, Icon{Font: "fi", CP: 0xf011}, tab.DefFile)
	})
}

func TestDecodeRejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"wrong version", (&bw{}).u32(2).b, "version"},
		{"truncated header", (&bw{}).u32(1).u8(1).b, "truncated"},
		{"empty font name", (&bw{}).u32(1).u8(1).u8(0).b, "font class"},
		{"def font index out of range", (&bw{}).u32(1).u8(1).u8(2).raw("fi").u8(3).u32(1).b, "font index"},
		{"codepoint out of range", (&bw{}).u32(1).u8(1).u8(2).raw("fi").u8(0).u32(0x110000).b, "codepoint"},
		{"rule counts exceed payload", payloadHead(1000, 0).b, "exceed payload"},
		{"reserved kind bits", payloadHead(1, 0).u8(0x40).u32(1).u16(1).raw("x").b, "reserved kind bits"},
		{"rule font index out of range", payloadHead(1, 0).u8(0x05).u32(1).u16(1).raw("x").b, "font index"},
		{"empty pattern", payloadHead(1, 0).u8(0).u32(1).u16(0).b, "empty match pattern"},
		{"trailing garbage", payloadHead(0, 0).u8(0).b, "trailing garbage"},
		{
			"rule codepoint out of range",
			payloadHead(1, 0).u8(0).u32(0x110000).u16(1).raw("x").b,
			"codepoint",
		},
		{
			"truncated colour",
			payloadHead(1, 0).u8(0x10).u32(1).u8(0xaa).b,
			"truncated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeTable(packPayload(t, tc.payload))
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestDecodeAcceptsMinimalPayloads(t *testing.T) {
	// One colour-same regex rule with flags + one plain suffix rule +
	// one dir rule with a distinct light colour: every kind branch.
	p := payloadHead(2, 1)
	p.u8(0x08 | 0x10 | 0x20).u32(0x41).u8(0xaa).u8(0xbb).u8(0xcc).u8(1).raw("i").u16(4).raw("^ab$")
	p.u8(0).u32(0x42).u16(3).raw(".go")
	p.u8(0x10).u32(0x43).u8(1).u8(2).u8(3).u8(0x0a).u8(0x0b).u8(0x0c).u16(4).raw("test")
	tab, err := DecodeTable(packPayload(t, p.b))
	require.NoError(t, err)
	require.Len(t, tab.FileRules, 2)
	require.Len(t, tab.DirRules, 1)
	assert.Equal(t, Rule{Font: "fi", CP: 0x41, Regex: "^ab$", Flags: "i", Dark: "#aabbcc", Light: "#aabbcc"},
		tab.FileRules[0])
	assert.Equal(t, Rule{Font: "fi", CP: 0x42, Suffix: ".go"}, tab.FileRules[1])
	assert.Equal(t, Rule{Font: "fi", CP: 0x43, Dark: "#010203", Light: "#0a0b0c", Suffix: "test"},
		tab.DirRules[0])
}

func TestFallbackTable(t *testing.T) {
	tab := fallbackTable()
	assert.Empty(t, tab.FileRules)
	assert.Empty(t, tab.DirRules)
	assert.Equal(t, Icon{Font: "oct", CP: 0xf011}, tab.DefFile)
	assert.Equal(t, Icon{Font: "oct", CP: 0xf016}, tab.DefDir)
}
