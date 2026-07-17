package index

// Case-folded matching over the single original-case storage. The store
// keeps exactly ONE copy of every name and dir path (original case);
// case-insensitivity is produced at scan time by folding the stored
// bytes against a pre-folded pattern, instead of storing lowercased
// twins of every blob.
//
// Two folding regimes, chosen per query by foldPattern:
//
//   - ASCII fast path (all-ASCII query): the pattern is folded byte-wise
//     through foldTable ('A'-'Z' -> lowercase, every other byte
//     identity) and matched with byte compares that fold the haystack
//     side through the same table. ciIndexASCII is the engine hot loop:
//     a rarest-byte anchor search over the contiguous name blob.
//   - Rune slow path (query with any non-ASCII byte): the pattern is
//     folded per rune with unicode.ToLower and matched rune-wise,
//     folding each haystack rune with unicode.ToLower. O(hay * pat)
//     per entry -- correct but linear-scan slow (hundreds of ms over
//     tens of millions of entries).
//
// SEMANTICS (pinned by fold_test.go, documented in CLAUDE.md): for
// all-ASCII queries against all-ASCII data this is byte-for-byte the
// old strings.ToLower behavior. Exotic Unicode edges changed with the
// storage slimming: an ASCII query no longer matches the two Unicode
// runes whose unicode.ToLower IS an ASCII letter -- U+0130 (Turkish
// dotted capital I -> 'i') and U+212A (Kelvin sign -> 'k') -- because
// the ASCII fast path folds single bytes and never decodes the stored
// UTF-8. Queries CONTAINING such runes take the rune path and still
// match both forms (a U+0130 "ISTANBUL" query finds the U+0130 name
// AND the plain-ASCII one). Invalid UTF-8 in stored names keeps the
// old semantics: each invalid byte compares as U+FFFD, so a query
// carrying U+FFFD matches it.

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// foldTable maps 'A'-'Z' to their lowercase forms and every other byte
// to itself. It is the single ASCII case-folding definition shared by
// the engine and the test reference models.
var foldTable = buildFoldTable()

func buildFoldTable() (t [256]byte) {
	for i := range t {
		t[i] = byte(i)
	}
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = byte(c) + ('a' - 'A')
	}
	return t
}

// nameByteFreq scores how common each byte is in file names (higher =
// more common). ciIndexASCII anchors its scan on the LEAST common
// pattern byte so bytes.IndexByte skips as much of the blob as
// possible between candidate verifications. Only ASCII values matter
// (patterns on this path are folded ASCII); the grades are heuristic
// and only their relative order matters -- a mediocre anchor costs
// speed, never correctness.
var nameByteFreq = buildNameByteFreq()

func buildNameByteFreq() (t [256]uint8) {
	for i := range t {
		t[i] = 15 // control bytes, high bytes, exotic punctuation: rare
	}
	grade := func(s string, score uint8) {
		for i := 0; i < len(s); i++ {
			t[s[i]] = score
		}
	}
	grade("!\"#$%&'()*+,:;<=>?@[\\]^`{|}~", 40) // legal-but-unusual punctuation
	grade("jqxz", 30)                           // the rare letters
	grade("kvwy", 70)
	grade("bfghp", 110)
	grade(" ", 140)
	grade("cdlmu", 150)
	grade("nors", 170)
	grade("0123456789", 190) // generated names are digit-heavy
	grade("aeit", 210)       // the most common letters
	grade("._-", 230)        // near-universal name separators
	// Patterns are pre-folded so only lowercase indices are consulted;
	// mirror the letters anyway so the table is safe for any caller.
	for c := byte('A'); c <= 'Z'; c++ {
		t[c] = t[c+('a'-'A')]
	}
	return t
}

// anchorOffset returns the offset of the pattern byte with the lowest
// expected name frequency (ties: the earliest). pat must be non-empty.
func anchorOffset(pat string) int {
	best := 0
	for i := 1; i < len(pat); i++ {
		if nameByteFreq[pat[i]] < nameByteFreq[pat[best]] {
			best = i
		}
	}
	return best
}

// foldRune is the rune-path folding definition: Unicode simple
// lowercase mapping, matching what strings.ToLower applies per rune.
func foldRune(r rune) rune { return unicode.ToLower(r) }

// foldPattern lowers a query into its match pattern. An all-ASCII query
// folds byte-wise through foldTable and reports ascii=true (the byte
// fast path); anything else folds per rune with foldRune (invalid UTF-8
// becomes U+FFFD, like strings.ToLower) and reports false (the rune
// slow path). The returned slice is freshly allocated and mutable.
func foldPattern(q string) (pat []byte, ascii bool) {
	for i := 0; i < len(q); i++ {
		if q[i] >= utf8.RuneSelf {
			pat = make([]byte, 0, len(q))
			for _, r := range q {
				pat = utf8.AppendRune(pat, foldRune(r))
			}
			return pat, false
		}
	}
	pat = make([]byte, len(q))
	for i := 0; i < len(q); i++ {
		pat[i] = foldTable[q[i]]
	}
	return pat, true
}

