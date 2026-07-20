package ffext

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Native-messaging frame caps. Firefox's documented limits: a message
// FROM the host to the extension may be at most 1 MB (an oversized
// frame kills the port), while a message TO the host may be up to
// 4 GB -- MaxInFrame is this side's own sanity cap well under that
// (a full tab dump is a few hundred KB at the pathological end).
const (
	MaxOutFrame = 1 << 20
	MaxInFrame  = 8 << 20
)

// ReadFrame reads one native-messaging frame from r: a 4-byte
// NATIVE-endian unsigned length prefix (MDN: "an unsigned 32-bit value
// containing the message length in native byte order"), then that many
// bytes of UTF-8 JSON. A declared length over max is an error (the
// stream is unrecoverable past a refused frame, so callers treat it as
// fatal). io.EOF is returned verbatim when the stream ends cleanly
// BEFORE a prefix; a stream ending mid-frame is io.ErrUnexpectedEOF.
func ReadFrame(r io.Reader, max uint32) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, err
		}
		return nil, err // io.EOF (clean end) or a real read error
	}
	n := binary.NativeEndian.Uint32(prefix[:])
	if n > max {
		return nil, fmt.Errorf("frame of %d bytes exceeds the %d-byte cap", n, max)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF // torn frame, never a clean end
		}
		return nil, err
	}
	return body, nil
}

// WriteFrame writes one native-messaging frame to w: the 4-byte
// native-endian length prefix and body, as a single Write call so a
// pipe reader never observes a torn prefix. Bodies over max are
// refused before anything is written.
func WriteFrame(w io.Writer, body []byte, max uint32) error {
	if uint64(len(body)) > uint64(max) {
		return fmt.Errorf("frame of %d bytes exceeds the %d-byte cap", len(body), max)
	}
	buf := make([]byte, 4+len(body))
	binary.NativeEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err := w.Write(buf)
	return err
}
