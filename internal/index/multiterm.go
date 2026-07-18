package index

// Multi-term name-mode queries (>= 2 whitespace-separated terms; see
// match.Terms). Semantics: EVERY term must match the name, in any
// order ("fire fox" and "fox fire" both find "Firefox"); whitespace is
// a term separator, never matched literally, so names containing
// spaces are still found because each term substring-matches ("my
// backup" finds "My Documents Backup"). Path mode (a separator in the
// query) stays literal single-pattern -- paths legitimately contain
// spaces -- and is dispatched before term splitting.
//
// Classes: classSub when every term substring-matches, classFuzzy when
// all terms match with at least one term subsequence-only. Multi-term
// queries never produce exact/prefix classes (a name cannot equal or
// start with two different terms at once; a degenerate repeated-term
// query stays classSub for simplicity). classFuzzy candidates carry
// the summed per-term alignment score (every term scored with the
// shared internal/match scorer against the name -- substring terms
// simply align at their occurrence).
//
// ASCII fast path (every term ASCII): a DRIVER-term blob scan. The
// driver is the term whose rarest byte has the fewest occurrences in
// the name blob (exact per-store histogram) -- the cheapest stream of
// candidate entries; any term whose rarest byte count is ZERO proves
// the whole query empty. Phase A runs the existing anchored substring
// scan for the driver and fully judges each candidate entry against
// the remaining terms (substring first, subsequence fallback), marking
// every visited entry in the pooled bitset. Phase B (the fuzzy sweep
// for entries where the DRIVER itself is subsequence-only) reuses the
// phase-2 rarest-byte anchor sweep and skips marked entries; it is
// skipped entirely when the phase-A all-substring hits already fill
// the limit (no classFuzzy row could enter the top-limit -- the same
// argument as the single-term skip rule), when fuzzy is disabled, or
// when the driver is a single unit (its subsequence IS its substring).
// Queries with any non-ASCII term take a per-entry slow path with the
// same sharding and semantics (the documented rune-regime cost).

