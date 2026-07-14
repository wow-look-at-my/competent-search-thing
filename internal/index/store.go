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

// nameSep separates entry names inside the nameLower/nameOrig blobs.
// File names can never contain a NUL byte, so a match can never span
// two names.
const nameSep byte = 0x00

// Store is the compact in-memory index. Entry data is laid out in flat
// parallel columns instead of per-entry structs/strings so that search
// can scan one contiguous byte blob:
//
//   - dirs/dirIndex intern every parent-directory path once.
//   - nameLower holds all entry names lowercased (unicode-aware),
//     concatenated with a 0x00 separator after each name; lowOff (len
//     n+1) gives each name's start, and lowOff[i+1]-1 is its end.
//   - nameOrig/origOff hold the original-case names with their own
//     offsets (unicode lowercasing can change byte length).
//   - parent maps entry -> dir id; flags holds per-entry bits.
//   - children maps dir id -> entry ids, supporting point deletes and
//     dedup by (parent, name).
//
// Removals only set tombstone bits; space is reclaimed by rebuilding
// into a fresh Store (Manager.BuildFromDisk).
type Store struct {
	dirs     []string          // dir id -> absolute, clean path
	dirIndex map[string]uint32 // absolute path -> dir id

	nameLower []byte   // lowercased names, 0x00-separated
	lowOff    []uint32 // len n+1; name i is nameLower[lowOff[i]:lowOff[i+1]-1]
	nameOrig  []byte   // original-case names, 0x00-separated
	origOff   []uint32 // len n+1, same shape as lowOff

	parent   []uint32           // entry -> dir id
	flags    []byte             // entry -> flag bits
	children map[uint32][]int32 // dir id -> entry ids (live and tombstoned)

	live int // number of non-tombstoned entries
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		dirIndex: make(map[string]uint32),
		lowOff:   []uint32{0},
		origOff:  []uint32{0},
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
	return s.appendEntry(pid, parentDir, name, isDir), nil
}

// appendEntry appends a brand-new entry without checking for an
// existing (parent, name) duplicate. The walker uses it directly when
// filling a fresh store, where os.ReadDir guarantees unique names per
// directory; everyone else goes through AddEntry.
func (s *Store) appendEntry(pid uint32, parentDir, name string, isDir bool) int32 {
	id := int32(len(s.parent))
	s.nameLower, s.lowOff = appendName(s.nameLower, s.lowOff, strings.ToLower(name))
	s.nameOrig, s.origOff = appendName(s.nameOrig, s.origOff, name)
	s.parent = append(s.parent, pid)
	var f byte
	if isDir {
		f = flagDir
		s.internDir(joinDir(parentDir, name))
	}
	s.flags = append(s.flags, f)
	s.children[pid] = append(s.children[pid], id)
	s.live++
	return id
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
		if string(s.origNameBytes(id)) == name {
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
func (s *Store) Name(id int32) string { return string(s.origNameBytes(id)) }

// ParentDir returns the absolute path of entry id's parent directory.
func (s *Store) ParentDir(id int32) string { return s.dirs[s.parent[id]] }

// origNameBytes returns entry id's original name as a subslice of the
// blob (no copy; callers must not modify it).
func (s *Store) origNameBytes(id int32) []byte {
	return s.nameOrig[s.origOff[id] : s.origOff[id+1]-1]
}

// lowerNameBytes returns entry id's lowercased name as a subslice of
// the blob (no copy).
func (s *Store) lowerNameBytes(id int32) []byte {
	return s.nameLower[s.lowOff[id] : s.lowOff[id+1]-1]
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
