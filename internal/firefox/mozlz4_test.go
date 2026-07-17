package firefox

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mozLz4File assembles a raw mozLz4 file from an explicit declared
// size and block bytes, valid or not -- the hand-vector builder.
func mozLz4File(size uint32, block []byte) []byte {
	out := append([]byte(nil), mozLz4Magic...)
	out = binary.LittleEndian.AppendUint32(out, size)
	return append(out, block...)
}

// mozLz4Compress is the tiny test-only reference compressor: a valid
// mozLz4 file whose LZ4 block encodes data as ONE literals-only
// sequence (correct, if uncompressed -- the match paths are covered by
// the hand vectors). The session-store fixtures build on it.
func mozLz4Compress(data []byte) []byte {
	return mozLz4File(uint32(len(data)), lz4LiteralsBlock(data))
}

// lz4LiteralsBlock emits one literals-only LZ4 block sequence,
// 0xFF-chaining the length when it exceeds the token nibble.
func lz4LiteralsBlock(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	var block []byte
	if n := len(data); n < 15 {
		block = append(block, byte(n)<<4)
	} else {
		block = append(block, 0xF0)
		rem := n - 15
		for rem >= 255 {
			block = append(block, 0xFF)
			rem -= 255
		}
		block = append(block, byte(rem))
	}
	return append(block, data...)
}

func TestDecodeMozLz4HandVectors(t *testing.T) {
	tests := []struct {
		name string
		size uint32
		blk  []byte
		want string
	}{
		{
			name: "literals only",
			size: 3,
			blk:  []byte{0x30, 'a', 'b', 'c'},
			want: "abc",
		},
		{
			name: "zero size empty block",
			size: 0,
			blk:  nil,
			want: "",
		},
		{
			name: "zero literal final token after a match",
			size: 8,
			blk:  []byte{0x40, 'a', 'b', 'c', 'd', 0x04, 0x00, 0x00},
			want: "abcdabcd",
		},
		{
			name: "match copies earlier output",
			size: 9,
			// 4 literals "abcd", match offset 4 len 4, final literal "X".
			blk:  []byte{0x40, 'a', 'b', 'c', 'd', 0x04, 0x00, 0x10, 'X'},
			want: "abcdabcdX",
		},
		{
			name: "overlapping match self-replicates",
			size: 8,
			// 1 literal 'a', match offset 1 len 3+4=7: seven more 'a's.
			blk:  []byte{0x13, 'a', 0x01, 0x00, 0x00},
			want: "aaaaaaaa",
		},
		{
			name: "literal length 0xFF chain",
			size: 273,
			// 15 + 255 + 3 = 273 literals.
			blk:  append([]byte{0xF0, 0xFF, 0x03}, bytes.Repeat([]byte{'L'}, 273)...),
			want: strings.Repeat("L", 273),
		},
		{
			name: "match length 0xFF chain",
			size: 276,
			// 1 literal 'b', offset 1, match 15+255+1+4=275, final token.
			blk:  []byte{0x1F, 'b', 0x01, 0x00, 0xFF, 0x01, 0x00},
			want: strings.Repeat("b", 276),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeMozLz4(mozLz4File(tt.size, tt.blk), 0)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestDecodeMozLz4Rejects(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		maxSize int
		errPart string
	}{
		{
			name:    "truncated header",
			data:    []byte("mozLz40\x00\x03"),
			errPart: "truncated header",
		},
		{
			name:    "bad magic",
			data:    append([]byte("mozLz41\x00"), 0x03, 0, 0, 0, 0x30, 'a', 'b', 'c'),
			errPart: "bad magic",
		},
		{
			name:    "oversize declared size",
			data:    mozLz4File(101, []byte{0x30, 'a', 'b', 'c'}),
			maxSize: 100,
			errPart: "exceeds the 100-byte cap",
		},
		{
			name:    "oversize against the default cap",
			data:    mozLz4File(1<<31-1, []byte{0x30, 'a', 'b', 'c'}),
			errPart: "exceeds",
		},
		{
			name:    "empty block for non-empty size",
			data:    mozLz4File(3, nil),
			errPart: "empty block",
		},
		{
			name:    "truncated literals",
			data:    mozLz4File(3, []byte{0x30, 'a', 'b'}),
			errPart: "literals run past the input",
		},
		{
			name:    "truncated literal extension chain",
			data:    mozLz4File(600, []byte{0xF0, 0xFF, 0xFF}),
			errPart: "literal length: truncated extension bytes",
		},
		{
			name:    "missing match offset byte",
			data:    mozLz4File(9, []byte{0x40, 'a', 'b', 'c', 'd', 0x04}),
			errPart: "missing match offset",
		},
		{
			name:    "truncated match extension chain",
			data:    mozLz4File(600, []byte{0x1F, 'b', 0x01, 0x00, 0xFF}),
			errPart: "match length: truncated extension bytes",
		},
		{
			name:    "match offset zero",
			data:    mozLz4File(9, []byte{0x40, 'a', 'b', 'c', 'd', 0x00, 0x00, 0x10, 'X'}),
			errPart: "invalid match offset 0",
		},
		{
			name:    "match offset before output start",
			data:    mozLz4File(9, []byte{0x40, 'a', 'b', 'c', 'd', 0x05, 0x00, 0x10, 'X'}),
			errPart: "before the output start",
		},
		{
			name:    "declared size underrun",
			data:    mozLz4File(5, []byte{0x30, 'a', 'b', 'c'}),
			errPart: "declared size 5 but decoded 3 bytes",
		},
		{
			name:    "literals overrun the declared size",
			data:    mozLz4File(2, []byte{0x30, 'a', 'b', 'c'}),
			errPart: "literals exceed the declared size",
		},
		{
			name: "match overruns the declared size",
			// 4 literals + 4-byte match = 8 > declared 6.
			data:    mozLz4File(6, []byte{0x40, 'a', 'b', 'c', 'd', 0x04, 0x00, 0x00}),
			errPart: "match exceeds the declared size",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeMozLz4(tt.data, tt.maxSize)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errPart)
			require.Nil(t, got)
		})
	}
}

func TestDecodeMozLz4RoundTrip(t *testing.T) {
	payloads := map[string][]byte{
		"empty":      nil,
		"one byte":   {0x42},
		"fourteen":   []byte("fourteen-bytes"),
		"fifteen":    []byte("fifteen--bytes!"),
		"json-ish":   []byte(`{"windows":[{"tabs":[{"entries":[{"url":"https://example.org/"}]}]}]}`),
		"chain edge": bytes.Repeat([]byte{'x'}, 15+255), // extension chain ending in 0x00
	}
	// A deterministic pseudo-random blob big enough to need several
	// 0xFF extension bytes.
	big := make([]byte, 100_000)
	state := uint32(0x1234_5678)
	for i := range big {
		state = state*1664525 + 1013904223
		big[i] = byte(state >> 24)
	}
	payloads["100k pseudo-random"] = big

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeMozLz4(mozLz4Compress(payload), 0)
			require.NoError(t, err)
			require.Equal(t, payload, append([]byte(nil), got...))
		})
	}
}
