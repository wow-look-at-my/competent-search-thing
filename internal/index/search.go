package index

import (
	"bytes"
	"runtime"
	"sort"
	"sync"
)

// Match classes, in ranking order.
const (
	classExact  uint8 = iota // query equals the whole name
	classPrefix              // name starts with the query
	classSub                 // query occurs elsewhere in the name
	classFuzzy               // query is a subsequence (but not a substring) of the name
)

// minShardEntries keeps tiny stores on the single-threaded path where
// goroutine fan-out would cost more than it saves.
const minShardEntries = 4096

// QueryOptions tunes one query.
type QueryOptions struct {
	// FuzzyDisabled turns the fuzzy (subsequence) tier off for
	// name-mode queries; the query then behaves exactly like the
	// substring-only engine. The zero value keeps fuzzy matching on.
	FuzzyDisabled bool
}

// Query returns up to limit entries whose name matches q,
// case-insensitively, best matches first. Ranking: exact name matches,
// then name-prefix matches, then other substring matches, then fuzzy
// (subsequence) matches; within a class directories sort before files,
// then shorter full paths, then lexicographic path order (the fuzzy
// class ranks by alignment score first -- see fuzzy.go). An empty
// query, an empty store, or a non-positive limit returns nil.
//
// A query containing a path separator switches to path mode and is
// matched against the full path instead of the name (see path.go); the
// name scans below are untouched by that dispatch and path mode has no
// fuzzy tier.
//
// The scan is sharded across NumCPU contiguous entry ranges. The store
// keeps names in original case only, so matching folds case at scan
// time (fold.go): an all-ASCII query scans the contiguous name blob
// with the ciIndexASCII anchor search and a per-worker bounded
// top-limit heap, so a keystroke over a million entries never sorts a
// full match list; a query with non-ASCII runes takes the per-entry
// rune-folding slow path with the same sharding and ranking.
func (s *Store) Query(q string, limit int) []Result {
	return s.QueryWith(q, limit, QueryOptions{})
}

// QueryWith is Query with per-query options.
func (s *Store) QueryWith(q string, limit int, opts QueryOptions) []Result {
	n := s.Len()
	if q == "" || limit <= 0 || n == 0 {
		return nil
	}
	pat, ascii := foldPattern(q)
	if bytes.IndexByte(pat, nameSep) >= 0 {
		// No name can contain NUL; a NUL in the pattern could only
		// false-match across the blob separator.
		return nil
	}
	if hasPathSep(pat) {
		return s.queryPath(pat, ascii, limit)
	}
	qs := string(pat)
	if opts.FuzzyDisabled {
		return s.queryNamesSub(qs, ascii, n, limit)
	}
	return s.queryNamesFuzzy(qs, ascii, n, limit)
}

// queryNamesSub is the substring-only name scan: the pre-fuzzy engine,
// dispatched when the fuzzy tier is disabled. It must stay
// behavior-identical to the original Query body.
func (s *Store) queryNamesSub(qs string, ascii bool, n, limit int) []Result {
	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	heaps := make([]*topK, workers)
	if workers == 1 {
		heaps[0] = newTopK(s, limit)
		s.scanNames(qs, ascii, 0, n, heaps[0], nil)
	} else {
		per := (n + workers - 1) / workers
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo := w * per
			hi := min(lo+per, n)
			if lo >= hi {
				heaps[w] = newTopK(s, limit)
				continue
			}
			wg.Add(1)
			go func(w, lo, hi int) {
				defer wg.Done()
				h := newTopK(s, limit)
				s.scanNames(qs, ascii, lo, hi, h, nil)
				heaps[w] = h
			}(w, lo, hi)
		}
		wg.Wait()
	}

	return s.selectTop(heaps, limit)
}

// scanNames scans one shard with the folding regime the pattern was
// prepared for. When marks is non-nil every live match's entry bit is
// set in it (the phase-1 side channel of the fuzzy two-phase scan; see
// fuzzy.go). Returns the number of live matches found in the shard.
func (s *Store) scanNames(pat string, ascii bool, lo, hi int, h *topK, marks []uint64) int {
	if ascii {
		return s.scanRange(pat, lo, hi, h, marks)
	}
	return s.scanRangeFold(pat, lo, hi, h, marks)
}

