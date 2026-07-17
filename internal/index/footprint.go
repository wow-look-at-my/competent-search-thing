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

	// NameBytes is the single original-case name blob
	// (0x00-separated); OffsetBytes its uint32 offset table;
	// ParentBytes the entry->dir column; FlagBytes the per-entry flag
	// column. Case-insensitivity is folded in at scan time (fold.go),
	// so there are no lowercased twin columns to account for.
	NameBytes   int64
	OffsetBytes int64
	ParentBytes int64
	FlagBytes   int64

	// DirStringBytes is the byte data of the interned dir paths;
	// DirHeaderBytes the 16-byte string headers of the dir column.
	DirStringBytes int64
	DirHeaderBytes int64

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
		NameBytes:      int64(len(s.names)),
		OffsetBytes:    4 * int64(len(s.nameOff)),
		ParentBytes:    4 * int64(len(s.parent)),
		FlagBytes:      int64(len(s.flags)),
		DirHeaderBytes: stringHeaderBytes * int64(len(s.dirs)),
	}
	for _, d := range s.dirs {
		f.DirStringBytes += int64(len(d))
	}
	f.DirIndexApproxBytes = int64(len(s.dirIndex))*(stringHeaderBytes+4+mapEntryOverheadBytes) + f.DirStringBytes
	for _, ids := range s.children {
		f.ChildrenApproxBytes += sliceHeaderBytes + 4*int64(cap(ids)) + mapEntryOverheadBytes
	}
	f.TotalBytes = f.NameBytes + f.OffsetBytes +
		f.ParentBytes + f.FlagBytes + f.DirStringBytes +
		f.DirHeaderBytes +
		f.DirIndexApproxBytes + f.ChildrenApproxBytes
	return f
}
