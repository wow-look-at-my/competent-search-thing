package index

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Excluder decides which walked entries to skip. Two pattern kinds:
//
//   - A pattern WITHOUT a path separator is matched with filepath.Match
//     against each entry's base name ("node_modules", ".git", "*.tmp").
//     A matching directory is pruned (never descended); a matching file
//     is skipped.
//   - A pattern WITH a separator is matched with filepath.Match against
//     the entry's full absolute path ("/home/*/secret"). filepath.Match
//     semantics apply: '*' does not cross separators and there is no
//     '**'.
//
// The zero/nil Excluder matches nothing. Excluder is immutable after
// construction and safe for concurrent use; the watcher phase reuses it
// to filter fsnotify events with identical semantics.
type Excluder struct {
	base []string // matched against entry base names
	full []string // matched against full absolute paths
}

// NewExcluder validates the patterns and splits them by kind. Empty
// patterns are ignored; a malformed pattern (filepath.ErrBadPattern)
// is reported up front instead of silently never matching.
func NewExcluder(patterns []string) (*Excluder, error) {
	e := &Excluder{}
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if _, err := filepath.Match(p, "probe"); err != nil {
			return nil, fmt.Errorf("index: bad exclude pattern %q: %w", p, err)
		}
		if strings.ContainsRune(p, '/') || strings.ContainsRune(p, filepath.Separator) {
			e.full = append(e.full, p)
		} else {
			e.base = append(e.base, p)
		}
	}
	return e, nil
}

// Match reports whether an entry with the given base name and full
// absolute path is excluded. Exactly MatchBase || MatchFull; the walk
// hot path calls the halves separately so it can skip materializing
// the full path when HasFullPatterns is false.
func (e *Excluder) Match(base, full string) bool {
	return e.MatchBase(base) || e.MatchFull(full)
}

// MatchBase reports whether the base name alone is excluded by a
// base-name pattern.
func (e *Excluder) MatchBase(base string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.base {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

// MatchFull reports whether the full absolute path is excluded by a
// full-path pattern.
func (e *Excluder) MatchFull(full string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.full {
		if ok, _ := filepath.Match(p, full); ok {
			return true
		}
	}
	return false
}

// HasFullPatterns reports whether any full-path patterns exist.
// Callers that would have to BUILD the full path just to check it
// (the walker, for every file entry) skip that work entirely when
// there are none.
func (e *Excluder) HasFullPatterns() bool {
	return e != nil && len(e.full) > 0
}
