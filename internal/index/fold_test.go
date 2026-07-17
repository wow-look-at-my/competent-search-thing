package index

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// testFold lowers s the way the engine folds it against a pattern of
// the given regime: byte-wise through foldTable for ASCII patterns,
// per-rune unicode.ToLower otherwise. The naive reference models build
// on this so engine and reference share ONE fold definition (foldTable
// + foldRune) while keeping the scan machinery independent (stdlib
// strings operations over folded copies).
func testFold(s string, ascii bool) string {
	if ascii {
		b := []byte(s)
		for i := range b {
			b[i] = foldTable[b[i]]
		}
		return string(b)
	}
	return strings.Map(foldRune, s)
}

func TestFoldTableExhaustive(t *testing.T) {
	for i := 0; i < 256; i++ {
		b := byte(i)
		want := b
		if b >= 'A' && b <= 'Z' {
			want = b + ('a' - 'A')
		}
		require.Equal(t, want, foldTable[b], "byte 0x%02x", b)
	}
}

func TestFoldPattern(t *testing.T) {
	cases := []struct {
		q     string
		pat   string
		ascii bool
	}{
		{"", "", true},
		{"abc", "abc", true},
		{"ABC", "abc", true},
		{"Read.Me_09-X", "read.me_09-x", true},
		{"a\x00b", "a\x00b", true},
		// Non-ASCII queries fold per rune; a fold that lands in ASCII
		// (U+0130 -> i, U+212A -> k) still stays on the rune path.
		{"\u0130", "i", false},
		{"\u0130STANBUL", "istanbul", false},
		{"\u212a", "k", false},
		{"M\u00dcsic", "m\u00fcsic", false},
		{"Stra\u1e9eE", "stra\u00dfe", false},
		// Invalid UTF-8 folds to U+FFFD, matching strings.ToLower.
		{"a\xffb", "a\ufffdb", false},
	}
	for _, tc := range cases {
		pat, ascii := foldPattern(tc.q)
		require.Equal(t, tc.pat, string(pat), "query %q", tc.q)
		require.Equal(t, tc.ascii, ascii, "query %q regime", tc.q)
	}
}

func TestAnchorOffset(t *testing.T) {
	cases := []struct {
		pat  string
		want int
	}{
		{"x", 0},
		{"data", 0},   // 'd' is the least common of d/a/t/a
		{"aqua", 1},   // 'q' beats the vowels
		{"re", 0},     // 'r' scores under 'e'
		{"a.b", 2},    // '.' is near-universal; 'b' anchors
		{"e0", 1},     // digits score under the top vowels
		{"zz", 0},     // ties keep the earliest
		{"zzqx", 0},   // all rare: earliest wins
		{"images", 3}, // 'g' is the rarest here
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, anchorOffset(tc.pat), "pattern %q", tc.pat)
	}
}

func TestCiIndexASCIIExplicit(t *testing.T) {
	cases := []struct {
		blob string
		pat  string
		want int
	}{
		{"Hello World", "world", 6},
		{"Hello World", "hello", 0},
		{"Hello World", "o w", 4},
		{"xaqz", "aq", 1},        // anchor 'q' sits at pattern offset 1
		{"qrs", "aq", -1},        // anchor hit would start before byte 0
		{"TESTQ", "tq", 3},       // uppercase variant of the anchor byte
		{"zaza zebra", "zeb", 5}, // false anchor candidates skipped
		{"abc", "abcd", -1},      // pattern longer than the blob
		{"", "a", -1},
		{"A", "a", 0},
		{"ab\x00cd", "bc", -1}, // never matches across the NUL separator
		{"ab\x00cd", "cd", 3},
		{"data_DATA", "data", 0},
		{"xDATAx", "data", 1},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, ciIndexASCII([]byte(tc.blob), tc.pat),
			"ciIndexASCII(%q, %q)", tc.blob, tc.pat)
		require.Equal(t, tc.want, ciIndexASCIIStr(tc.blob, tc.pat),
			"ciIndexASCIIStr(%q, %q)", tc.blob, tc.pat)
	}
}