import (
	"sort"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// queryNamesMulti dispatches a multi-term query.
func (s *Store) queryNamesMulti(terms []match.Term, n, limit int, fuzzyOff bool, b *Blend) []Result {
	allASCII := true
	for _, t := range terms {
		if !t.ASCII {
			allASCII = false
			break
		}
	}
	pats := make([][]int32, len(terms))
	for i, t := range terms {
		pats[i] = t.Units()
	}
	if !allASCII {
		return s.queryMultiFold(terms, pats, n, limit, fuzzyOff, b)
	}

	// Driver selection + the impossible-byte fast reject.
	driver := 0
	bestCnt := ^uint64(0)
	for i, t := range terms {
		cnt := s.blobByteCount(s.fuzzyAnchorByte(t.Pat))
		if cnt == 0 {
			return nil // some pattern byte never occurs in the blob
		}
		if cnt < bestCnt {
			bestCnt, driver = cnt, i
		}
	}

	workers, per := shardPlanMarked(n)
	marks := fuzzyMarksGet(n)
	defer fuzzyMarksPut(marks)
	heaps := make([]*topK, workers)
	counts := make([]int, workers)

	runShards(workers, per, n, func(w, lo, hi int) {
		h := newTopK(s, limit)
		sc := fuzzyScratchGet()
		defer fuzzyScratchPut(sc)
		counts[w] = s.scanRangeMultiSub(terms, driver, pats, lo, hi, h, marks, sc, fuzzyOff)
		heaps[w] = h
	})

	total := 0
	for _, c := range counts {
		total += c
	}
	if total < limit && !fuzzyOff && len(terms[driver].Pat) >= 2 {
		anchor := s.fuzzyAnchorByte(terms[driver].Pat)
		runShards(workers, per, n, func(w, lo, hi int) {
			sc := fuzzyScratchGet()
			defer fuzzyScratchPut(sc)
			s.scanRangeMultiFuzzy(terms, driver, pats, anchor, lo, hi, heaps[w], marks, sc)
		})
	}
	return s.selectTop(heaps, limit, b)
}

// scanRangeMultiSub is phase A: the anchored substring scan for the
// driver term over entries [lo, hi), fully judging every candidate.
// Every VISITED live entry is marked (judged here, phase B must skip
// it -- re-judging an entry added here would duplicate it). Returns
// the shard's all-substring (classSub) match count, the skip-rule
// input.
func (s *Store) scanRangeMultiSub(terms []match.Term, driver int, pats [][]int32, lo, hi int, h *topK, marks []uint64, sc *fuzzyScratch, fuzzyOff bool) int {
	base := s.nameOff[lo]
	blob := s.names[base:s.nameOff[hi]]
	scn := newCiScan(blob, terms[driver].Pat)
	off := 0
	cur := lo
	count := 0
	for {
		rel := scn.next(off)
		if rel < 0 {
			return count
		}
		pos := base + uint32(rel)
		e := cur + sort.Search(hi-cur, func(k int) bool { return s.nameOff[cur+k+1] > pos })
		if s.flags[e]&flagTombstone == 0 {
			markEntry(marks, e)
			nb := s.nameBytes(int32(e))
			anyFuzzy, ok := s.judgeOthers(nb, terms, driver, fuzzyOff)
			switch {
			case ok && !anyFuzzy:
				h.add(s.makeMultiCand(e, classSub, 0))
				count++
			case ok:
				h.add(s.makeMultiCand(e, classFuzzy, sc.scoreASCIIMulti(nb, pats)))
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

// judgeOthers checks every non-driver term against the name: substring
// first, subsequence fallback when fuzzy is on. ok=false when any term
// fails; anyFuzzy reports a subsequence-only term.
func (s *Store) judgeOthers(nb []byte, terms []match.Term, driver int, fuzzyOff bool) (anyFuzzy, ok bool) {
	for i, t := range terms {
		if i == driver {
			continue
		}
		if ciIndexASCII(nb, t.Pat) >= 0 {
			continue
		}
		if !fuzzyOff && fuzzySubseqASCII(nb, t.Pat) {
			anyFuzzy = true
			continue
		}
		return false, false
	}
	return anyFuzzy, true
}

// scanRangeMultiFuzzy is phase B: the rarest-byte anchor sweep for
// entries where the driver term is subsequence-only. Entries phase A
// visited (driver substring) are marked and skipped; survivors need
// the driver subsequence plus every other term (substring or
// subsequence -- fuzzy is on whenever this phase runs).
func (s *Store) scanRangeMultiFuzzy(terms []match.Term, driver int, pats [][]int32, anchor byte, lo, hi int, h *topK, marks []uint64, sc *fuzzyScratch) {
	base := s.nameOff[lo]
	blob := s.names[base:s.nameOff[hi]]
	scn := s.newFuzzyAnchorScan(blob, anchor)
	off := 0
	cur := lo
	for {
		rel := scn.next(off)
		if rel < 0 {
			return
		}
		pos := base + uint32(rel)
		e := cur + sort.Search(hi-cur, func(k int) bool { return s.nameOff[cur+k+1] > pos })
		if s.flags[e]&flagTombstone == 0 && !entryMarked(marks, e) {
			nb := s.nameBytes(int32(e))
			if fuzzySubseqASCII(nb, terms[driver].Pat) {
				// The driver is subsequence-only here: classFuzzy
				// regardless of how the other terms matched.
				if _, ok := s.judgeOthers(nb, terms, driver, false); ok {
					h.add(s.makeMultiCand(e, classFuzzy, sc.scoreASCIIMulti(nb, pats)))
				}
			}
		}
		next := e + 1
		if next >= hi {
			return
		}
		off = int(s.nameOff[next] - base)
		cur = next
	}
}

// queryMultiFold is the multi-term slow path for term sets carrying
// non-ASCII patterns: a sharded per-entry judgment in each term's own
// regime. O(entries * terms * name * pat) -- the same documented cost
// class as the single-term rune path.
func (s *Store) queryMultiFold(terms []match.Term, pats [][]int32, n, limit int, fuzzyOff bool, b *Blend) []Result {
	workers, per := shardPlanMarked(n)
	heaps := make([]*topK, workers)
	runShards(workers, per, n, func(w, lo, hi int) {
		h := newTopK(s, limit)
		sc := fuzzyScratchGet()
		defer fuzzyScratchPut(sc)
		for e := lo; e < hi; e++ {
			if s.flags[e]&flagTombstone != 0 {
				continue
			}
			nb := s.nameBytes(int32(e))
			anyFuzzy := false
			ok := true
			for i, t := range terms {
				if termContainsBytes(nb, t) {
					continue
				}
				if !fuzzyOff && termSubseqBytes(nb, t, pats[i]) {
					anyFuzzy = true
					continue
				}
				ok = false
				break
			}
			if !ok {
				continue
			}
			if anyFuzzy {
				h.add(s.makeMultiCand(e, classFuzzy, sc.scoreMultiMixed(nb, terms, pats)))
			} else {
				h.add(s.makeMultiCand(e, classSub, 0))
			}
		}
		heaps[w] = h
	})
	return s.selectTop(heaps, limit, b)
}

// termContainsBytes reports a substring match of term t in the name
// bytes, in t's regime.
func termContainsBytes(nb []byte, t match.Term) bool {
	if t.ASCII {
		return ciIndexASCII(nb, t.Pat) >= 0
	}
	return foldContains(nb, t.Pat)
}

// termSubseqBytes reports a subsequence match of term t in the name
// bytes, in t's regime (patUnits = t.Units(), precomputed).
func termSubseqBytes(nb []byte, t match.Term, patUnits []int32) bool {
	if t.ASCII {
		return fuzzySubseqASCII(nb, t.Pat)
	}
	return fuzzySubseqFold(nb, patUnits)
}

// makeMultiCand builds a multi-term ranking candidate for entry e.
func (s *Store) makeMultiCand(e int, class uint8, score int32) cand {
	nameLen := int(s.nameOff[e+1] - 1 - s.nameOff[e])
	pathLen := joinedLen(s.dirs[s.parent[e]], nameLen)
	return cand{
		id:      int32(e),
		pathLen: int32(pathLen),
		score:   score,
		class:   class,
		isDir:   s.flags[e]&flagDir != 0,
	}
}

// scoreASCIIMulti sums the per-term alignment scores against one name
// prepared ONCE in the byte regime.
func (sc *fuzzyScratch) scoreASCIIMulti(nb []byte, pats [][]int32) int32 {
	sc.units = match.GrowI32(sc.units, len(nb))
	sc.bonus = match.GrowI8(sc.bonus, len(nb))
	match.PrepareASCII(nb, sc.units, sc.bonus)
	total := int32(0)
	for _, p := range pats {
		total += sc.dp.Align(p, sc.units, sc.bonus)
	}
	return total
}

// scoreMultiMixed sums the per-term alignment scores for a mixed-
// regime term set: the name is prepared at most once per regime.
func (sc *fuzzyScratch) scoreMultiMixed(nb []byte, terms []match.Term, pats [][]int32) int32 {
	total := int32(0)
	prepared := false
	for i, t := range terms {
		if !t.ASCII {
			continue
		}
		if !prepared {
			sc.units = match.GrowI32(sc.units, len(nb))
			sc.bonus = match.GrowI8(sc.bonus, len(nb))
			match.PrepareASCII(nb, sc.units, sc.bonus)
			prepared = true
		}
		total += sc.dp.Align(pats[i], sc.units, sc.bonus)
	}
	prepared = false
	for i, t := range terms {
		if t.ASCII {
			continue
		}
		if !prepared {
			sc.units, sc.bonus = match.PrepareFold(nb, sc.units[:0], sc.bonus[:0])
			prepared = true
		}
		total += sc.dp.Align(pats[i], sc.units, sc.bonus)
	}
	return total
}

// shardPlanMarked computes the worker count and 64-aligned shard size
// used by every scan that writes the shared mark bitset (disjoint
// words per shard, no atomics; see queryNamesFuzzy).
func shardPlanMarked(n int) (workers, per int) {
	workers = shardWorkers(n)
	per = ((n+workers-1)/workers + 63) &^ 63
	workers = (n + per - 1) / per
	return workers, per
}