// upperVariant returns the second byte the anchor scan must look for:
// the uppercase form of a folded lowercase letter, or c itself when the
// byte has no ASCII case twin (the caller then runs a single stream).
func upperVariant(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}

// ciScan is a resumable anchor scan for one pre-folded ASCII pattern
// over one blob: the engine's hot loop. It looks for the rarest
// pattern byte's two case variants with bytes.IndexByte and verifies
// the full pattern around each candidate with a fold-compare. The two
// variants' next-hit positions PERSIST across next calls -- when the
// caller skips ahead (scanRange jumping to the end of a matched name)
// each variant stream advances monotonically instead of re-scanning,
// so a whole-blob scan costs two IndexByte passes total no matter how
// many matches it yields. (The naive restart-per-hit version was
// quadratic when one variant was absent from the blob: every restart
// re-scanned the entire remainder for it.)
type ciScan struct {
	blob []byte
	pat  string
	a    int  // anchor offset in pat
	c1   byte // folded anchor byte
	c2   byte // uppercase variant (== c1 when the byte has no twin)
	// hi is the last valid anchor index: the anchor can sit no earlier
	// than a (candidate start >= 0) and no later than hi (the
	// candidate must fit before the blob end).
	hi int
	n1 int // next c1 at/after the cursor; -1 = exhausted for good
	n2 int // next c2, same convention; -1 from the start when c2==c1
}

// newCiScan prepares a scan. pat must be non-empty pre-folded ASCII
// without NUL (foldPattern output after the caller's NUL check).
func newCiScan(blob []byte, pat string) ciScan {
	sc := ciScan{blob: blob, pat: pat, n1: -1, n2: -1}
	m := len(pat)
	if m > len(blob) {
		return sc // hi stays 0 with n1/n2 exhausted: next always -1
	}
	sc.a = anchorOffset(pat)
	sc.c1 = pat[sc.a]
	sc.c2 = upperVariant(sc.c1)
	sc.hi = len(blob) - m + sc.a
	sc.n1 = nextIndexByte(blob, sc.c1, sc.a, sc.hi)
	if sc.c2 != sc.c1 {
		sc.n2 = nextIndexByte(blob, sc.c2, sc.a, sc.hi)
	}
	return sc
}

// next returns the first fold-match starting at or after off, or -1.
// Successive calls must not decrease off.
func (sc *ciScan) next(off int) int {
	// Catch a variant's cached position up to the cursor. An exhausted
	// stream (-1) never restarts: no occurrence at/after an earlier
	// cursor means none at/after a later one.
	from := off + sc.a
	if sc.n1 >= 0 && sc.n1 < from {
		sc.n1 = nextIndexByte(sc.blob, sc.c1, from, sc.hi)
	}
	if sc.n2 >= 0 && sc.n2 < from {
		sc.n2 = nextIndexByte(sc.blob, sc.c2, from, sc.hi)
	}
	for {
		h := sc.n1
		if h < 0 || (sc.n2 >= 0 && sc.n2 < h) {
			h = sc.n2
		}
		if h < 0 {
			return -1
		}
		if start := h - sc.a; ciMatchAround(sc.blob, start, sc.pat, sc.a) {
			return start
		}
		if h == sc.n1 {
			sc.n1 = nextIndexByte(sc.blob, sc.c1, h+1, sc.hi)
		} else {
			sc.n2 = nextIndexByte(sc.blob, sc.c2, h+1, sc.hi)
		}
	}
}

// ciIndexASCII returns the index of the first fold-match of pat in
// blob, or -1: a one-shot ciScan. Callers that repeatedly skip ahead
// in one blob must hold a ciScan instead (see scanRange).
func ciIndexASCII(blob []byte, pat string) int {
	sc := newCiScan(blob, pat)
	return sc.next(0)
}

// nextIndexByte returns the smallest i in [from, hi] with blob[i] == c,
// or -1.
func nextIndexByte(blob []byte, c byte, from, hi int) int {
	if from > hi {
		return -1
	}
	if r := bytes.IndexByte(blob[from:hi+1], c); r >= 0 {
		return from + r
	}
	return -1
}

