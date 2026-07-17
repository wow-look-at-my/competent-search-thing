package index

// Footprint is the Store's memory accounting, for diagnostics and
// capacity planning (how many bytes a whole-filesystem index costs).
// The slice/blob fields are exact (len-based; spare append capacity is
// not counted). The two map-backed fields are documented
// approximations: Go maps do not expose their layout, so they use a
// fixed per-entry overhead estimate, and the dirIndex keys share their
// byte data with Dirs' strings (interning), so DirIndexApproxBytes
// errs high by design.
type Footprint struct {
	// Entries is the total entry count including tombstones,
	// LiveEntries the non-tombstoned count, Dirs the interned
	// parent-directory count.
	Entries     int
	LiveEntries int
	Dirs        int

	// NameLowerBytes / NameOrigBytes are the two name blobs
	// (0x00-separated); OffsetBytes covers both uint32 offset tables;
	// ParentBytes the entry->dir column; FlagBytes the per-entry flag
	// column.
	NameLowerBytes int64
	NameOrigBytes  int64
	OffsetBytes    int64
	ParentBytes    int64
	FlagBytes      int64

	// DirStringBytes is the byte data of the interned dir paths;
	// DirLowerExtraBytes counts the lowered copies ONLY where
	// lowercasing changed the path (strings.ToLower returns the
	// original string unchanged when nothing lowers, so equal content
	// means shared bytes); DirHeaderBytes is the 16-byte string
	// headers of both dir columns.
	DirStringBytes     int64
	DirLowerExtraBytes int64
	DirHeaderBytes     int64

	// DirIndexApproxBytes estimates the path->id map at 16 (string
	// header) + len(key) + 4 (value) + 48 (bucket overhead) per entry;
	// the key BYTES are shared with DirStringBytes, so this errs high.
	// ChildrenApproxBytes estimates the dir->children map at 24 (slice
	// header) + 4*cap + 48 (bucket overhead) per entry.
	DirIndexApproxBytes int64
	ChildrenApproxBytes int64

	// TotalBytes sums every byte field above.
	TotalBytes int64
}

// BytesPerEntry returns TotalBytes averaged over all entries (0 for an
// empty store).
func (f Footprint) BytesPerEntry() float64 {
	if f.Entries == 0 {
		return 0
	}
	return float64(f.TotalBytes) / float64(f.Entries)
}

// stringHeaderBytes and sliceHeaderBytes are the 64-bit sizes of Go's
// string and slice headers; mapEntryOverheadBytes is the estimated
// per-entry bucket overhead used by the map approximations.
const (
	stringHeaderBytes     = 16
	sliceHeaderBytes      = 24
	mapEntryOverheadBytes = 48
)

// Footprint computes the store's memory accounting. It walks the dir
// table and the children map, so it is O(dirs + entries) -- cheap next
// to a rebuild, but not free; diagnostics only. Callers holding no
// lock must go through Manager.Footprint.
func (s *Store) Footprint() Footprint {
	f := Footprint{
		Entries:        len(s.parent),
		LiveEntries:    s.live,
		Dirs:           len(s.dirs),
		NameLowerBytes: int64(len(s.nameLower)),
		NameOrigBytes:  int64(len(s.nameOrig)),
		OffsetBytes:    4 * int64(len(s.lowOff)+len(s.origOff)),
		ParentBytes:    4 * int64(len(s.parent)),
		FlagBytes:      int64(len(s.flags)),
		DirHeaderBytes: stringHeaderBytes * int64(len(s.dirs)+len(s.dirsLower)),
	}
	for i, d := range s.dirs {
		f.DirStringBytes += int64(len(d))
		if s.dirsLower[i] != d {
			f.DirLowerExtraBytes += int64(len(s.dirsLower[i]))
		}
	}
	f.DirIndexApproxBytes = int64(len(s.dirIndex))*(stringHeaderBytes+4+mapEntryOverheadBytes) + f.DirStringBytes
	for _, ids := range s.children {
		f.ChildrenApproxBytes += sliceHeaderBytes + 4*int64(cap(ids)) + mapEntryOverheadBytes
	}
	f.TotalBytes = f.NameLowerBytes + f.NameOrigBytes + f.OffsetBytes +
		f.ParentBytes + f.FlagBytes + f.DirStringBytes +
		f.DirLowerExtraBytes + f.DirHeaderBytes +
		f.DirIndexApproxBytes + f.ChildrenApproxBytes
	return f
}
