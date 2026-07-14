package index

import (
	"bytes"
	"runtime"
	"sort"
	"strings"
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
// The scan is sharded across NumCPU contiguous entry ranges; each
// worker scans its slice of the lowercased name blob with bytes.Index
// and keeps its own bounded top-limit heap, so a keystroke over a
// million entries never sorts a full match list.
func (s *Store) Query(q string, limit int) []Result {
	n := s.Len()
	if q == "" || limit <= 0 || n == 0 {
		return nil
	}
	pat := []byte(strings.ToLower(q))
	if bytes.IndexByte(pat, nameSep) >= 0 {
		// No name can contain NUL; a NUL in the pattern could only
		// false-match across the blob separator.
		return nil
	}

	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	heaps := make([]*topK, workers)
	if workers == 1 {
		heaps[0] = newTopK(s, limit)
		s.scanRange(pat, 0, n, heaps[0])
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
				s.scanRange(pat, lo, hi, h)
				heaps[w] = h
			}(w, lo, hi)
		}
		wg.Wait()
	}

	return s.selectTop(heaps, limit)
}

// scanRange scans entries [lo, hi) for pat and feeds live matches into
// h. The shard boundaries fall on name boundaries by construction, and
// because neither names nor pat contain the 0x00 separator, a match
// can never span two names. After a hit the scan skips to the end of
// the matched name, so each entry is reported at most once, at the
// first occurrence of pat inside its name.
func (s *Store) scanRange(pat []byte, lo, hi int, h *topK) {
	base := s.lowOff[lo]
	blob := s.nameLower[base:s.lowOff[hi]]
	off := 0
	cur := lo
	for {
		rel := bytes.Index(blob[off:], pat)
		if rel < 0 {
			return
		}
		pos := base + uint32(off+rel)
		// Map the hit position to its entry: the unique e in [cur, hi)
		// with lowOff[e] <= pos < lowOff[e+1].
		e := cur + sort.Search(hi-cur, func(k int) bool { return s.lowOff[cur+k+1] > pos })
		if s.flags[e]&flagTombstone == 0 {
			h.add(s.makeCand(int32(e), pos, len(pat)))
		}
		next := e + 1
		if next >= hi {
			return
		}
		off = int(s.lowOff[next] - base)
		cur = next
	}
}

// makeCand builds the ranking candidate for entry e whose first match
// (of a patLen-byte lowercased pattern) starts at absolute blob
// position pos. It allocates nothing: the path length is derived from
// the offset tables.
func (s *Store) makeCand(e int32, pos uint32, patLen int) cand {
	start := s.lowOff[e]
	class := classSub
	if pos == start {
		class = classPrefix
		if int(s.lowOff[e+1]-1-start) == patLen {
			class = classExact
		}
	}
	dir := s.dirs[s.parent[e]]
	pathLen := joinedLen(dir, int(s.origOff[e+1]-1-s.origOff[e]))
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