// TestCiIndexASCIIMatchesReference cross-checks the anchor search
// against bytes.Index over a byte-wise folded copy, which must agree
// on the exact index (ASCII folding preserves byte positions). Blobs
// include NUL separators, mixed case, digits, and non-ASCII bytes.
func TestCiIndexASCIIMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []byte("aAbBeEzZqQxX019 ._-\x00\x00\xc3\xb1\xff")
	patAlphabet := []byte("abezqx01._- ")
	for iter := 0; iter < 5000; iter++ {
		blob := make([]byte, rng.Intn(120))
		for i := range blob {
			blob[i] = alphabet[rng.Intn(len(alphabet))]
		}
		pat := make([]byte, 1+rng.Intn(5))
		for i := range pat {
			pat[i] = patAlphabet[rng.Intn(len(patAlphabet))]
		}
		want := bytes.Index([]byte(testFold(string(blob), true)), pat)
		require.Equal(t, want, ciIndexASCII(blob, string(pat)),
			"blob %q pat %q", blob, pat)
		require.Equal(t, want, ciIndexASCIIStr(string(blob), string(pat)),
			"str variant, blob %q pat %q", blob, pat)
	}
}

// TestCiScanResume drives the resumable scanner the way scanRange does
// (monotonically increasing skip-ahead offsets) and cross-checks every
// reported position against a fresh one-shot scan. The first alphabet
// has NO uppercase at all -- the case that made a restart-per-hit
// design quadratic (each restart re-scanned the whole remainder for
// the absent upper variant) and must stay cheap for the cached scan.
func TestCiScanResume(t *testing.T) {
	rng := rand.New(rand.NewSource(43))
	alphabets := [][]byte{
		[]byte("ab z01._\x00"),
		[]byte("aAbBzZ01._\x00\x00\xc3"),
	}
	pats := []string{"a", "az", "z1", "ab", "b"}
	for _, alphabet := range alphabets {
		for iter := 0; iter < 500; iter++ {
			blob := make([]byte, rng.Intn(400))
			for i := range blob {
				blob[i] = alphabet[rng.Intn(len(alphabet))]
			}
			pat := pats[rng.Intn(len(pats))]
			sc := newCiScan(blob, pat)
			off := 0
			for off <= len(blob) {
				want := ciIndexASCII(blob[off:], pat)
				got := sc.next(off)
				if want < 0 {
					require.Equal(t, -1, got, "blob %q pat %q off %d", blob, pat, off)
					break
				}
				require.Equal(t, off+want, got, "blob %q pat %q off %d", blob, pat, off)
				// Resume anywhere past the match start, like scanRange
				// skipping to the end of the matched entry.
				off = got + 1 + rng.Intn(4)
			}
		}
	}
}

func TestCiPrefixSuffixContains(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	alphabet := []byte("aAbBzZ01._\xc3\xb1")
	patAlphabet := []byte("abz01._")
	for iter := 0; iter < 3000; iter++ {
		s := make([]byte, rng.Intn(30))
		for i := range s {
			s[i] = alphabet[rng.Intn(len(alphabet))]
		}
		pat := make([]byte, rng.Intn(5))
		for i := range pat {
			pat[i] = patAlphabet[rng.Intn(len(patAlphabet))]
		}
		str, ps := string(s), string(pat)
		folded := testFold(str, true)
		require.Equal(t, strings.HasPrefix(folded, ps), ciHasPrefixASCII(s, ps), "prefix %q %q", s, ps)
		require.Equal(t, strings.HasPrefix(folded, ps), ciHasPrefixASCII(str, ps), "prefix str %q %q", s, ps)
		require.Equal(t, strings.HasSuffix(folded, ps), ciHasSuffixASCII(str, ps), "suffix %q %q", s, ps)
		require.Equal(t, strings.Contains(folded, ps), ciContainsASCII(str, ps), "contains %q %q", s, ps)
	}
}

