package index

// Fuzzy (subsequence) matching for name-mode queries.
//
// Entries whose folded name does NOT contain the query as a substring
// but DOES contain it as a subsequence (all query units present in
// order, gaps allowed, same case folding as the engine: byte-wise
// foldTable for all-ASCII queries, per-rune foldRune otherwise) match
// with classFuzzy -- strictly below every substring tier, so
// exact/prefix/substring results always outrank fuzzy ones. Within the
// fuzzy tier candidates rank by a position-aware alignment score
// (higher first), then the usual tie-breaks. There is no typo
// tolerance: a query unit that never occurs in the name is a miss.
// Path-mode queries have no fuzzy tier.
//
// Two-phase scan (queryNamesFuzzy):
//
//  1. Phase 1 is the EXISTING substring scan, unchanged, plus two side
//     channels: each shard counts its live matches and sets the entry's
//     bit in a shared pooled bitset (shard entry ranges are rounded to
//     multiples of 64 so no two shards ever write the same word).
//  2. Skip rule: when the phase-1 total reaches the query limit, phase
//     2 is skipped entirely -- every phase-1 hit has a class strictly
//     better than classFuzzy, so no fuzzy hit could enter the top
//     limit. Common (expensive) queries therefore cost exactly what
//     they cost without the fuzzy tier, plus the bitset upkeep.
//  3. Phase 2 (ASCII queries) enumerates candidates with a rarest-byte
//     blob sweep: bytes.IndexByte over both case variants of the
//     query byte with the fewest occurrences in the name blob (exact
//     counts from the store's byte histogram -- the static
//     nameByteFreq table cannot know that, say, 'q' is absent from a
//     corpus where 'z' is common). Hits map to entries exactly like
//     scanRange; tombstoned and phase-1-marked entries are skipped,
//     survivors get the subsequence check and, on a pass, a score and
//     a classFuzzy heap add. Cost: one extra blob sweep plus a few
//     candidate checks -- the same order as the substring scan.
//     Queries with non-ASCII runes take a per-entry rune subsequence
//     walk instead (correct, documented slow, like scanRangeFold).
//
// A single-unit query is a degenerate case: its subsequence matches
// ARE its substring matches, so the fuzzy tier is provably empty and
// phase 2 is skipped outright.
//
// Scoring is the shared internal/match alignment scorer (constants,
// bonuses, DP, and greedy fallback live THERE; fuzzyScratch below is
// only the pooled per-worker buffer glue). Only candidates that
// already passed the subsequence check are ever scored, so scoring is
// off the hot path. Tests pin score ORDERINGS, not absolute values.

