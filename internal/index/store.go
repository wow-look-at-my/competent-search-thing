// Package index implements the in-memory file-name index: a compact
// column-oriented store, a parallel directory walker that fills it, a
// parallel case-insensitive substring search over it, and a Manager
// that adds the locking contract on top.
//
// Concurrency contract: a Store by itself is NOT safe for concurrent
// use. The Manager owns a sync.RWMutex; queries run under RLock and
// mutations under Lock. Walk serializes its own writes internally, but
// it must target a store that nothing else is reading or writing (the
// Manager walks into a fresh private store and swaps it in when done).
package index

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Entry flag bits stored in Store.flags.
const (
	flagDir       byte = 1 << 0 // entry is a directory
	flagTombstone byte = 1 << 1 // entry has been removed
)

// nameSep separates entry names inside the names blob. File names can
// never contain a NUL byte, so a match can never span two names.
const nameSep byte = 0x00

// Store is the compact in-memory index. Entry data is laid out in flat
// parallel columns instead of per-entry structs/strings so that search
// can scan one contiguous byte blob:
//
//   - dirs/dirIndex intern every parent-directory path once.
//   - names holds all entry names in their ORIGINAL case, concatenated
//     with a 0x00 separator after each name; nameOff (len n+1) gives
//     each name's start, and nameOff[i+1]-1 is its end. There is
//     deliberately no lowercased twin of this blob (or of the dir
//     table): case-insensitivity is folded in at scan time (fold.go),
//     halving the name storage.
//   - parent maps entry -> dir id; flags holds per-entry bits.
//   - children maps dir id -> entry ids, supporting point deletes and
//     dedup by (parent, name).
//
// Removals only set tombstone bits; space is reclaimed by rebuilding
// into a fresh Store (Manager.BuildFromDisk).
type Store struct {
	dirs     []string          // dir id -> absolute, clean path (original case)
	dirIndex map[string]uint32 // absolute path -> dir id

	names   []byte   // original-case names, 0x00-separated
	nameOff []uint32 // len n+1; name i is names[nameOff[i]:nameOff[i+1]-1]

	parent   []uint32           // entry -> dir id
	flags    []byte             // entry -> flag bits
	children map[uint32][]int32 // dir id -> entry ids (live and tombstoned)

	live int // number of non-tombstoned entries

	// byteFreq counts every byte in the names blob (updated on append,
	// never decremented -- tombstoned names stay in the blob). The
	// fuzzy phase-2 sweep uses it to anchor on the pattern byte with
	// the fewest actual blob occurrences (fuzzy.go). A fixed 2 KiB,
	// deliberately not part of Footprint.
	byteFreq [256]uint64
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		dirIndex: make(map[string]uint32),
		nameOff:  []uint32{0},
		children: make(map[uint32][]int32),
	}
}

// internDir returns the dir id for path, creating it on demand. The
// path must already be absolute and clean.
func (s *Store) internDir(path string) uint32 {
	if id, ok := s.dirIndex[path]; ok {
		return id
	}
	id := uint32(len(s.dirs))
	s.dirs = append(s.dirs, path)
	s.dirIndex[path] = id
	return id
}

// validateName rejects names that would corrupt the blob layout or
// path reconstruction.
func validateName(name string) error {
	switch {
	case name == "":
		return errors.New("index: empty entry name")
	case name == "." || name == "..":
		return fmt.Errorf("index: invalid entry name %q", name)
	case strings.IndexByte(name, nameSep) >= 0:
		return errors.New("index: entry name contains NUL byte")
	case strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator):
		return fmt.Errorf("index: entry name %q contains a path separator", name)
	}
	return nil
}

