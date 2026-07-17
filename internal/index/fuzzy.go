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
// Scoring (fuzzyAlignDP / fuzzyAlignGreedy): per matched unit a base
// score plus positional bonuses -- name start, after a word boundary
// ('-', '_', '.', ' ' and letter<->digit transitions), camelCase
// lower->upper steps, and consecutive-run continuation -- minus a
// capped affine gap penalty per gap. Names up to fuzzyMaxDPUnits units
// get an optimal-alignment DP (fzf-v2 style); longer names fall back
// to a greedy first-occurrence alignment. Only candidates that already
// passed the subsequence check are ever scored, so scoring is off the
// hot path. Tests pin score ORDERINGS, not absolute values.

import (
	"runtime"
	"sort"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Scoring model constants. Only their relative order is contractual
// (fuzzy_test.go pins orderings): boundary > camel > transition keeps
// "foo_bar" ahead of "FooBar" ahead of unstructured scatter for an
// "fb"-style query, and the gap cap keeps one huge gap from drowning
// an otherwise good match.
const (
	fuzzyScoreMatch       = 16 // per matched query unit
	fuzzyBonusStart       = 12 // match at the first unit of the name
	fuzzyBonusBoundary    = 10 // match right after '-', '_', '.', ' '
	fuzzyBonusCamel       = 7  // match at a lower->upper camelCase step
	fuzzyBonusTransition  = 6  // match at a letter<->digit transition
	fuzzyBonusConsecutive = 8  // match directly after the previous match
	fuzzyGapOpen          = 3  // first skipped unit of a gap
	fuzzyGapExtend        = 1  // each further skipped unit
	fuzzyGapCap           = 9  // ceiling of one gap's total penalty
	// fuzzyMaxDPUnits bounds the optimal-alignment DP; longer names
	// (pathological -- real file names top out at 255 bytes) take the
	// greedy fallback.
	fuzzyMaxDPUnits = 512
)

// fuzzyNInf is the DP's minus-infinity: low enough that no chain of
// additive bonuses can ever raise an unreachable state above a real
// score, high enough that per-row arithmetic can never underflow
// int32.
const fuzzyNInf = int32(-1) << 28

// queryNamesFuzzy is the fuzzy-enabled name-mode scan: the substring
// phase (identical results to queryNamesSub) followed, when the
// substring hits leave room under limit, by the subsequence phase.
func (s *Store) queryNamesFuzzy(qs string, ascii bool, n, limit int) []Result {
	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	// Round the shard size up to a multiple of 64 entries: shards then
	// touch disjoint bitset words, so phase 1 needs no atomics.
	per := ((n+workers-1)/workers + 63) &^ 63
	workers = (n + per - 1) / per

	marks := fuzzyMarksGet(n)
	defer fuzzyMarksPut(marks)
	heaps := make([]*topK, workers)
	counts := make([]int, workers)

	runPhase := func(shard func(w, lo, hi int)) {
		if workers == 1 {
			shard(0, 0, n)
			return
		}
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo := w * per
			hi := min(lo+per, n)
			wg.Add(1)
			go func(w, lo, hi int) {
				defer wg.Done()
				shard(w, lo, hi)
			}(w, lo, hi)
		}
		wg.Wait()
	}

	runPhase(func(w, lo, hi int) {
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
		patUnits := fuzzyPatternUnits(qs, ascii)
		runPhase(func(w, lo, hi int) {
			sc := fuzzyScratchGet()
			defer fuzzyScratchPut(sc)
			sc.pat = patUnits
			if ascii {
				s.scanRangeFuzzy(qs, lo, hi, heaps[w], marks, sc)
			} else {
				s.scanRangeFuzzyFold(patUnits, lo, hi, heaps[w], marks, sc)
			}
		})
	}
	return s.selectTop(heaps, limit)
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
// units: bytes for the ASCII regime, runes otherwise.
func fuzzyPatternUnits(qs string, ascii bool) []int32 {
	if ascii {
		units := make([]int32, len(qs))
		for i := 0; i < len(qs); i++ {
			units[i] = int32(qs[i])
		}
		return units
	}
	units := make([]int32, 0, len(qs))
	for _, r := range qs {
		units = append(units, int32(r))
	}
	return units
}

// scanRangeFuzzy is the phase-2 candidate sweep for ASCII patterns
// over entries [lo, hi): a ciScan for the anchor byte alone (both case
// variants, persistent IndexByte streams), entry mapping and
// skip-to-entry-end exactly like scanRange, then the subsequence check
// and scoring on unmarked live survivors.
func (s *Store) scanRangeFuzzy(pat string, lo, hi int, h *topK, marks []uint64, sc *fuzzyScratch) {
	base := s.nameOff[lo]
	blob := s.names[base:s.nameOff[hi]]
	scn := newCiScan(blob, s.fuzzyAnchor(pat))
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

// fuzzyAnchor picks the pattern byte with the fewest occurrences in
// the name blob (both case variants counted, exact per-store counts
// from the byte histogram) and returns it as a one-byte pattern for
// ciScan. The histogram counts the whole blob, tombstoned names
// included -- correct, because the sweep scans the whole blob too.
func (s *Store) fuzzyAnchor(pat string) string {
	best := 0
	bestN := s.blobByteCount(pat[0])
	for i := 1; i < len(pat); i++ {
		if n := s.blobByteCount(pat[i]); n < bestN {
			best, bestN = i, n
		}
	}
	return pat[best : best+1]
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
// subsequence of the name, folding each name byte through foldTable.
func fuzzySubseqASCII(nb []byte, pat string) bool {
	if len(pat) > len(nb) {
		return false
	}
	j := 0
	for i := 0; i < len(nb) && j < len(pat); i++ {
		if foldTable[nb[i]] == pat[j] {
			j++
		}
	}
	return j == len(pat)
}

// fuzzySubseqFold reports whether the pattern (as folded rune units)
// is a subsequence of the name's foldRune-folded runes. Invalid UTF-8
// decodes as U+FFFD per byte, matching foldContains.
func fuzzySubseqFold(nb []byte, patUnits []int32) bool {
	j := 0
	for i := 0; i < len(nb) && j < len(patUnits); {
		r, n := decodeRuneAt(nb, i)
		if int32(foldRune(r)) == patUnits[j] {
			j++
		}
		i += n
	}
	return j == len(patUnits)
}

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
// shared pattern units plus per-candidate folded name units, position
// bonuses, and the DP's four rolling rows.
type fuzzyScratch struct {
	pat   []int32
	units []int32
	bonus []int8
	rowH  []int32
	rowP  []int32
	curH  []int32
	curP  []int32
}

var fuzzyScratchPool = sync.Pool{New: func() any { return new(fuzzyScratch) }}

func fuzzyScratchGet() *fuzzyScratch   { return fuzzyScratchPool.Get().(*fuzzyScratch) }
func fuzzyScratchPut(sc *fuzzyScratch) { fuzzyScratchPool.Put(sc) }

// scoreASCII scores an already-verified ASCII subsequence match of
// sc.pat against the original-case name bytes.
func (sc *fuzzyScratch) scoreASCII(nb []byte) int32 {
	sc.units = growI32(sc.units, len(nb))
	sc.bonus = growI8(sc.bonus, len(nb))
	var prev byte
	for i, c := range nb {
		sc.units[i] = int32(foldTable[c])
		sc.bonus[i] = asciiBonus(prev, c, i == 0)
		prev = c
	}
	return sc.alignScore(len(nb))
}

// scoreFold is scoreASCII for the rune regime: units are the name's
// foldRune-folded runes, bonuses classify the original runes.
func (sc *fuzzyScratch) scoreFold(nb []byte) int32 {
	sc.units = sc.units[:0]
	sc.bonus = sc.bonus[:0]
	var prev rune
	first := true
	for i := 0; i < len(nb); {
		r, n := decodeRuneAt(nb, i)
		sc.units = append(sc.units, int32(foldRune(r)))
		sc.bonus = append(sc.bonus, runeBonus(prev, r, first))
		prev, first = r, false
		i += n
	}
	return sc.alignScore(len(sc.units))
}

// alignScore dispatches a prepared candidate (units/bonus filled for n
// name units) to the DP or, past the size bound, the greedy fallback.
func (sc *fuzzyScratch) alignScore(n int) int32 {
	if n <= fuzzyMaxDPUnits {
		return sc.alignDP(n)
	}
	return fuzzyAlignGreedy(sc.pat, sc.units[:n], sc.bonus[:n])
}

// asciiBonus classifies the positional bonus for a match at a byte
// whose predecessor is prev (bytes of a multi-byte rune land in the
// no-bonus default; the ASCII regime never inspects them as runes).
func asciiBonus(prev, cur byte, first bool) int8 {
	if first {
		return fuzzyBonusStart
	}
	switch prev {
	case '-', '_', '.', ' ':
		return fuzzyBonusBoundary
	}
	if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
		return fuzzyBonusCamel
	}
	pd := prev >= '0' && prev <= '9'
	cd := cur >= '0' && cur <= '9'
	pl := isASCIILetter(prev)
	cl := isASCIILetter(cur)
	if (pl && cd) || (pd && cl) {
		return fuzzyBonusTransition
	}
	return 0
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// runeBonus is asciiBonus over runes, using the Unicode categories; on
// ASCII input it agrees with asciiBonus exactly (pinned by tests).
func runeBonus(prev, cur rune, first bool) int8 {
	if first {
		return fuzzyBonusStart
	}
	switch prev {
	case '-', '_', '.', ' ':
		return fuzzyBonusBoundary
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return fuzzyBonusCamel
	}
	if (unicode.IsLetter(prev) && unicode.IsDigit(cur)) ||
		(unicode.IsDigit(prev) && unicode.IsLetter(cur)) {
		return fuzzyBonusTransition
	}
	return 0
}

// alignDP computes the optimal alignment score of sc.pat against the
// prepared name (n units): affine gaps with a per-gap cap, consecutive
// bonus, O(m*n) time over four rolling rows. States per pattern unit q
// and name position p:
//
//	H[q][p] = best score with pat[q] matched exactly at p
//	P[q][p] = max over k < p of H[q][k] - gapPen(p-k), the "last match
//	          before p, gap running through p" state, where
//	          gapPen(g) = min(fuzzyGapOpen + (g-1)*fuzzyGapExtend,
//	          fuzzyGapCap); the cap is realized by a running-max floor
//	          (see fillGapRow).
//
// The subsequence check has already passed, so a valid final state
// exists; the gap before the first match and after the last one is
// free (position preference is expressed by bonuses instead).
func (sc *fuzzyScratch) alignDP(n int) int32 {
	pat := sc.pat
	units := sc.units[:n]
	bonus := sc.bonus[:n]
	m := len(pat)

	sc.rowH = growI32(sc.rowH, n)
	sc.rowP = growI32(sc.rowP, n)
	sc.curH = growI32(sc.curH, n)
	sc.curP = growI32(sc.curP, n)
	prevH, prevP, curH, curP := sc.rowH, sc.rowP, sc.curH, sc.curP

	for p := 0; p < n; p++ {
		if units[p] == pat[0] {
			curH[p] = fuzzyScoreMatch + int32(bonus[p])
		} else {
			curH[p] = fuzzyNInf
		}
	}
	for q := 1; q < m; q++ {
		prevH, curH = curH, prevH
		prevP, curP = curP, prevP
		fillGapRow(prevH, prevP)
		curH[0] = fuzzyNInf
		for p := 1; p < n; p++ {
			v := fuzzyNInf
			if units[p] == pat[q] {
				b := int32(bonus[p])
				cb := b
				if cb < fuzzyBonusConsecutive {
					cb = fuzzyBonusConsecutive
				}
				v = prevH[p-1] + fuzzyScoreMatch + cb
				if w := prevP[p-1] + fuzzyScoreMatch + b; w > v {
					v = w
				}
			}
			curH[p] = v
		}
	}
	best := fuzzyNInf
	for p := 0; p < n; p++ {
		if curH[p] > best {
			best = curH[p]
		}
	}
	sc.rowH, sc.rowP, sc.curH, sc.curP = prevH, prevP, curH, curP
	return best
}

// fillGapRow derives one pattern row's gap state P from its match
// state H (see alignDP): P[t] chooses, per position, between opening a
// gap after a match at t-1, extending the running gap, and the capped
// floor under the best match seen so far.
func fillGapRow(h, gp []int32) {
	runMax := fuzzyNInf
	prev := fuzzyNInf
	gp[0] = fuzzyNInf
	for t := 1; t < len(h); t++ {
		if h[t-1] > runMax {
			runMax = h[t-1]
		}
		v := h[t-1] - fuzzyGapOpen
		if w := prev - fuzzyGapExtend; w > v {
			v = w
		}
		if w := runMax - fuzzyGapCap; w > v {
			v = w
		}
		gp[t] = v
		prev = v
	}
}

// fuzzyAlignGreedy scores the first-occurrence (leftmost) alignment:
// the fallback for names past the DP bound. Same bonus and gap model,
// single pass, not necessarily optimal.
func fuzzyAlignGreedy(pat, units []int32, bonus []int8) int32 {
	score := int32(0)
	prevMatch := -1
	q := 0
	for p := 0; p < len(units) && q < len(pat); p++ {
		if units[p] != pat[q] {
			continue
		}
		b := int32(bonus[p])
		if q > 0 {
			if prevMatch == p-1 {
				if b < fuzzyBonusConsecutive {
					b = fuzzyBonusConsecutive
				}
			} else {
				pen := fuzzyGapOpen + int32(p-prevMatch-2)*fuzzyGapExtend
				if pen > fuzzyGapCap {
					pen = fuzzyGapCap
				}
				score -= pen
			}
		}
		score += fuzzyScoreMatch + b
		prevMatch = p
		q++
	}
	return score
}

// growI32 / growI8 return the slice resized to n, reallocating only
// when the capacity is short.
func growI32(s []int32, n int) []int32 {
	if cap(s) < n {
		return make([]int32, n)
	}
	return s[:n]
}

func growI8(s []int8, n int) []int8 {
	if cap(s) < n {
		return make([]int8, n)
	}
	return s[:n]
}