// ciIndexASCIIStr is ciIndexASCII for a string haystack (dir paths in
// the path-mode plan build), sharing anchorOffset and the verify loop.
func ciIndexASCIIStr(s, pat string) int {
	m := len(pat)
	if m > len(s) {
		return -1
	}
	a := anchorOffset(pat)
	c1 := pat[a]
	c2 := upperVariant(c1)
	hi := len(s) - m + a
	n1 := nextIndexByteStr(s, c1, a, hi)
	n2 := -1
	if c2 != c1 {
		n2 = nextIndexByteStr(s, c2, a, hi)
	}
	for {
		h := n1
		if h < 0 || (n2 >= 0 && n2 < h) {
			h = n2
		}
		if h < 0 {
			return -1
		}
		if start := h - a; ciMatchAround(s, start, pat, a) {
			return start
		}
		if h == n1 {
			n1 = nextIndexByteStr(s, c1, h+1, hi)
		} else {
			n2 = nextIndexByteStr(s, c2, h+1, hi)
		}
	}
}

// nextIndexByteStr is nextIndexByte for strings.
func nextIndexByteStr(s string, c byte, from, hi int) int {
	if from > hi {
		return -1
	}
	if r := strings.IndexByte(s[from:hi+1], c); r >= 0 {
		return from + r
	}
	return -1
}

// ciMatchAround reports whether pat fold-matches the haystack at
// start. The byte at offset skip (the anchor) is already known to
// match.
func ciMatchAround[T []byte | string](s T, start int, pat string, skip int) bool {
	for i := 0; i < len(pat); i++ {
		if i != skip && foldTable[s[start+i]] != pat[i] {
			return false
		}
	}
	return true
}

// ciContainsASCII reports whether s fold-contains the pre-folded ASCII
// pattern.
func ciContainsASCII(s, pat string) bool {
	if len(pat) == 0 {
		return true
	}
	return ciIndexASCIIStr(s, pat) >= 0
}

// ciHasPrefixASCII reports whether s fold-starts with the pre-folded
// ASCII pattern.
func ciHasPrefixASCII[T []byte | string](s T, pat string) bool {
	if len(s) < len(pat) {
		return false
	}
	for i := 0; i < len(pat); i++ {
		if foldTable[s[i]] != pat[i] {
			return false
		}
	}
	return true
}

// ciHasSuffixASCII reports whether s fold-ends with the pre-folded
// ASCII pattern.
func ciHasSuffixASCII(s, pat string) bool {
	return len(s) >= len(pat) && ciHasPrefixASCII(s[len(s)-len(pat):], pat)
}

// decodeRuneAt decodes the rune starting at byte i of s, with exact
// utf8.DecodeRune semantics for invalid encodings (RuneError, size 1).
// The multi-byte case copies at most utf8.UTFMax bytes to a stack
// buffer, so both string and []byte callers stay allocation-free.
func decodeRuneAt[T []byte | string](s T, i int) (rune, int) {
	if c := s[i]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	var buf [utf8.UTFMax]byte
	end := i + utf8.UTFMax
	if end > len(s) {
		end = len(s)
	}
	n := copy(buf[:], s[i:end])
	return utf8.DecodeRune(buf[:n])
}

// foldPrefixLen returns the byte length of the prefix of s that
// fold-matches the pre-folded pattern (foldPattern rune output), or -1
// when s does not fold-start with it. A return of len(s) therefore
// means s fold-EQUALS the pattern.
func foldPrefixLen[T []byte | string](s T, pat string) int {
	i := 0
	for j := 0; j < len(pat); {
		if i >= len(s) {
			return -1
		}
		pr, pn := decodeRuneAt(pat, j)
		sr, sn := decodeRuneAt(s, i)
		if foldRune(sr) != pr {
			return -1
		}
		i += sn
		j += pn
	}
	return i
}

// foldEquals reports whether s fold-equals the pre-folded pattern.
func foldEquals[T []byte | string](s T, pat string) bool {
	return foldPrefixLen(s, pat) == len(s)
}

// foldContains reports whether s fold-contains the pre-folded pattern
// at any rune boundary. O(len(s) * len(pat)); rune-path only.
func foldContains[T []byte | string](s T, pat string) bool {
	if len(pat) == 0 {
		return true
	}
	for i := 0; i < len(s); {
		if foldPrefixLen(s[i:], pat) >= 0 {
			return true
		}
		_, n := decodeRuneAt(s, i)
		i += n
	}
	return false
}

// foldHasSuffix reports whether s fold-ends with the pre-folded
// pattern, comparing runes backward from both ends.
func foldHasSuffix(s, pat string) bool {
	i, j := len(s), len(pat)
	for j > 0 {
		if i == 0 {
			return false
		}
		pr, pn := utf8.DecodeLastRuneInString(pat[:j])
		sr, sn := utf8.DecodeLastRuneInString(s[:i])
		if foldRune(sr) != pr {
			return false
		}
		i -= sn
		j -= pn
	}
	return true
}