// AddEntry adds one entry under parentDir (an absolute path), creating
// the dir id on demand. Adding a directory also registers its own path
// in the dir table so children can be added later. If a (parentDir,
// name) entry already exists it is updated in place: a tombstoned entry
// is resurrected and the directory bit is refreshed. Returns the entry
// id.
func (s *Store) AddEntry(parentDir, name string, isDir bool) (int32, error) {
	if err := validateName(name); err != nil {
		return -1, err
	}
	if !filepath.IsAbs(parentDir) {
		return -1, fmt.Errorf("index: parent dir %q is not absolute", parentDir)
	}
	parentDir = filepath.Clean(parentDir)
	pid := s.internDir(parentDir)
	if id := s.findChild(pid, name); id >= 0 {
		if s.flags[id]&flagTombstone != 0 {
			s.flags[id] &^= flagTombstone
			s.live++
		}
		if isDir {
			s.flags[id] |= flagDir
			s.internDir(joinDir(parentDir, name))
		} else {
			s.flags[id] &^= flagDir
		}
		return id, nil
	}
	var dirPath string
	if isDir {
		dirPath = joinDir(parentDir, name)
	}
	return s.appendEntry(pid, name, dirPath, isDir), nil
}

// appendEntry appends a brand-new entry without checking for an
// existing (parent, name) duplicate. The walker uses it directly when
// filling a fresh store, where os.ReadDir guarantees unique names per
// directory; everyone else goes through AddEntry. dirPath is the
// entry's OWN absolute path when isDir -- callers pass the string
// they already built (the walker's queue copy), so the join is never
// performed twice -- and is ignored for files.
func (s *Store) appendEntry(pid uint32, name, dirPath string, isDir bool) int32 {
	id := int32(len(s.parent))
	s.names, s.nameOff = appendName(s.names, s.nameOff, name)
	for i := 0; i < len(name); i++ {
		s.byteFreq[name[i]]++
	}
	s.parent = append(s.parent, pid)
	var f byte
	if isDir {
		f = flagDir
		s.internDir(dirPath)
	}
	s.flags = append(s.flags, f)
	s.children[pid] = append(s.children[pid], id)
	s.live++
	return id
}

// growChildren ensures dir id pid's children slice has capacity for n
// more appends. The walker calls it once per directory with the exact
// batch size before its appendEntry loop, so walk-built children
// slices end at cap == len instead of accumulating the 1->2->4 append
// ladder's ~1.32x measured overshoot (plus its copy churn).
func (s *Store) growChildren(pid uint32, n int) {
	kids := s.children[pid]
	if n <= 0 || cap(kids)-len(kids) >= n {
		return
	}
	grown := make([]int32, len(kids), len(kids)+n)
	copy(grown, kids)
	s.children[pid] = grown
}

// appendName appends one name plus the separator byte to a blob and
// records the new end offset.
func appendName(blob []byte, offs []uint32, name string) ([]byte, []uint32) {
	blob = append(blob, name...)
	blob = append(blob, nameSep)
	return blob, append(offs, uint32(len(blob)))
}

// findChild returns the entry id (live or tombstoned) named name under
// dir id pid, or -1. Matching is on the original name, byte-exact.
func (s *Store) findChild(pid uint32, name string) int32 {
	for _, id := range s.children[pid] {
		if string(s.nameBytes(id)) == name {
			return id
		}
	}
	return -1
}

// RemoveByPath tombstones the entry at path. If path is a directory
// known to the store, its whole subtree is tombstoned too: every
// interned dir at or below path has all of its child entries marked,
// which covers nested files and the nested directory entries
// themselves. Returns the number of entries newly tombstoned (0 if
// nothing matched).
func (s *Store) RemoveByPath(path string) int {
	path = filepath.Clean(path)
	removed := 0
	// The entry itself (file, or the dir's own entry in its parent).
	if pid, ok := s.dirIndex[filepath.Dir(path)]; ok {
		if id := s.findChild(pid, filepath.Base(path)); id >= 0 {
			removed += s.tombstone(id)
		}
	}
	// The subtree, if path is an interned directory.
	if _, ok := s.dirIndex[path]; ok {
		for did, dirPath := range s.dirs {
			if !isWithin(dirPath, path) {
				continue
			}
			for _, id := range s.children[uint32(did)] {
				removed += s.tombstone(id)
			}
		}
	}
	return removed
}

// tombstone marks entry id removed; returns 1 if it was live.
func (s *Store) tombstone(id int32) int {
	if s.flags[id]&flagTombstone != 0 {
		return 0
	}
	s.flags[id] |= flagTombstone
	s.live--
	return 1
}

