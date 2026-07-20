package index

import (
	"bytes"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
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
	// Blend folds the frecency/recency/noise signals into the final
	// ordering of the merged top-K candidates (see blend.go). Nil --
	// or an inactive blend -- leaves the ranking byte-identical to
	// the pre-blend engine.
	Blend *Blend
	// Trace, when non-nil, receives one ResultSignals per returned
	// Result, in order -- the per-candidate ranking components the
	// selection stage computed (see signalstrace.go). Nil is zero
	// cost and byte-identical to today; non-nil never changes the
	// results or their order.
	Trace *[]ResultSignals
}

// Query returns up to limit entries whose name matches q,
// case-insensitively, best matches first. Ranking: exact name matches,
// then name-prefix matches, then other substring matches, then fuzzy
// (subsequence) matches; within a class directories sort before files,
// then shorter full paths, then numeric-aware lexicographic path
// order -- aligned digit runs compare numerically DESCENDING so
// datestamped/versioned families deliver newest first (numorder.go);
// the fuzzy
// class ranks by alignment score first -- see fuzzy.go. An empty
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
	// Resolve the pick-memory prior for this query ONCE, on a
	// per-query Blend copy, so selectBlended never needs the query
	// string threaded through the per-mode scan functions and the
	// caller's Blend stays immutable. A resolver returning nil (no
	// learned tables apply) leaves priorFn nil = no term.
	if pb := opts.Blend; pb != nil && pb.Prior != nil {
		cp := *pb
		cp.priorFn = pb.Prior(q)
		opts.Blend = &cp
	}
	// A requested signals trace rides a per-query blend copy so every
	// path below stays untouched (signalstrace.go); with Trace nil
	// this is exactly opts.Blend. traceBlend copies the whole Blend
	// (including the priorFn bound above), so the prior and trace
	// seams compose without either knowing of the other.
	b := traceBlend(opts)
	if hasPathSep(pat) {
		// Path mode stays literal single-pattern: paths legitimately
		// contain spaces, so a separator disables term splitting.
		// (queryPath normalizes '/' to the native separator IN pat.)
		res := s.queryPath(pat, ascii, limit, b)
		fillPathRanges(res, string(pat), ascii)
		return res
	}
	terms := match.Terms(q)
	var res []Result
	switch len(terms) {
	case 0:
		return nil // all-whitespace query
	case 1:
		// The single-term path IS the pre-multi engine, byte-identical
		// for queries without surrounding whitespace; a padded query
		// (" report ") behaves as its trimmed term.
		t := terms[0]
		if opts.FuzzyDisabled {
			res = s.queryNamesSub(t.Pat, t.ASCII, n, limit, b)
		} else {
			res = s.queryNamesFuzzy(t.Pat, t.ASCII, n, limit, b)
		}
	default:
		res = s.queryNamesMulti(terms, n, limit, opts.FuzzyDisabled, b)
	}
	fillNameRanges(res, terms, !opts.FuzzyDisabled)
	return res
}

// fillNameRanges computes the per-character highlight ranges on each
// returned row's Name via the shared engine -- for the (at most limit)
// selected rows only, never during the scan.
func fillNameRanges(res []Result, terms []match.Term, allowFuzzy bool) {
	for i := range res {
		res[i].MatchRanges = match.Positions(res[i].Name, terms, allowFuzzy)
	}
}

// fillPathRanges is the path-mode highlight: when the normalized
// folded query's final segment (after its last separator) fold-matches
// the start of a row's name, that name prefix lights up; a query
// ending in a separator, or a match lying entirely within the
// directory, highlights nothing. Best-effort display sugar, not a
// match-classification replay.
func fillPathRanges(res []Result, qs string, ascii bool) {
	cut := strings.LastIndexByte(qs, sepByte)
	rem := qs[cut+1:]
	if rem == "" {
		return
	}
	for i := range res {
		name := res[i].Name
		matched := -1
		if ascii {
			if ciHasPrefixASCII(name, rem) {
				matched = len(rem)
			}
		} else {
			matched = foldPrefixLen(name, rem)
		}
		if matched < 0 {
			continue
		}
		runes := utf8.RuneCountInString(name[:matched])
		res[i].MatchRanges = [][2]int{{0, runes}}
	}
}

// shardWorkers picks the scan fan-out for n entries, keeping tiny
// stores single-threaded (see minShardEntries).
func shardWorkers(n int) int {
	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	return workers
}

// runShards runs shard(w, lo, hi) for the workers-way split of [0, n)
// in per-sized ranges, in parallel past one worker. Empty tail shards
// are skipped (their heap slot stays nil; selectTop tolerates that).
func runShards(workers, per, n int, shard func(w, lo, hi int)) {
	if workers == 1 {
		shard(0, 0, n)
		return
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo := w * per
		hi := min(lo+per, n)
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			shard(w, lo, hi)
		}(w, lo, hi)
	}
	wg.Wait()
}

// queryNamesSub is the substring-only name scan: the pre-fuzzy engine,
// dispatched when the fuzzy tier is disabled. It must stay
// behavior-identical to the original Query body.
func (s *Store) queryNamesSub(qs string, ascii bool, n, limit int, b *Blend) []Result {
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

	return s.selectTop(heaps, limit, b)
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
// the best limit entries. An ACTIVE blend reorders the merged set by
// the frecency/recency/noise signals here -- the only ranking stage
// that ever sees them (blend.go); inactive (the normal case) takes
// the exact pre-blend path below.
func (s *Store) selectTop(heaps []*topK, limit int, b *Blend) []Result {
	var all []cand
	for _, h := range heaps {
		if h == nil {
			continue // an empty tail shard never armed its heap
		}
		all = append(all, h.items...)
	}
	if len(all) == 0 {
		return nil
	}
	if b.active() {
		return s.selectBlended(all, limit, b)
	}
	sort.Slice(all, func(i, j int) bool { return s.candCompare(all[i], all[j]) < 0 })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]Result, len(all))
	for i, c := range all {
		out[i] = Result{Path: s.EntryPath(c.id), Name: s.Name(c.id), IsDir: c.isDir}
	}
	if tr := b.traceBuf(); tr != nil {
		// The inactive-blend trace records only what this path
		// computed: match class and alignment; the signal components
		// stay zero (they never participated), EffClass == Class.
		sig := make([]ResultSignals, len(all))
		for i, c := range all {
			sig[i] = ResultSignals{
				Path:     out[i].Path,
				Class:    c.class,
				EffClass: c.class,
				Align:    c.score,
				IsDir:    c.isDir,
				PathLen:  c.pathLen,
			}
		}
		*tr = sig
	}
	return out
}