import (
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// fuzzyMaxDPUnits bounds the optimal-alignment DP (match.MaxDPUnits).
const fuzzyMaxDPUnits = match.MaxDPUnits

// queryNamesFuzzy is the fuzzy-enabled name-mode scan: the substring
// phase (identical results to queryNamesSub) followed, when the
// substring hits leave room under limit, by the subsequence phase.
func (s *Store) queryNamesFuzzy(qs string, ascii bool, n, limit int, b *Blend) []Result {
	// 64-aligned shards touch disjoint bitset words, so phase 1 needs
	// no atomics (shardPlanMarked, shared with the multi-term scans).
	workers, per := shardPlanMarked(n)

	marks := fuzzyMarksGet(n)
	defer fuzzyMarksPut(marks)
	heaps := make([]*topK, workers)
	counts := make([]int, workers)

	runShards(workers, per, n, func(w, lo, hi int) {
		h := newTopK(s, limit)
		counts[w] = s.scanNames(qs, ascii, lo, hi, h, marks)
		heaps[w] = h
	})

	total := 0
	for _, c := range counts {
		total += c
	}
	if total < limit && fuzzyViable(qs, ascii) {
		// The wg.Wait above is the barrier: phase 2 reads the bitset,
		// nothing writes it anymore.
		anchor := byte(0)
		if ascii {
			anchor = s.fuzzyAnchorByte(qs)
			if s.blobByteCount(anchor) == 0 {
				// The rarest pattern byte never occurs in the blob at
				// all (exact histogram): no name can hold the
				// subsequence, so phase 2 is provably empty.
				return s.selectTop(heaps, limit, b)
			}
		}
		patUnits := fuzzyPatternUnits(qs, ascii)
		runShards(workers, per, n, func(w, lo, hi int) {
			sc := fuzzyScratchGet()
			defer fuzzyScratchPut(sc)
			sc.pat = patUnits
			if ascii {
				s.scanRangeFuzzy(qs, anchor, lo, hi, heaps[w], marks, sc)
			} else {
				s.scanRangeFuzzyFold(patUnits, lo, hi, heaps[w], marks, sc)
			}
		})
	}
	return s.selectTop(heaps, limit, b)
}

// fuzzyViable reports whether the pattern can have fuzzy-only matches:
// a single-unit subsequence IS a substring, so only multi-unit
// patterns get a phase 2.
func fuzzyViable(qs string, ascii bool) bool {
	if ascii {
		return len(qs) >= 2
	}
	return utf8.RuneCountInString(qs) >= 2
}

// fuzzyPatternUnits lowers the pre-folded pattern into comparison
// units (match.PatternUnits).
func fuzzyPatternUnits(qs string, ascii bool) []int32 {
	return match.PatternUnits(qs, ascii)
}

// scanRangeFuzzy is the phase-2 candidate sweep for ASCII patterns
// over entries [lo, hi): persistent IndexByte streams for the anchor
// byte's case variants (a variant the histogram proves absent from
// the blob is never scanned -- half the sweep on single-case
// corpora), entry mapping and skip-to-entry-end exactly like
// scanRange, then the subsequence check and scoring on unmarked live
// survivors.
func (s *Store) scanRangeFuzzy(pat string, anchor byte, lo, hi int, h *topK, marks []uint64, sc *fuzzyScratch) {
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
			if fuzzySubseqASCII(nb, pat) {
				h.add(s.makeFuzzyCand(e, sc.scoreASCII(nb)))
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

// fuzzyAnchorScan iterates the blob positions holding either case
// variant of the anchor byte: the phase-2 counterpart of ciScan's
// two-variant stream (same persistence contract -- successive next
// offsets must not decrease and must move past the previous hit), but
// without a pattern verification step, and with a variant whose exact
// blob count is zero never scanned at all.
type fuzzyAnchorScan struct {
	blob   []byte
	c1, c2 byte // c2 == c1 when there is no second variant to scan
	n1, n2 int  // next occurrence at/after the cursor; -1 = exhausted
}

// newFuzzyAnchorScan arms the streams for the folded anchor byte c,
// consulting the byte histogram to drop provably-empty variants. The
// caller has already established that at least one variant occurs
// (blobByteCount > 0).
func (s *Store) newFuzzyAnchorScan(blob []byte, c byte) fuzzyAnchorScan {
	sc := fuzzyAnchorScan{blob: blob, c1: c, c2: c, n1: -1, n2: -1}
	if u := upperVariant(c); u != c {
		switch {
		case s.byteFreq[c] == 0:
			sc.c1 = u // only the uppercase form exists
		case s.byteFreq[u] > 0:
			sc.c2 = u // both exist: two streams
		}
	}
	hi := len(blob) - 1
	sc.n1 = nextIndexByte(blob, sc.c1, 0, hi)
	if sc.c2 != sc.c1 {
		sc.n2 = nextIndexByte(blob, sc.c2, 0, hi)
	}
	return sc
}

// next returns the first anchor occurrence at or after off, or -1.
// Successive calls must not decrease off.
func (sc *fuzzyAnchorScan) next(off int) int {
	hi := len(sc.blob) - 1
	if sc.n1 >= 0 && sc.n1 < off {
		sc.n1 = nextIndexByte(sc.blob, sc.c1, off, hi)
	}
	if sc.n2 >= 0 && sc.n2 < off {
		sc.n2 = nextIndexByte(sc.blob, sc.c2, off, hi)
	}
	h := sc.n1
	if h < 0 || (sc.n2 >= 0 && sc.n2 < h) {
		h = sc.n2
	}
	return h
}

// scanRangeFuzzyFold is the phase-2 scan for patterns carrying
// non-ASCII runes: a per-entry rune subsequence walk (no sweep), the
// fuzzy counterpart of scanRangeFold.
func (s *Store) scanRangeFuzzyFold(patUnits []int32, lo, hi int, h *topK, marks []uint64, sc *fuzzyScratch) {
	for e := lo; e < hi; e++ {
		if s.flags[e]&flagTombstone != 0 || entryMarked(marks, e) {
			continue
		}
		nb := s.nameBytes(int32(e))
		if !fuzzySubseqFold(nb, patUnits) {
			continue
		}
		h.add(s.makeFuzzyCand(e, sc.scoreFold(nb)))
	}
}

// makeFuzzyCand builds the classFuzzy ranking candidate for entry e.
func (s *Store) makeFuzzyCand(e int, score int32) cand {
	nameLen := int(s.nameOff[e+1] - 1 - s.nameOff[e])
	pathLen := joinedLen(s.dirs[s.parent[e]], nameLen)
	return cand{
		id:      int32(e),
		pathLen: int32(pathLen),
		score:   score,
		class:   classFuzzy,
		isDir:   s.flags[e]&flagDir != 0,
	}
}

// fuzzyAnchorByte picks the pattern byte with the fewest occurrences
// in the name blob (both case variants counted, exact per-store
// counts from the byte histogram). The histogram counts the whole
// blob, tombstoned names included -- correct, because the sweep scans
// the whole blob too.
func (s *Store) fuzzyAnchorByte(pat string) byte {
	best := pat[0]
	bestN := s.blobByteCount(best)
	for i := 1; i < len(pat); i++ {
		if n := s.blobByteCount(pat[i]); n < bestN {
			best, bestN = pat[i], n
		}
	}
	return best
}

// blobByteCount returns how often the folded byte c occurs in the name
// blob, counting both ASCII case variants.
func (s *Store) blobByteCount(c byte) uint64 {
	n := s.byteFreq[c]
	if u := upperVariant(c); u != c {
		n += s.byteFreq[u]
	}
	return n
}

// fuzzySubseqASCII reports whether the pre-folded ASCII pattern is a
// subsequence of the name (match.SubseqASCII).
func fuzzySubseqASCII(nb []byte, pat string) bool { return match.SubseqASCII(nb, pat) }

// fuzzySubseqFold reports whether the pattern (as folded rune units)
// is a subsequence of the name's folded runes (match.SubseqFold).
func fuzzySubseqFold(nb []byte, patUnits []int32) bool { return match.SubseqFold(nb, patUnits) }

// Phase-1 match bitset: one bit per entry, pooled (about 3.8 MB at 30M
// entries), written by phase 1 (64-aligned shards, no sharing), read
// only by phase 2.

var fuzzyMarksPool sync.Pool

// fuzzyMarksGet returns a zeroed bitset covering n entries.
func fuzzyMarksGet(n int) []uint64 {
	words := (n + 63) / 64
	if v, _ := fuzzyMarksPool.Get().(*[]uint64); v != nil && cap(*v) >= words {
		m := (*v)[:words]
		clear(m)
		return m
	}
	return make([]uint64, words)
}

func fuzzyMarksPut(m []uint64) { fuzzyMarksPool.Put(&m) }

func markEntry(marks []uint64, e int) { marks[e>>6] |= 1 << (e & 63) }

func entryMarked(marks []uint64, e int) bool { return marks[e>>6]&(1<<(e&63)) != 0 }

// fuzzyScratch carries one worker's reusable scoring buffers: the
// shared pattern units plus per-candidate folded name units and
// position bonuses, and the shared scorer's rolling DP rows. The
// scoring algorithm itself lives in internal/match; this struct is
// only the pooled buffer glue around it.
type fuzzyScratch struct {
	pat   []int32
	units []int32
	bonus []int8
	dp    match.DPState
}

var fuzzyScratchPool = sync.Pool{New: func() any { return new(fuzzyScratch) }}

func fuzzyScratchGet() *fuzzyScratch   { return fuzzyScratchPool.Get().(*fuzzyScratch) }
func fuzzyScratchPut(sc *fuzzyScratch) { fuzzyScratchPool.Put(sc) }

// scoreASCII scores an already-verified ASCII subsequence match of
// sc.pat against the original-case name bytes.
func (sc *fuzzyScratch) scoreASCII(nb []byte) int32 {
	sc.units = match.GrowI32(sc.units, len(nb))
	sc.bonus = match.GrowI8(sc.bonus, len(nb))
	match.PrepareASCII(nb, sc.units, sc.bonus)
	return sc.dp.Align(sc.pat, sc.units, sc.bonus)
}

// scoreFold is scoreASCII for the rune regime: units are the name's
// folded runes, bonuses classify the original runes.
func (sc *fuzzyScratch) scoreFold(nb []byte) int32 {
	sc.units, sc.bonus = match.PrepareFold(nb, sc.units[:0], sc.bonus[:0])
	return sc.dp.Align(sc.pat, sc.units, sc.bonus)
}

// fuzzyAlignGreedy scores the first-occurrence (leftmost) alignment
// (match.AlignGreedy), the fallback for names past the DP bound.
func fuzzyAlignGreedy(pat, units []int32, bonus []int8) int32 {
	return match.AlignGreedy(pat, units, bonus)
}
