package firefox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// mozLz4Magic is the 8-byte header of Firefox's .jsonlz4 files
// ("mozLz40\0"). What follows is a 4-byte little-endian uncompressed
// size and raw LZ4 BLOCK-format data -- NOT the LZ4 frame format, so
// there is no frame magic and no checksums.
var mozLz4Magic = []byte("mozLz40\x00")

// DefaultMozLz4Cap bounds the declared uncompressed size accepted by
// DecodeMozLz4. Real session snapshots are a few megabytes; a declared
// size beyond this is treated as corruption, not a bigger allocation.
const DefaultMozLz4Cap = 64 << 20

// mozLz4HeaderLen is the magic plus the 4-byte size field.
const mozLz4HeaderLen = 12

// DecodeMozLz4 decodes one mozLz4 file (magic, declared size, LZ4
// block) and returns exactly the declared number of bytes. maxSize
// caps the declared size (non-positive = DefaultMozLz4Cap); anything
// larger is rejected as corruption. The block must decode to exactly
// the declared size -- overruns and underruns are errors -- and every
// read is bounds-checked, so corrupt or truncated input can never
// panic or over-allocate.
func DecodeMozLz4(data []byte, maxSize int) ([]byte, error) {
	if maxSize <= 0 {
		maxSize = DefaultMozLz4Cap
	}
	if len(data) < mozLz4HeaderLen {
		return nil, errors.New("mozlz4: truncated header")
	}
	if !bytes.Equal(data[:len(mozLz4Magic)], mozLz4Magic) {
		return nil, errors.New("mozlz4: bad magic")
	}
	size := int64(binary.LittleEndian.Uint32(data[len(mozLz4Magic):mozLz4HeaderLen]))
	if size > int64(maxSize) {
		return nil, fmt.Errorf("mozlz4: declared size %d exceeds the %d-byte cap", size, maxSize)
	}
	out, err := lz4BlockDecode(data[mozLz4HeaderLen:], int(size))
	if err != nil {
		return nil, fmt.Errorf("mozlz4: %w", err)
	}
	return out, nil
}

// lz4BlockDecode decodes LZ4 block-format src into exactly size output
// bytes. The block is a run of sequences: a token byte (high nibble =
// literal count, low nibble = match length base), each nibble of 15
// extended by 0xFF-chained bytes (every extension byte is added; the
// first non-0xFF byte ends the chain), the literals, and -- unless the
// input ends right after the literals, which is the legal final
// sequence -- a 2-byte little-endian match offset (1..65535; 0 is
// invalid) and a match of low-nibble+4 bytes copied from the already-
// produced output. Match copies run byte-by-byte FORWARD because an
// offset smaller than the length overlaps the bytes it is producing
// (self-replication, e.g. offset 1 repeating one byte) and is legal.
func lz4BlockDecode(src []byte, size int) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("negative size")
	}
	dst := make([]byte, 0, size)
	if len(src) == 0 {
		if size != 0 {
			return nil, errors.New("empty block for a non-empty declared size")
		}
		return dst, nil
	}
	for i := 0; ; {
		if i >= len(src) {
			return nil, errors.New("truncated block: missing token")
		}
		token := src[i]
		i++

		litLen, n, err := lz4Length(int(token>>4), src[i:])
		if err != nil {
			return nil, fmt.Errorf("literal length: %w", err)
		}
		i += n
		if litLen > len(src)-i {
			return nil, errors.New("truncated block: literals run past the input")
		}
		if litLen > size-len(dst) {
			return nil, errors.New("output overrun: literals exceed the declared size")
		}
		dst = append(dst, src[i:i+litLen]...)
		i += litLen

		if i == len(src) {
			// Final sequence: literals only, no match. The block must
			// have produced exactly the declared size.
			if len(dst) != size {
				return nil, fmt.Errorf("declared size %d but decoded %d bytes", size, len(dst))
			}
			return dst, nil
		}

		if len(src)-i < 2 {
			return nil, errors.New("truncated block: missing match offset")
		}
		offset := int(binary.LittleEndian.Uint16(src[i : i+2]))
		i += 2
		if offset == 0 {
			return nil, errors.New("invalid match offset 0")
		}
		if offset > len(dst) {
			return nil, fmt.Errorf("match offset %d reaches before the output start", offset)
		}
		matchLen, n, err := lz4Length(int(token&0x0F), src[i:])
		if err != nil {
			return nil, fmt.Errorf("match length: %w", err)
		}
		i += n
		matchLen += 4 // minmatch: the low nibble encodes length-4
		if matchLen > size-len(dst) {
			return nil, errors.New("output overrun: match exceeds the declared size")
		}
		for j := 0; j < matchLen; j++ {
			dst = append(dst, dst[len(dst)-offset])
		}
	}
}

// lz4Length resolves one token nibble to its full length: a nibble
// under 15 is the length itself; 15 means extension bytes follow, each
// added to the length, the chain ending at the first byte that is not
// 0xFF. It returns the length and how many extension bytes were read.
func lz4Length(nibble int, src []byte) (length, read int, err error) {
	length = nibble
	if nibble < 15 {
		return length, 0, nil
	}
	for {
		if read >= len(src) {
			return 0, 0, errors.New("truncated extension bytes")
		}
		b := src[read]
		read++
		length += int(b)
		if b != 0xFF {
			return length, read, nil
		}
	}
}
