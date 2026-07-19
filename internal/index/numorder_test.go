package index

import (
	"math/rand"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/frecency"
)

// cmpPaths runs compareJoinedNumeric over two full paths split the way
// the store stores them (parent dir + name), so the unit tests below
// exercise the exact production entry point.
func cmpPaths(dirA, nameA, dirB, nameB string) int {
	return compareJoinedNumeric(dirA, []byte(nameA), dirB, []byte(nameB))
}

func TestCompareJoinedNumeric(t *testing.T) {
	cases := []struct {
		name                     string
		dirA, nameA, dirB, nameB string
		want                     int // sign only
	}{
		{"newest datestamp first",
			"/s", "Screenshot 2026-07-18 at 09.12.44.png",
			"/s", "Screenshot 2024-02-01 at 10.10.11.png", -1},
		{"higher version first",
			"/d", "invoice_v2.pdf", "/d", "invoice_v1.pdf", -1},
		{"higher version first (reversed args)",
			"/d", "invoice_v1.pdf", "/d", "invoice_v2.pdf", 1},
		{"non-digit difference keeps byte order",
			"/d", "report_alpha.txt", "/d", "report_betaq.txt", -1},
		{"equal numbers fall through to later bytes",
			"/d", "v1_alpha.txt", "/d", "v1_betaq.txt", -1},
		{"digit vs non-digit keeps byte order",
			"/d", "log1x.txt", "/d", "logaa.txt", -1},
		{"later runs decide when earlier runs tie",
			"/d", "img_2026_01.png", "/d", "img_2026_09.png", 1},
		{"unequal-length aligned runs compare numerically",
			"/d", "a12b3", "/d", "a1b23", -1}, // 12 > 1 at the first run
		{"leading zeros equal numerically, then plain order",
			"/d", "a01b2", "/d", "a1b02", -1}, // runs tie (1, 2): fallback a01... < a1...
		{"multi-run date wins on the month",
			"/d", "2026-09-01.log", "/d", "2026-03-15.log", -1},
		{"dirs participate too",
			"/backup/2026", "data.bin", "/backup/2024", "data.bin", -1},
		{"identical paths compare equal",
			"/d", "same_1.txt", "/d", "same_1.txt", 0},
		{"proper prefix still sorts shorter first",
			"/d", "abc", "/d", "abcd", -1},
	}
	for _, tc := range cases {
		got := cmpPaths(tc.dirA, tc.nameA, tc.dirB, tc.nameB)
		switch {
		case tc.want < 0:
			require.Negative(t, got, tc.name)
		case tc.want > 0:
			require.Positive(t, got, tc.name)
		default:
			require.Zero(t, got, tc.name)
		}
		// Antisymmetry on every case.
		back := cmpPaths(tc.dirB, tc.nameB, tc.dirA, tc.nameA)
		require.Equal(t, -sign(got), sign(back), "%s: antisymmetry", tc.name)
	}
}

func sign(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}

// TestCompareJoinedNumericTotalOrder sorts a tricky corpus (digit
// runs, leading zeros, mixed digit/letter boundaries, the historical
// a1x/a1y/a2x non-transitivity trap of a naive skeleton-gated rule)
// with the comparator and verifies pairwise consistency of the sorted
// order -- the property that keeps the shard heaps and sorts sound.
func TestCompareJoinedNumericTotalOrder(t *testing.T) {
	names := []string{
		"a1x", "a1y", "a2x", "a2y", "a10", "a01", "a1b", "ab1",
		"v07_x", "v7_ax", "v70_a", "z9_zz", "z10zz",
		"n1", "n2", "n9", "0aa", "9aa", ":aa", "_aa",
	}
	// A deterministic shuffle plus random digit-heavy extras.
	rng := rand.New(rand.NewSource(20260719))
	for i := 0; i < 40; i++ {
		names = append(names, "f"+strconv.Itoa(rng.Intn(30))+"-"+strconv.Itoa(rng.Intn(30)))
	}
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return cmpPaths("/d", sorted[i], "/d", sorted[j]) < 0
	})
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			c := cmpPaths("/d", sorted[i], "/d", sorted[j])
			require.LessOrEqual(t, c, 0,
				"sorted order inconsistent: %q vs %q", sorted[i], sorted[j])
		}
	}
}

