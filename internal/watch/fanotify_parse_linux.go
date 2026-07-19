package watch

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// fanotify wire-format parsing, kept PURE (no syscalls, no state) so
// the byte walk is unit-testable without privileges. The layouts come
// from fanotify(7) "Reading fanotify events": a stream of
// fanotify_event_metadata records (24 bytes; event_len is the offset
// to the next record and includes the info records), each followed by
// info records of the shape {info_type u8, pad u8, len u16} + payload.
// A FAN_EVENT_INFO_TYPE_DFID_NAME payload is __kernel_fsid_t (8
// bytes) + struct file_handle {handle_bytes u32, handle_type i32,
// f_handle...} + the NUL-terminated entry name.
//
// All supported linux targets are little-endian, so the fixed-width
// fields decode with binary.LittleEndian (this file only compiles on
// linux; the windows cross-build never sees it).

const (
	// fanoMetaLen is the fanotify_event_metadata size (x/sys pins the
	// same value as FAN_EVENT_METADATA_LEN).
	fanoMetaLen = 24
	// fanoInfoFixed is the fixed prefix of a DFID_NAME info record:
	// header (4) + fsid (8) + handle_bytes (4) + handle_type (4).
	fanoInfoFixed = 20
)

// fanoFsid is __kernel_fsid_t, shaped like unix.Fsid.Val so statfs
// results compare directly against event records; it keys the
// fsid -> mount-fd routing table.
type fanoFsid [2]int32

// fanoEvent is one parsed directory-entry event: the (possibly
// mask-merged) event bits, the parent directory's filesystem + file
// handle, and the entry name. The handle bytes are copied out of the
// read buffer, so events stay valid after the buffer is reused.
type fanoEvent struct {
	Mask       uint64
	Fsid       fanoFsid
	HandleType int32
	Handle     []byte
	Name       string
}

// parseFanotifyBuf walks one read's worth of fanotify records and
// returns the DFID_NAME dirent events plus whether any record carried
// FAN_Q_OVERFLOW (kernel queue overflowed: events were lost). The
// walk NEVER panics on truncated or malformed input: a record whose
// length fields cannot advance the walk ends it, a record of an
// unknown metadata version is skipped whole, and an info record that
// overruns its bounds is skipped -- fanotify provides no ordering or
// layout guarantees beyond the length fields, so every consumer
// downstream treats events as advisory dirty paths anyway.
func parseFanotifyBuf(buf []byte) (events []fanoEvent, overflow bool) {
	le := binary.LittleEndian
	for len(buf) >= fanoMetaLen {
		eventLen := int(le.Uint32(buf[0:4]))
		vers := buf[4]
		metaLen := int(le.Uint16(buf[6:8]))
		if eventLen < fanoMetaLen || eventLen > len(buf) || metaLen < fanoMetaLen || metaLen > eventLen {
			// Malformed length fields: the walk cannot advance
			// reliably, so whatever parsed already is the answer.
			return events, overflow
		}
		mask := le.Uint64(buf[8:16])
		info := buf[metaLen:eventLen]
		buf = buf[eventLen:]
		if vers != unix.FANOTIFY_METADATA_VERSION {
			continue // unknown ABI revision: skip the record whole
		}
		if mask&unix.FAN_Q_OVERFLOW != 0 {
			overflow = true
			continue // overflow records carry no usable object info
		}
		for len(info) >= 4 {
			infoType := info[0]
			infoLen := int(le.Uint16(info[2:4]))
			if infoLen < 4 || infoLen > len(info) {
				break // malformed info header: stop walking this event
			}
			rec := info[:infoLen]
			info = info[infoLen:]
			if infoType != unix.FAN_EVENT_INFO_TYPE_DFID_NAME {
				continue // FID/PIDFD/...: not subscribed, skip
			}
			if len(rec) < fanoInfoFixed {
				continue
			}
			fsid := fanoFsid{int32(le.Uint32(rec[4:8])), int32(le.Uint32(rec[8:12]))}
			hb := int(le.Uint32(rec[12:16]))
			ht := int32(le.Uint32(rec[16:20]))
			if hb < 0 || fanoInfoFixed+hb > len(rec) {
				continue // handle overruns the record: skip it
			}
			handle := append([]byte(nil), rec[fanoInfoFixed:fanoInfoFixed+hb]...)
			name := rec[fanoInfoFixed+hb:]
			if i := bytes.IndexByte(name, 0); i >= 0 {
				name = name[:i] // NUL-terminated; records pad past it
			}
			events = append(events, fanoEvent{
				Mask:       mask,
				Fsid:       fsid,
				HandleType: ht,
				Handle:     handle,
				Name:       string(name),
			})
		}
	}
	return events, overflow
}
