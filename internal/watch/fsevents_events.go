package watch

import (
	"path/filepath"
	"runtime"
)

// The pure half of the darwin FSEvents backend: flag constants, the
// per-record intake decision, and the symlink path translator. This
// file is deliberately UNTAGGED -- it compiles and is unit-tested on
// every platform (the linux CI job included), mirroring what
// fanotify_parse_linux.go does for the fanotify backend (that file is
// linux-gated only because it needs unix types; this one needs none).
// The cgo glue that feeds these functions lives in fsevents_darwin.go.

// FSEventStream event flags, copied from FSEvents.h
// (kFSEventStreamEventFlag*; CoreServices FSEvents.framework). The
// values are a stable ABI -- they are bits of the on-wire
// FSEventStreamEventFlags word and have not changed since they were
// introduced (10.5-10.7 era). Higher bits (kFSEventStreamEventFlag
// ItemIsFile 0x10000, ItemIsDir 0x20000, ItemIsSymlink 0x40000,
// OwnEvent 0x80000, ...) are deliberately not mirrored: intake never
// branches on them -- lstat at reconcile time is the kind authority.
const (
	fseMustScanSubDirs   uint32 = 0x00000001
	fseUserDropped       uint32 = 0x00000002
	fseKernelDropped     uint32 = 0x00000004
	fseEventIdsWrapped   uint32 = 0x00000008
	fseHistoryDone       uint32 = 0x00000010
	fseRootChanged       uint32 = 0x00000020
	fseMount             uint32 = 0x00000040
	fseUnmount           uint32 = 0x00000080
	fseItemCreated       uint32 = 0x00000100
	fseItemRemoved       uint32 = 0x00000200
	fseItemInodeMetaMod  uint32 = 0x00000400
	fseItemRenamed       uint32 = 0x00000800
	fseItemModified      uint32 = 0x00001000
	fseItemFinderInfoMod uint32 = 0x00002000
	fseItemChangeOwner   uint32 = 0x00004000
	fseItemXattrMod      uint32 = 0x00008000
)

// fseOverflowMask marks records that mean EVENTS WERE LOST (or their
// ids wrapped): the watcher must degrade and request a sweep, exactly
// like an inotify/fanotify queue overflow. MustScanSubDirs also
// carries a usable subtree root, which fseDecide still emits as a
// dirty path (the shallow reconcile is free; the requested sweep does
// the depth).
const fseOverflowMask = fseMustScanSubDirs | fseUserDropped |
	fseKernelDropped | fseEventIdsWrapped

// fseNameMask marks flags that can change the set of indexed NAMES.
// A record carrying none of these (and no overflow bit) is pure
// content/metadata churn -- the FSEvents equivalent of wantEvent's
// Write/Chmod drop. Mount/Unmount are name-changing by nature (a
// mount replaces a directory's visible content), and RootChanged is
// included defensively even though the stream never sets WatchRoot.
const fseNameMask = fseItemCreated | fseItemRemoved | fseItemRenamed |
	fseMount | fseUnmount | fseRootChanged

// Delivery-channel bounds for the FSEvents notifier, matching the
// fanotify backend's semantics (fanoEventBuf/fanoErrBuf are declared
// in the linux-only file, so darwin needs its own copies): a full
// events channel drops the event and synthesizes one overflow error --
// kernel-queue semantics in miniature, better an overflow signal
// driving a sweep than an unbounded queue or a blocked callback.
const (
	fseEventBuf = 1024
	fseErrBuf   = 16
)

// fsePathTranslator maps event paths reported under a RESOLVED root
// spelling back to the CONFIGURED spelling. macOS reports FSEvents
// paths with symlinks resolved at the stream root (/tmp, /var and
// /etc are symlinks into /private), so a stream created for the
// configured root /tmp delivers /private/tmp/... paths; without the
// translation the index would fork into /private twins of the
// configured roots. The zero value translates nothing (the default
// config's root "/" resolves to itself, so no pairs exist).
type fsePathTranslator struct {
	pairs [][2]string // [resolved prefix, configured prefix]
}

// add registers one resolved -> configured prefix pair. Identical
// spellings are dropped (nothing to translate).
func (tr *fsePathTranslator) add(resolved, configured string) {
	if resolved == "" || resolved == configured {
		return
	}
	tr.pairs = append(tr.pairs, [2]string{resolved, configured})
}

// translate maps one event path back to the configured spelling: the
// first pair whose resolved prefix covers the path (path-boundary
// aware, never a bare string prefix) has its configured prefix
// swapped in. Paths under no pair pass through verbatim.
func (tr fsePathTranslator) translate(path string) string {
	for _, p := range tr.pairs {
		if pathWithin(path, p[0]) {
			return p[1] + path[len(p[0]):]
		}
	}
	return path
}

// fseDecide is the ONE intake decision for a raw FSEvents record,
// pure so the linux CI job tests it: it reports the (translated,
// cleaned) dirty path to emit, whether the record signals overflow
// (events were lost -- degrade and sweep), and whether the path
// should be emitted at all. The two outputs are independent: an
// overflow record outside the roots still degrades the watcher, it
// just emits no path.
//
// Drop rules, in order: a HistoryDone-only record is noise (the
// stream starts SinceNow, so history never replays; guarded anyway);
// a record whose flags carry ONLY content/metadata bits (no name
// bit, no overflow bit) cannot change the set of indexed names --
// note flags==0 (possible under coalescing) is KEPT: fail open into
// a reconcile, never fail silent; a path outside every configured
// root is out of index scope (a stream rooted at "/" sees
// everything, exactly like fanotify's whole-superblock marks).
func fseDecide(path string, flags uint32, roots []string, tr fsePathTranslator) (emit string, overflow, ok bool) {
	overflow = flags&fseOverflowMask != 0
	if !overflow {
		if flags == fseHistoryDone {
			return "", false, false
		}
		if flags != 0 && flags&fseNameMask == 0 {
			return "", false, false
		}
	}
	p := filepath.Clean(tr.translate(path))
	for _, r := range roots {
		if pathWithin(p, r) {
			return p, overflow, true
		}
	}
	return "", overflow, false
}

// PerDirBackendName is the honest runtime label for the
// per-directory fsnotify backend on this OS: fsnotify implements it
// with inotify on linux, kqueue on darwin and the BSDs, and
// ReadDirectoryChangesW on windows (labeled "windows"). The config
// VALUE "inotify" keeps selecting the per-directory backend on every
// OS -- only the runtime label is per-OS. Exported for the app
// layer's tests (the summary log and the watch:backend payload carry
// it).
func PerDirBackendName() string {
	switch runtime.GOOS {
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return "kqueue"
	case "windows":
		return "windows"
	default:
		return "inotify"
	}
}
