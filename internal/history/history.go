// Package history keeps the searchbar's recall list: the queries
// whose activation actually ran something (a file opened or revealed,
// a plugin action executed), replayed by the frontend when Up is
// pressed at a blank bar. The list is ordered oldest to newest, an
// exact repeat moves to the newest slot instead of duplicating, and
// the size is capped (oldest dropped). A Store optionally persists
// the list as a plain JSON array of strings.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// maxEntries caps the history; adding past the cap drops the oldest
// entries.
const maxEntries = 100

// Store holds the query history. All methods are safe for concurrent
// use: the Wails-bound wrappers in internal/app run on arbitrary
// goroutines.
type Store struct {
	mu      sync.Mutex
	path    string
	persist bool
	entries []string // oldest -> newest
}

// New creates a store backed by the JSON file at path. persist false
// keeps the history in memory only: Load and Add never touch the
// disk, so a pre-existing file is neither read nor rewritten.
func New(path string, persist bool) *Store {
	return &Store{path: path, persist: persist}
}

// Load replaces the in-memory list with the persisted one. A missing
// file (or a memory-only store) is an empty history and no error; an
// unreadable or non-JSON-string-array file starts empty and returns
// the reason once, for logging -- the store stays fully usable either
// way. Loaded entries get the same invariants Add enforces (trimmed,
// no blanks, deduped keeping the newest occurrence, capped), because a
// hand-edited file may hold anything.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	if !s.persist {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	var raw []string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s is not a JSON array of strings: %v", s.path, err)
	}
	s.entries = normalize(raw)
	return nil
}

// normalize applies the Add invariants to a loaded list: entries are
// trimmed, blanks dropped, exact duplicates keep only their newest
// occurrence, and the result is capped to the newest maxEntries.
func normalize(raw []string) []string {
	kept := make([]string, 0, len(raw)) // newest -> oldest while filtering
	seen := make(map[string]struct{}, len(raw))
	for i := len(raw) - 1; i >= 0 && len(kept) < maxEntries; i-- {
		e := strings.TrimSpace(raw[i])
		if e == "" {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		kept = append(kept, e)
	}
	out := make([]string, len(kept))
	for i, e := range kept {
		out[len(kept)-1-i] = e // reverse back into oldest -> newest
	}
	return out
}

// Add records entry as the newest history item: it is trimmed (blank
// results are skipped silently), an exact match already in the list
// moves to the newest position instead of duplicating, and the oldest
// entry falls off past the cap. A persisting store then rewrites the
// file; the in-memory list is updated even when that write fails, so
// in-session recall survives disk problems.
func (s *Store) Add(entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries)+1)
	for _, e := range s.entries {
		if e != entry {
			out = append(out, e)
		}
	}
	out = append(out, entry)
	if len(out) > maxEntries {
		out = out[len(out)-maxEntries:]
	}
	s.entries = out
	if !s.persist {
		return nil
	}
	return s.save()
}

// save writes the list as an indented JSON array, atomically: into a
// temp file in the target directory (chmodded 0600 -- queries are the
// user's business), then renamed over the destination, so a crash
// mid-write never leaves a truncated file behind. The parent
// directory is created first, like config.Save does.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding history: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", s.path, err)
	}
	return nil
}

// Entries returns the history, oldest to newest, as a defensive copy
// (never nil).
func (s *Store) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.entries))
	copy(out, s.entries)
	return out
}
