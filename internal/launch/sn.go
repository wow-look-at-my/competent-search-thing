package launch

import "strings"

// snChunkSize is the payload size of one libstartup-notification
// ClientMessage: format 8, 20 data bytes.
const snChunkSize = 20

// SNRemoveMessage builds the libstartup-notification broadcast that
// ends startup sequence id: the wire string `remove: ID="<id>"` with
// the value backslash-escaped per the spec ('"' and '\') plus the
// terminating NUL. Broadcasting it is how a launcher reaps a sequence
// whose launchee never completes startup notification (the
// chromium-family case) -- without it, mutter keeps the busy cursor
// until its 15-second timeout.
func SNRemoveMessage(id string) []byte {
	var b strings.Builder
	b.WriteString(`remove: ID="`)
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == '"' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteString(`"`)
	return append([]byte(b.String()), 0)
}

// SNChunks splits a wire payload into the 20-byte ClientMessage
// chunks the X broadcast sends: the first chunk rides a
// _NET_STARTUP_INFO_BEGIN message, every following one
// _NET_STARTUP_INFO, and the last is zero-padded to the full 20
// bytes. Always at least one chunk.
func SNChunks(payload []byte) [][]byte {
	var chunks [][]byte
	for start := 0; start == 0 || start < len(payload); start += snChunkSize {
		chunk := make([]byte, snChunkSize)
		if start < len(payload) {
			copy(chunk, payload[start:])
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}
