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
)

// minShardEntries keeps tiny stores on the single-threaded path where
// goroutine fan-out would cost more than it saves.
const minShardEntries = 4096

// Query returns up to limit entries whose name contains q,
// case-insensitively, best matches first. Ranking: exact name matches,
// then name-prefix matches, then other substring matches; within a
// class directories sort before files, then shorter full paths, then
// lexicographic path order. An empty query, an empty store, or a
// non-positive limit returns nil.
//
// A query containing a path separator switches to path mode and is
// matched against the full path instead of the name (see path.go); the
// name-only scan below is untouched by that dispatch.
//
// The scan is sharded across NumCPU contiguous entry ranges. The store
// keeps names in original case only, so matching folds case at scan
// time (fold.go): an all-ASCII query scans the contiguous name blob
// with the ciIndexASCII anchor search and a per-worker bounded
// top-limit heap, so a keystroke over a million entries never sorts a
// full match list; a query with non-ASCII runes takes the per-entry
// rune-folding slow path with the same sharding and ranking.
func (s *Store) Query(q string, limit int) []Result {
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

	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	heaps := make([]*topK, workers)
	if workers == 1 {
		heaps[0] = newTopK(s, limit)
		s.scanNames(qs, ascii, 0, n, heaps[0])
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
				s.scanNames(qs, ascii, lo, hi, h)
				heaps[w] = h
			}(w, lo, hi)
		}
		wg.Wait()
	}

	return s.selectTop(heaps, limit)
}

// scanNames scans one shard with the folding regime the pattern was
// prepared for.
func (s *Store) scanNames(pat string, ascii bool, lo, hi int, h *topK) {
	if ascii {
		s.scanRange(pat, lo, hi, h)
		return
	}
	s.scanRangeFold(pat, lo, hi, h)
}

// scanRange scans entries [lo, hi) for the pre-folded ASCII pattern and
// feeds live matches into h. The shard boundaries fall on name
// boundaries by construction, and because neither names nor pat contain
// the 0x00 separator (the separator never fold-equals a pattern byte),
// a match can never span two names. After a hit the scan skips to the
// end of the matched name, so each entry is reported at most once, at
// the first occurrence of pat inside its name.
func (s *Store) scanRange(pat string, lo, hi int, h *topK) {
	base := s.nameOff[lo]
	blob := s.names[base:s.nameOff[hi]]
	sc := newCiScan(blob, pat)
	off := 0
	cur := lo
	for {
		rel := sc.next(off)
		if rel < 0 {
			return
		}
		pos := base + uint32(rel)
		// Map the hit position to its entry: the unique e in [cur, hi)
		// with nameOff[e] <= pos < nameOff[e+1].
		e := cur + sort.Search(hi-cur, func(k int) bool { return s.nameOff[cur+k+1] > pos })
		if s.flags[e]&flagTombstone == 0 {
			h.add(s.makeCand(int32(e), pos, len(pat)))
		}
		next := e + 1
		if next >= hi {
			return
		}
		off = int(s.nameOff[next] - base)
		cur = next
	}
}

// scanRangeFold is the non-ASCII counterpart of scanRange: a per-entry
// rune-folding classification (see fold.go). O(entries * name * pat) --
// the documented slow path for queries carrying non-ASCII runes.
func (s *Store) scanRangeFold(pat string, lo, hi int, h *topK) {
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
	}
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
