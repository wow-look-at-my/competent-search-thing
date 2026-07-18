package watch

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// The builders below hand-assemble fanotify wire records with
// independent arithmetic (never the parser's constants), mirroring the
// layouts in fanotify(7) "Reading fanotify events".

// fanoInfoRec builds one info record: {info_type, pad, len} + fsid +
// file_handle{handle_bytes, handle_type, f_handle} + name bytes. nul
// appends the kernel's NUL terminator; pad appends alignment padding
// after it (both inside the record's len, like the kernel).
func fanoInfoRec(infoType byte, fsid fanoFsid, handleType int32, handle []byte, name string, nul bool, pad int) []byte {
	le := binary.LittleEndian
	b := []byte{infoType, 0, 0, 0}
	b = le.AppendUint32(b, uint32(fsid[0]))
	b = le.AppendUint32(b, uint32(fsid[1]))
	b = le.AppendUint32(b, uint32(len(handle)))
	b = le.AppendUint32(b, uint32(handleType))
	b = append(b, handle...)
	b = append(b, name...)
	if nul {
		b = append(b, 0)
	}
	for i := 0; i < pad; i++ {
		b = append(b, 0)
	}
	le.PutUint16(b[2:4], uint16(len(b)))
	return b
}

// fanoMeta builds one fanotify_event_metadata record (24 bytes) with
// the info payload appended and event_len covering both.
func fanoMeta(mask uint64, vers byte, info []byte) []byte {
	le := binary.LittleEndian
	b := make([]byte, 24)
	le.PutUint32(b[0:4], uint32(24+len(info)))
	b[4] = vers
	le.PutUint16(b[6:8], 24)
	le.PutUint64(b[8:16], mask)
	le.PutUint32(b[16:20], ^uint32(0)) // fd = FAN_NOFD
	le.PutUint32(b[20:24], 12345)      // pid
	return append(b, info...)
}

// fanoRec is the common case: one event with one DFID_NAME record.
func fanoRec(mask uint64, fsid fanoFsid, handle []byte, name string) []byte {
	return fanoMeta(mask, 3,
		fanoInfoRec(byte(unix.FAN_EVENT_INFO_TYPE_DFID_NAME), fsid, 1, handle, name, true, 0))
}

func concat(bufs ...[]byte) []byte {
	var out []byte
	for _, b := range bufs {
		out = append(out, b...)
	}
	return out
}

func TestParseFanotifyBuf(t *testing.T) {
	fsid := fanoFsid{7, 9}
	h := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0x01, 0x02, 0x03, 0x04}
	dfid := byte(unix.FAN_EVENT_INFO_TYPE_DFID_NAME)
	ev := func(mask uint64, name string) fanoEvent {
		return fanoEvent{Mask: mask, Fsid: fsid, HandleType: 1, Handle: h, Name: name}
	}
	cases := []struct {
		name     string
		buf      []byte
		want     []fanoEvent
		overflow bool
	}{
		{name: "empty buffer"},
		{name: "short garbage", buf: []byte{1, 2, 3}},
		{
			name: "single create",
			buf:  fanoRec(unix.FAN_CREATE, fsid, h, "file.txt"),
			want: []fanoEvent{ev(unix.FAN_CREATE, "file.txt")},
		},
		{
			name: "multi-event buffer",
			buf: concat(
				fanoRec(unix.FAN_CREATE, fsid, h, "a"),
				fanoRec(unix.FAN_DELETE, fsid, h, "b"),
				fanoRec(unix.FAN_MOVED_TO, fsid, h, "c"),
			),
			want: []fanoEvent{ev(unix.FAN_CREATE, "a"), ev(unix.FAN_DELETE, "b"), ev(unix.FAN_MOVED_TO, "c")},
		},
		{
			name: "merged mask create|delete (kernel event merging)",
			buf:  fanoRec(unix.FAN_CREATE|unix.FAN_DELETE, fsid, h, "churn"),
			want: []fanoEvent{ev(unix.FAN_CREATE|unix.FAN_DELETE, "churn")},
		},
		{
			name: "ondir bit preserved",
			buf:  fanoRec(unix.FAN_CREATE|unix.FAN_ONDIR, fsid, h, "subdir"),
			want: []fanoEvent{ev(unix.FAN_CREATE|unix.FAN_ONDIR, "subdir")},
		},
		{
			name:     "overflow record alone",
			buf:      fanoMeta(unix.FAN_Q_OVERFLOW, 3, nil),
			overflow: true,
		},
		{
			name: "overflow among real events",
			buf: concat(
				fanoRec(unix.FAN_CREATE, fsid, h, "before"),
				fanoMeta(unix.FAN_Q_OVERFLOW, 3, nil),
				fanoRec(unix.FAN_DELETE, fsid, h, "after"),
			),
			want:     []fanoEvent{ev(unix.FAN_CREATE, "before"), ev(unix.FAN_DELETE, "after")},
			overflow: true,
		},
		{
			name: "non-DFID info record skipped",
			buf: fanoMeta(unix.FAN_CREATE, 3,
				fanoInfoRec(byte(unix.FAN_EVENT_INFO_TYPE_FID), fsid, 1, h, "", false, 0)),
		},
		{
			name: "FID record skipped, DFID_NAME after it still parsed",
			buf: fanoMeta(unix.FAN_CREATE, 3, concat(
				fanoInfoRec(byte(unix.FAN_EVENT_INFO_TYPE_FID), fsid, 1, h, "", false, 0),
				fanoInfoRec(dfid, fsid, 1, h, "kept", true, 0),
			)),
			want: []fanoEvent{ev(unix.FAN_CREATE, "kept")},
		},
		{
			name: "unknown metadata version skipped whole",
			buf: concat(
				fanoMeta(unix.FAN_CREATE, 2, fanoInfoRec(dfid, fsid, 1, h, "old-abi", true, 0)),
				fanoRec(unix.FAN_CREATE, fsid, h, "ok"),
			),
			want: []fanoEvent{ev(unix.FAN_CREATE, "ok")},
		},
		{
			name: "zero-length name",
			buf:  fanoRec(unix.FAN_CREATE, fsid, h, ""),
			want: []fanoEvent{ev(unix.FAN_CREATE, "")},
		},
		{
			name: "alignment padding after the NUL ignored",
			buf: fanoMeta(unix.FAN_CREATE, 3,
				fanoInfoRec(dfid, fsid, 1, h, "padded", true, 3)),
			want: []fanoEvent{ev(unix.FAN_CREATE, "padded")},
		},
		{
			name: "zero-length handle",
			buf:  fanoRec(unix.FAN_CREATE, fsid, nil, "nohandle"),
			want: []fanoEvent{{Mask: unix.FAN_CREATE, Fsid: fsid, HandleType: 1, Name: "nohandle"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs, overflow := parseFanotifyBuf(tc.buf)
			require.Equal(t, tc.overflow, overflow)
			require.Len(t, evs, len(tc.want))
			for i := range tc.want {
				require.Equal(t, tc.want[i], evs[i], "event %d", i)
			}
		})
	}
}