// TestNumericTieBreakNameMode pins the user-visible fix in plain name
// mode: a datestamped screenshot family delivers newest first, a
// versioned family delivers the highest version first, an ordinary
// non-datestamp pair keeps plain lexicographic order, and a pair whose
// numbers tie keeps the pre-change order (stability).
func TestNumericTieBreakNameMode(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, "/shots", "Screenshot 2024-02-01 at 10.10.11.png", false)
	mustAdd(t, s, "/shots", "Screenshot 2026-07-18 at 09.12.44.png", false)
	mustAdd(t, s, "/shots", "Screenshot 2025-06-15 at 23.59.59.png", false)
	require.Equal(t, []string{
		"/shots/Screenshot 2026-07-18 at 09.12.44.png",
		"/shots/Screenshot 2025-06-15 at 23.59.59.png",
		"/shots/Screenshot 2024-02-01 at 10.10.11.png",
	}, resultPaths(s.Query("screenshot", 10)), "datestamped family: newest first")

	mustAdd(t, s, "/docs", "invoice_v1.pdf", false)
	mustAdd(t, s, "/docs", "invoice_v2.pdf", false)
	require.Equal(t, []string{"/docs/invoice_v2.pdf", "/docs/invoice_v1.pdf"},
		resultPaths(s.Query("invoice", 10)), "versioned family: highest first")

	mustAdd(t, s, "/n", "report_alpha.txt", false)
	mustAdd(t, s, "/n", "report_betaq.txt", false)
	require.Equal(t, []string{"/n/report_alpha.txt", "/n/report_betaq.txt"},
		resultPaths(s.Query("report", 10)), "non-datestamp pair: plain lexicographic untouched")

	mustAdd(t, s, "/eq", "v1_alpha.txt", false)
	mustAdd(t, s, "/eq", "v1_betaq.txt", false)
	require.Equal(t, []string{"/eq/v1_alpha.txt", "/eq/v1_betaq.txt"},
		resultPaths(s.Query("v1", 10)), "same-number pair: stable, existing order")
}

// TestNumericTieBreakSelection: the tie-break also decides WHICH rows
// survive the delivery cut when a family is larger than the limit --
// the newest members are the ones delivered.
func TestNumericTieBreakSelection(t *testing.T) {
	s := NewStore()
	for _, y := range []string{"2021", "2024", "2019", "2026", "2023"} {
		mustAdd(t, s, "/shots", "shot "+y+".png", false)
	}
	require.Equal(t, []string{"/shots/shot 2026.png", "/shots/shot 2024.png"},
		resultPaths(s.Query("shot", 2)), "the newest two win the cut")
}

// TestNumericTieBreakOtherModes covers the remaining query modes that
// share the comparator chain: fuzzy, multi-term, path mode, and the
// blend-active tail.
func TestNumericTieBreakOtherModes(t *testing.T) {
	s := NewStore()
	// Fuzzy: "rpt" is a subsequence (never a substring) of both names,
	// with identical alignments -- only the digits differ.
	mustAdd(t, s, "/f", "r_p_t_1.txt", false)
	mustAdd(t, s, "/f", "r_p_t_2.txt", false)
	require.Equal(t, []string{"/f/r_p_t_2.txt", "/f/r_p_t_1.txt"},
		resultPaths(s.Query("rpt", 10)), "fuzzy mode")

	// Multi-term: both terms substring-match both names (classSub).
	mustAdd(t, s, "/m", "report v1.txt", false)
	mustAdd(t, s, "/m", "report v2.txt", false)
	require.Equal(t, []string{"/m/report v2.txt", "/m/report v1.txt"},
		resultPaths(s.Query("report v", 10)), "multi-term mode")

	// Path mode: the query carries a separator; both full paths hold
	// it as a substring with equal class and length.
	mustAdd(t, s, "/p", "img_1.png", false)
	mustAdd(t, s, "/p", "img_2.png", false)
	require.Equal(t, []string{"/p/img_2.png", "/p/img_1.png"},
		resultPaths(s.Query("p/img", 10)), "path mode")

	// Blend-active tail: an active-but-silent blend orders through the
	// same candCompare chain, numeric tie-break included.
	now := time.Now()
	b := &Blend{
		Signals: frecency.Signals{Store: blendTestStore(t, now, nil)},
		Now:     func() time.Time { return now },
	}
	require.True(t, b.active())
	require.Equal(t, []string{"/m/report v2.txt", "/m/report v1.txt"},
		resultPaths(s.QueryWith("report v", 10, QueryOptions{Blend: b})), "blend tail")
}