// TestDecodeRuneAtParity pins decodeRuneAt to exact utf8.DecodeRune
// semantics at every position of valid, invalid, truncated, overlong,
// and surrogate inputs, through both generic instantiations.
func TestDecodeRuneAtParity(t *testing.T) {
	inputs := []string{
		"plain ascii",
		"M\u00fcsic/S\u00f6ng.mp3",
		"\u0130stanbul \u212a \u00df \u1e9e \u017f",
		"boundaries \u0080\u07ff\u0800\uffff\U00010000\U0010ffff",
		"invalid \xff\xfe standalone",
		"truncated \xc3",
		"truncated \xe2\x84",
		"truncated \xf0\x90\x8d",
		"overlong \xc0\x80 pair",
		"surrogate \xed\xa0\x80 half",
		"out of range \xf5\x80\x80\x80",
		"continuations \x80\xbf\x80",
	}
	// Random byte soup for good measure.
	rng := rand.New(rand.NewSource(23))
	for i := 0; i < 50; i++ {
		b := make([]byte, 1+rng.Intn(40))
		rng.Read(b)
		inputs = append(inputs, string(b))
	}
	for _, in := range inputs {
		b := []byte(in)
		for i := 0; i < len(in); i++ {
			wantR, wantN := utf8.DecodeRune(b[i:])
			gotR, gotN := decodeRuneAt(b, i)
			require.Equal(t, wantR, gotR, "rune at %d of %q ([]byte)", i, in)
			require.Equal(t, wantN, gotN, "size at %d of %q ([]byte)", i, in)
			wantR, wantN = utf8.DecodeRuneInString(in[i:])
			gotR, gotN = decodeRuneAt(in, i)
			require.Equal(t, wantR, gotR, "rune at %d of %q (string)", i, in)
			require.Equal(t, wantN, gotN, "size at %d of %q (string)", i, in)
		}
	}
}

// foldPat runs foldPattern and asserts the query takes the rune path.
func foldPat(t *testing.T, q string) string {
	t.Helper()
	pat, ascii := foldPattern(q)
	require.False(t, ascii, "query %q was expected to take the rune path", q)
	return string(pat)
}

func TestFoldPrefixLen(t *testing.T) {
	cases := []struct {
		s    string
		q    string // raw query, prepared through foldPattern
		want int
	}{
		{"\u0130stanbul", "\u0130STANBUL", 9},
		{"\u0130stanbul.txt", "\u0130stanbul", 9},
		// The 3-byte Kelvin sign fold-matches the 1-byte 'k' the
		// pattern fold produced from it, and plain ASCII k does too.
		{"\u212aelvin", "\u212aelvin", 8},
		{"kelvin", "\u212aelvin", 6},
		{"Stra\u1e9eE", "stra\u00dfe", 8},
		{"stra\u00dfe", "Stra\u1e9eE", 7},
		{"\u00c9clair.txt", "\u00e9clair", 7},
		{"plain", "\u00e9", -1},
		{"", "\u00e9", -1},
		{"\u00e9", "\u00c9clair", -1}, // s exhausted before the pattern
	}
	for _, tc := range cases {
		pat := foldPat(t, tc.q)
		require.Equal(t, tc.want, foldPrefixLen(tc.s, pat), "foldPrefixLen(%q, %q)", tc.s, pat)
		require.Equal(t, tc.want, foldPrefixLen([]byte(tc.s), pat), "[]byte variant, foldPrefixLen(%q, %q)", tc.s, pat)
		require.Equal(t, tc.want == len(tc.s), foldEquals(tc.s, pat), "foldEquals(%q, %q)", tc.s, pat)
	}
}

func TestFoldContainsAndSuffix(t *testing.T) {
	contains := []struct {
		s    string
		q    string
		want bool
	}{
		{"abc\u00c4hnlich.txt", "\u00e4hnl", true},
		{"\u00c4hnlich.txt", "\u00e4hnlich.txt", true},
		{"nothing here", "\u00e4", false},
		{"M\u00fcsic", "M\u00dcSIC", true},
		{"\u0130stanbul", "\u0130stanbul", true},
	}
	for _, tc := range contains {
		pat := foldPat(t, tc.q)
		require.Equal(t, tc.want, foldContains(tc.s, pat), "foldContains(%q, %q)", tc.s, pat)
		require.Equal(t, tc.want, foldContains([]byte(tc.s), pat), "[]byte foldContains(%q, %q)", tc.s, pat)
	}

	suffix := []struct {
		s    string
		q    string
		want bool
	}{
		{"/Data/M\u00fcsic", "M\u00dcSIC", true},
		{"/Data/M\u00fcsic", "/data/m\u00fcsic", true},
		{"/Data/M\u00fcsic", "x/data/m\u00fcsic", false},
		{"M\u00fcsic", "s\u00f6ng", false},
		{"Stra\u1e9eE", "a\u00dfe", true},
		{"", "\u00e4", false},
	}
	for _, tc := range suffix {
		pat := foldPat(t, tc.q)
		require.Equal(t, tc.want, foldHasSuffix(tc.s, pat), "foldHasSuffix(%q, %q)", tc.s, pat)
	}
}

