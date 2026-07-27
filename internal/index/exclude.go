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
//
// Patterns are split once more, by whether they contain any
// filepath.Match metacharacter. A metacharacter-free pattern matches
// exactly the strings equal to it, so those go into a set and cost one
// map lookup for the whole group instead of one glob-matcher call
// each. That matters because the shipped default excludes are ~13
// base-name and ~6 full-path patterns, ALL literal, and every walked
// entry pays the base check while every file entry pays the full one:
// a whole-filesystem build ran filepath.Match tens of millions of
// times to answer "no".
type Excluder struct {
	baseLit  map[string]struct{} // literal base-name patterns
	fullLit  map[string]struct{} // literal full-path patterns
	baseGlob []string            // base-name patterns with metacharacters
	fullGlob []string            // full-path patterns with metacharacters
}

// patternMeta lists the bytes that make filepath.Match do more than
// compare for equality. '\\' is an escape character on every platform
// except Windows, where it is the separator instead; treating it as a
// metacharacter everywhere just routes those patterns to the glob path,
// which is always correct.
const patternMeta = `*?[\`

// isLiteralPattern reports whether p matches exactly one string --
// itself. filepath.Match walks a metacharacter-free pattern byte for
// byte and requires both sides to end together, which is equality.
func isLiteralPattern(p string) bool {
	return !strings.ContainsAny(p, patternMeta)
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
		full := strings.ContainsRune(p, '/') || strings.ContainsRune(p, filepath.Separator)
		switch {
		case !isLiteralPattern(p) && full:
			e.fullGlob = append(e.fullGlob, p)
		case !isLiteralPattern(p):
			e.baseGlob = append(e.baseGlob, p)
		case full:
			if e.fullLit == nil {
				e.fullLit = make(map[string]struct{})
			}
			e.fullLit[p] = struct{}{}
		default:
			if e.baseLit == nil {
				e.baseLit = make(map[string]struct{})
			}
			e.baseLit[p] = struct{}{}
		}
	}
	return e, nil
}

// addFullLiterals adds trusted exact full paths without interpreting
// filepath.Match metacharacters. Mount-derived excludes use this path:
// '*' and '[' are legal filename bytes on Unix, and turning a mountpoint
// into a glob could either miss that mount or prune an unrelated path.
func (e *Excluder) addFullLiterals(paths []string) {
	if len(paths) == 0 {
		return
	}
	if e.fullLit == nil {
		e.fullLit = make(map[string]struct{}, len(paths))
	}
	for _, path := range paths {
		if path != "" {
			e.fullLit[path] = struct{}{}
		}
	}
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
	if _, ok := e.baseLit[base]; ok {
		return true
	}
	for _, p := range e.baseGlob {
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
	if _, ok := e.fullLit[full]; ok {
		return true
	}
	for _, p := range e.fullGlob {
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
	return e != nil && (len(e.fullLit) > 0 || len(e.fullGlob) > 0)
}
