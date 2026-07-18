package launch

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSNRemoveMessage(t *testing.T) {
	msg := SNRemoveMessage("app-123_TIME456")
	require.Equal(t, []byte("remove: ID=\"app-123_TIME456\"\x00"), msg)

	escaped := SNRemoveMessage(`we"ird\id`)
	require.Equal(t, []byte("remove: ID=\"we\\\"ird\\\\id\"\x00"), escaped,
		"quotes and backslashes are backslash-escaped per the SN spec")
}

func TestSNChunks(t *testing.T) {
	t.Run("short payload pads one chunk", func(t *testing.T) {
		chunks := SNChunks([]byte("abc\x00"))
		require.Len(t, chunks, 1)
		require.Len(t, chunks[0], 20)
		require.Equal(t, []byte("abc\x00"), chunks[0][:4])
		require.Equal(t, bytes.Repeat([]byte{0}, 16), chunks[0][4:])
	})
	t.Run("exact multiple has no extra chunk", func(t *testing.T) {
		payload := bytes.Repeat([]byte{'x'}, 40)
		chunks := SNChunks(payload)
		require.Len(t, chunks, 2)
		require.Equal(t, payload[:20], chunks[0])
		require.Equal(t, payload[20:], chunks[1])
	})
	t.Run("long payload splits and pads the tail", func(t *testing.T) {
		msg := SNRemoveMessage("prgname-4242-host-binary-7_TIME98765432")
		chunks := SNChunks(msg)
		require.Len(t, chunks, (len(msg)+19)/20)
		var joined []byte
		for _, c := range chunks {
			require.Len(t, c, 20)
			joined = append(joined, c...)
		}
		require.Equal(t, msg, joined[:len(msg)])
		for _, b := range joined[len(msg):] {
			require.Zero(t, b, "tail padding is zero bytes")
		}
	})
	t.Run("empty payload still yields one chunk", func(t *testing.T) {
		chunks := SNChunks(nil)
		require.Len(t, chunks, 1)
		require.Equal(t, bytes.Repeat([]byte{0}, 20), chunks[0])
	})
}