func TestParseFanotifyBufTruncation(t *testing.T) {
	fsid := fanoFsid{1, 2}
	h := []byte{9, 8, 7, 6}
	full := fanoRec(unix.FAN_CREATE, fsid, h, "file.txt")

	// Cut the buffer at EVERY byte boundary: never a panic, and only a
	// complete record parses.
	for cut := 0; cut <= len(full); cut++ {
		evs, overflow := parseFanotifyBuf(full[:cut])
		require.False(t, overflow, "cut=%d", cut)
		if cut < len(full) {
			require.Empty(t, evs, "cut=%d: a truncated record must not parse", cut)
		} else {
			require.Len(t, evs, 1)
		}
	}

	// With a second record appended, the intact first record parses at
	// every truncation point of the second.
	two := concat(full, full)
	for cut := len(full); cut < len(two); cut++ {
		evs, _ := parseFanotifyBuf(two[:cut])
		require.Len(t, evs, 1, "cut=%d", cut)
	}
}

func TestParseFanotifyBufMalformed(t *testing.T) {
	fsid := fanoFsid{1, 2}
	h := []byte{9, 8, 7, 6}
	le := binary.LittleEndian
	good := fanoRec(unix.FAN_CREATE, fsid, h, "good")

	t.Run("event_len smaller than the metadata ends the walk", func(t *testing.T) {
		bad := fanoRec(unix.FAN_CREATE, fsid, h, "bad")
		le.PutUint32(bad[0:4], 8) // < 24: cannot advance
		evs, overflow := parseFanotifyBuf(concat(bad, good))
		require.Empty(t, evs)
		require.False(t, overflow)
	})

	t.Run("metadata_len beyond event_len ends the walk", func(t *testing.T) {
		bad := fanoRec(unix.FAN_CREATE, fsid, h, "bad")
		le.PutUint16(bad[6:8], uint16(len(bad)+10))
		evs, _ := parseFanotifyBuf(concat(bad, good))
		require.Empty(t, evs)
	})

	t.Run("info len overrunning the event skips its info walk only", func(t *testing.T) {
		info := fanoInfoRec(byte(unix.FAN_EVENT_INFO_TYPE_DFID_NAME), fsid, 1, h, "bad", true, 0)
		le.PutUint16(info[2:4], uint16(len(info)+40)) // lies past the record
		evs, _ := parseFanotifyBuf(concat(fanoMeta(unix.FAN_CREATE, 3, info), good))
		require.Len(t, evs, 1, "the following record still parses")
		require.Equal(t, "good", evs[0].Name)
	})

	t.Run("handle_bytes overrunning the record skips it", func(t *testing.T) {
		info := fanoInfoRec(byte(unix.FAN_EVENT_INFO_TYPE_DFID_NAME), fsid, 1, h, "bad", true, 0)
		le.PutUint32(info[12:16], 200) // handle_bytes lies
		evs, _ := parseFanotifyBuf(concat(fanoMeta(unix.FAN_CREATE, 3, info), good))
		require.Len(t, evs, 1)
		require.Equal(t, "good", evs[0].Name)
	})

	t.Run("info record shorter than its fixed prefix skipped", func(t *testing.T) {
		info := []byte{byte(unix.FAN_EVENT_INFO_TYPE_DFID_NAME), 0, 6, 0, 0, 0}
		evs, _ := parseFanotifyBuf(concat(fanoMeta(unix.FAN_CREATE, 3, info), good))
		require.Len(t, evs, 1)
		require.Equal(t, "good", evs[0].Name)
	})
}

func TestParseFanotifyBufCopiesHandles(t *testing.T) {
	// The read loop reuses its buffer between reads, so parsed events
	// must not alias it.
	fsid := fanoFsid{3, 4}
	buf := fanoRec(unix.FAN_CREATE, fsid, []byte{1, 2, 3, 4}, "f")
	evs, _ := parseFanotifyBuf(buf)
	require.Len(t, evs, 1)
	for i := range buf {
		buf[i] = 0xff
	}
	require.Equal(t, []byte{1, 2, 3, 4}, evs[0].Handle)
	require.Equal(t, "f", evs[0].Name)
}