// Len returns the total number of entries including tombstones.
func (s *Store) Len() int { return len(s.parent) }

// LiveCount returns the number of live (non-tombstoned) entries.
func (s *Store) LiveCount() int { return s.live }

// IsDir reports whether entry id is a directory.
func (s *Store) IsDir(id int32) bool { return s.flags[id]&flagDir != 0 }

// Name returns entry id's original-case name.
func (s *Store) Name(id int32) string { return string(s.nameBytes(id)) }

// ParentDir returns the absolute path of entry id's parent directory.
func (s *Store) ParentDir(id int32) string { return s.dirs[s.parent[id]] }

// nameBytes returns entry id's original-case name as a subslice of the
// blob (no copy; callers must not modify it).
func (s *Store) nameBytes(id int32) []byte {
	return s.names[s.nameOff[id] : s.nameOff[id+1]-1]
}

// EntryPath reconstructs entry id's full absolute path.
func (s *Store) EntryPath(id int32) string {
	return joinDir(s.dirs[s.parent[id]], s.Name(id))
}

// ForEachLive calls fn for every live entry id in insertion order until
// fn returns false. Used for rebuild/compaction and diagnostics.
func (s *Store) ForEachLive(fn func(id int32) bool) {
	for i := range s.parent {
		id := int32(i)
		if s.flags[id]&flagTombstone != 0 {
			continue
		}
		if !fn(id) {
			return
		}
	}
}

// DefaultLiveDirsPage is the page size LiveDirsPage uses when the
// caller passes a non-positive max.
const DefaultLiveDirsPage = 4096

// LiveDirsPage visits entry ids in [start, Len()) in id order,
// collecting the absolute path of every live directory entry, and
// stops once max paths are collected (max <= 0 selects
// DefaultLiveDirsPage) or the id space ends. next is the id after the
// last id examined, or -1 when every id through Len()-1 has been
// examined (the id space is exhausted). A negative start is treated
// as 0. Entry ids are stable across mutations -- removals only set
// tombstone bits and resurrection reuses the id -- so pages taken
// across separate calls never skip or repeat an existing entry; the
// watch layer's sweeper uses that to enumerate directories chunk by
// chunk instead of holding the Manager's read lock across a full
// ForEachLive scan.
func (s *Store) LiveDirsPage(start int32, max int) (dirs []string, next int32) {
	if max <= 0 {
		max = DefaultLiveDirsPage
	}
	if start < 0 {
		start = 0
	}
	n := int32(s.Len())
	id := start
	for ; id < n; id++ {
		if s.flags[id]&flagTombstone != 0 || !s.IsDir(id) {
			continue
		}
		dirs = append(dirs, s.EntryPath(id))
		if len(dirs) == max {
			id++
			break
		}
	}
	if id >= n {
		return dirs, -1
	}
	return dirs, id
}

// ChildInfo describes one direct child of a directory: its name in
// the original case and whether the child is itself a directory.
type ChildInfo struct {
	Name  string
	IsDir bool
}

// ChildrenOf returns the live direct children of dir (an absolute
// path, cleaned like RemoveByPath's argument), or nil when dir is not
// an interned directory or has no live children. Order is
// unspecified. The watch layer's reconcile pass diffs the result
// against a fresh readdir of the same directory.
func (s *Store) ChildrenOf(dir string) []ChildInfo {
	pid, ok := s.dirIndex[filepath.Clean(dir)]
	if !ok {
		return nil
	}
	var out []ChildInfo
	for _, id := range s.children[pid] {
		if s.flags[id]&flagTombstone != 0 {
			continue
		}
		out = append(out, ChildInfo{Name: s.Name(id), IsDir: s.IsDir(id)})
	}
	return out
}

// joinDir joins a clean absolute directory path and a child name. Only
// a filesystem root ("/", or a drive root on Windows) keeps a trailing
// separator after filepath.Clean, so the separator is inserted unless
// already present.
func joinDir(dir, name string) string {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return dir + name
	}
	return dir + string(filepath.Separator) + name
}

// isWithin reports whether path equals dir or lies underneath it. Both
// must be clean absolute paths.
func isWithin(path, dir string) bool {
	if path == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(path, dir)
}