// TestFoldRuneVsStringsToLowerParity pins the shared fold definition:
// matching folded byte forms (strings.Map of foldRune, what the naive
// models use) agrees with the engine's rune-wise compare on randomized
// unicode-heavy strings.
func TestFoldRuneVsStringsToLowerParity(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	runes := []rune{
		'a', 'B', 'z', '0', '.', ' ',
		'\u00c4', '\u00e4', '\u00dc', '\u00fc', '\u00df', '\u1e9e',
		'\u0130', '\u0131', '\u212a', '\u017f', '\u00c9', '\u00e9',
		'\U0001f600', // an astral rune for 4-byte coverage
	}
	randStr := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteRune(runes[rng.Intn(len(runes))])
		}
		return b.String()
	}
	for iter := 0; iter < 4000; iter++ {
		s := randStr(rng.Intn(10))
		q := randStr(1 + rng.Intn(4))
		pat, ascii := foldPattern(q)
		if ascii {
			continue // rune-path parity is the point here
		}
		ps := string(pat)
		folded := testFold(s, false)
		require.Equal(t, strings.HasPrefix(folded, ps), foldPrefixLen(s, ps) >= 0,
			"prefix parity, s %q q %q", s, q)
		require.Equal(t, strings.Contains(folded, ps), foldContains(s, ps),
			"contains parity, s %q q %q", s, q)
		require.Equal(t, strings.HasSuffix(folded, ps), foldHasSuffix(s, ps),
			"suffix parity, s %q q %q", s, q)
		require.Equal(t, folded == ps, foldEquals(s, ps),
			"equality parity, s %q q %q", s, q)
	}
}

// TestFoldSemanticsPins fixes the deliberate behavior change from the
// stored-lowercase era (see fold.go): the ASCII fast path never decodes
// stored UTF-8, so the two runes whose unicode.ToLower IS an ASCII
// letter (U+0130 -> i, U+212A -> k) are no longer found by plain ASCII
// queries -- while queries containing them still find both forms.
func TestFoldSemanticsPins(t *testing.T) {
	s := NewStore()
	ist := mustAdd(t, s, "/u", "\u0130stanbul.txt", false)
	kel := mustAdd(t, s, "/u", "\u212aelvin.txt", false)
	plainKel := mustAdd(t, s, "/u", "kelvin.txt", false)

	// ASCII queries stay on the byte path: no match on the exotic runes.
	require.Empty(t, s.Query("istanbul", 10),
		"ASCII fast path must not fold U+0130 to 'i'")
	res := s.Query("kelvin", 10)
	require.Len(t, res, 1, "ASCII fast path must not fold U+212A to 'k'")
	require.Equal(t, s.EntryPath(plainKel), res[0].Path)

	// Queries carrying the exotic runes take the rune path and find
	// both spellings.
	res = s.Query("\u0130STANBUL", 10)
	require.Len(t, res, 1)
	require.Equal(t, s.EntryPath(ist), res[0].Path)

	res = s.Query("\u212aelvin", 10)
	require.Len(t, res, 2)
	require.ElementsMatch(t,
		[]string{s.EntryPath(kel), s.EntryPath(plainKel)},
		[]string{res[0].Path, res[1].Path})

	// U+017F (long s) is already lowercase; it never matched ASCII "s"
	// before and still does not.
	mustAdd(t, s, "/u", "\u017foo", false)
	require.Empty(t, s.Query("soo", 10))
}
