package ffext

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msgs := [][]byte{
		[]byte(`{"id":1,"type":"listTabs"}`),
		[]byte(`{}`),
		[]byte(strings.Repeat("x", 100_000)),
	}
	for _, m := range msgs {
		require.NoError(t, WriteFrame(&buf, m, MaxOutFrame))
	}
	for _, want := range msgs {
		got, err := ReadFrame(&buf, MaxInFrame)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := ReadFrame(&buf, MaxInFrame)
	require.ErrorIs(t, err, io.EOF, "clean stream end is io.EOF")
}

func TestFramePrefixIsNativeEndian(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, []byte("abcde"), MaxOutFrame))
	raw := buf.Bytes()
	require.Len(t, raw, 4+5)
	require.Equal(t, uint32(5), binary.NativeEndian.Uint32(raw[:4]))
	require.Equal(t, "abcde", string(raw[4:]))
}

func TestFrameZeroLength(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, nil, MaxOutFrame))
	got, err := ReadFrame(&buf, MaxInFrame)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWriteFrameRefusesOversize(t *testing.T) {
	var buf bytes.Buffer
	err := WriteFrame(&buf, make([]byte, 11), 10)
	require.Error(t, err)
	require.Zero(t, buf.Len(), "nothing written for a refused frame")
}

func TestReadFrameRefusesOversizeDeclaration(t *testing.T) {
	var buf bytes.Buffer
	var prefix [4]byte
	binary.NativeEndian.PutUint32(prefix[:], 1<<30)
	buf.Write(prefix[:])
	buf.WriteString("body")
	_, err := ReadFrame(&buf, MaxInFrame)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cap")
}

func TestReadFrameTornStreams(t *testing.T) {
	// A stream ending inside the prefix.
	_, err := ReadFrame(bytes.NewReader([]byte{1, 0}), MaxInFrame)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)

	// A stream ending inside the declared body.
	var buf bytes.Buffer
	var prefix [4]byte
	binary.NativeEndian.PutUint32(prefix[:], 10)
	buf.Write(prefix[:])
	buf.WriteString("short")
	_, err = ReadFrame(&buf, MaxInFrame)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
