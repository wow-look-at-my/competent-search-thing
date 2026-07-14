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
// absolute path is excluded.
func (e *Excluder) Match(base, full string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.base {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	for _, p := range e.full {
		if ok, _ := filepath.Match(p, full); ok {
			return true
		}
	}
	return false
}