// scanRange scans entries [lo, hi) for the pre-folded ASCII pattern and
// feeds live matches into h. The shard boundaries fall on name
// boundaries by construction, and because neither names nor pat contain
// the 0x00 separator (the separator never fold-equals a pattern byte),
// a match can never span two names. After a hit the scan skips to the
// end of the matched name, so each entry is reported at most once, at
// the first occurrence of pat inside its name.
func (s *Store) scanRange(pat string, lo, hi int, h *topK, marks []uint64) int {
	base := s.nameOff[lo]
	blob := s.names[base:s.nameOff[hi]]
	sc := newCiScan(blob, pat)
	off := 0
	cur := lo
	count := 0
	for {
		rel := sc.next(off)
		if rel < 0 {
			return count
		}
		pos := base + uint32(rel)
		// Map the hit position to its entry: the unique e in [cur, hi)
		// with nameOff[e] <= pos < nameOff[e+1].
		e := cur + sort.Search(hi-cur, func(k int) bool { return s.nameOff[cur+k+1] > pos })
		if s.flags[e]&flagTombstone == 0 {
			h.add(s.makeCand(int32(e), pos, len(pat)))
			count++
			if marks != nil {
				markEntry(marks, e)
			}
		}
		next := e + 1
		if next >= hi {
			return count
		}
		off = int(s.nameOff[next] - base)
		cur = next
	}
}

// scanRangeFold is the non-ASCII counterpart of scanRange: a per-entry
// rune-folding classification (see fold.go). O(entries * name * pat) --
// the documented slow path for queries carrying non-ASCII runes.
func (s *Store) scanRangeFold(pat string, lo, hi int, h *topK, marks []uint64) int {
	count := 0
	for e := lo; e < hi; e++ {
		if s.flags[e]&flagTombstone != 0 {
			continue
		}
		nb := s.nameBytes(int32(e))
		var class uint8
		if pl := foldPrefixLen(nb, pat); pl >= 0 {
			class = classPrefix
			if pl == len(nb) {
				class = classExact
			}
		} else if foldContains(nb, pat) {
			class = classSub
		} else {
			continue
		}
		pathLen := joinedLen(s.dirs[s.parent[e]], len(nb))
		h.add(cand{id: int32(e), pathLen: int32(pathLen), class: class, isDir: s.flags[e]&flagDir != 0})
		count++
		if marks != nil {
			markEntry(marks, e)
		}
	}
	return count
}

// makeCand builds the ranking candidate for entry e whose first match
// (of a patLen-byte pre-folded ASCII pattern) starts at absolute blob
// position pos. It allocates nothing: the path length is derived from
// the offset table. ASCII folding preserves byte counts, so the length
// compare against patLen is exact.
func (s *Store) makeCand(e int32, pos uint32, patLen int) cand {
	start := s.nameOff[e]
	class := classSub
	if pos == start {
		class = classPrefix
		if int(s.nameOff[e+1]-1-start) == patLen {
			class = classExact
		}
	}
	dir := s.dirs[s.parent[e]]
	pathLen := joinedLen(dir, int(s.nameOff[e+1]-1-s.nameOff[e]))
	return cand{id: e, pathLen: int32(pathLen), class: class, isDir: s.flags[e]&flagDir != 0}
}

// selectTop merges the per-shard heaps, fully sorts the small merged
// candidate set (at most workers*limit items), and builds Results for
// the best limit entries.
func (s *Store) selectTop(heaps []*topK, limit int) []Result {
	var all []cand
	for _, h := range heaps {
		all = append(all, h.items...)
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return s.candCompare(all[i], all[j]) < 0 })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]Result, len(all))
	for i, c := range all {
		out[i] = Result{Path: s.EntryPath(c.id), Name: s.Name(c.id), IsDir: c.isDir}
	}
	return out
}
