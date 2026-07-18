package match

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func names(rs []Ranked) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Candidate().Display
	}
	return out
}

func cand(display string, texts ...string) Candidate {
	if len(texts) == 0 {
		texts = []string{display}
	}
	return Candidate{Display: display, Texts: texts, Payload: display}
}

// TestRankFireFoxRepro is the user's bug pinned at the ranking level:
// an installed-apps snapshot containing Firefox must surface for
// "fire fox", "fox fire", and (fuzzily) "firefx", while "firefox" and
// "fire" keep their literal tiers.
func TestRankFireFoxRepro(t *testing.T) {
	cands := []Candidate{cand("Firefox"), cand("Files"), cand("GIMP")}
	for _, q := range []string{"fire fox", "fox fire", "firefx"} {
		rs := Rank(cands, RankOptions{Terms: Terms(q)})
		require.Equal(t, []string{"Firefox"}, names(rs), "query %q", q)
		require.True(t, rs[0].Minted())
		require.NotEmpty(t, rs[0].Positions(), "query %q must highlight", q)
	}

	rs := Rank(cands, RankOptions{Terms: Terms("fire")})
	require.Equal(t, []string{"Firefox"}, names(rs))
	require.Equal(t, TierPrefix, rs[0].Tier())

	rs = Rank(cands, RankOptions{Terms: Terms("firefox")})
	require.Equal(t, TierExact, rs[0].Tier())

	// Fuzzy disabled: the subsequence-only query yields nothing, the
	// multi-term substring conjunction still works.
	require.Empty(t, Rank(cands, RankOptions{Terms: Terms("firefx"), FuzzyDisabled: true}))
	rs = Rank(cands, RankOptions{Terms: Terms("fire fox"), FuzzyDisabled: true})
	require.Equal(t, []string{"Firefox"}, names(rs))
}

func TestRankTierOrderingAndBands(t *testing.T) {
	cands := []Candidate{
		cand("Campfire"),       // substring
		cand("Amazon Fire TV"), // word start
		cand("Firefox"),        // prefix
		cand("Fire"),           // exact
		cand("Frozen iZone"),   // no match ("fire" is not even a subsequence)
		cand("various-fx-item"),
	}
	rs := Rank(cands, RankOptions{Terms: Terms("fire")})
	require.Equal(t, []string{"Fire", "Firefox", "Amazon Fire TV", "Campfire"}, names(rs))

	// Band pins: descending, non-overlapping, all within 0..100.
	require.Equal(t, 83.0, rs[0].Score())
	require.Equal(t, 73.0, rs[1].Score())
	require.Equal(t, 63.0, rs[2].Score())
	require.Equal(t, 53.0, rs[3].Score())

	// The fuzzy tier lands strictly below every literal band.
	rs = Rank([]Candidate{cand("Firefox"), cand("fqiqqrqeqqqqqqfqqx")}, RankOptions{Terms: Terms("firefx")})
	require.Len(t, rs, 2)
	for _, r := range rs {
		require.Equal(t, TierFuzzy, r.Tier())
		require.Less(t, r.Score(), 53.0)
		require.GreaterOrEqual(t, r.Score(), 14.0)
	}
	require.Greater(t, rs[0].Score(), rs[1].Score(),
		"within the fuzzy tier the alignment score orders (structured beats scatter)")
	require.Equal(t, "Firefox", rs[0].Candidate().Display)
}

func TestRankFieldTieBreak(t *testing.T) {
	// Same tier via different fields: the primary-field match wins.
	a := Candidate{Display: "git dashboard", Texts: []string{"git dashboard", "chromium"}, Payload: 1}
	b := Candidate{Display: "release notes", Texts: []string{"release notes", "github-desktop"}, Payload: 2}
	rs := Rank([]Candidate{b, a}, RankOptions{Terms: Terms("git")})
	require.Equal(t, []string{"git dashboard", "release notes"}, names(rs),
		"prefix on Texts[0] outranks prefix on Texts[1]")
}

func TestRankTieBreaksAndCap(t *testing.T) {
	mk := func(name string, tie int64, key string) Candidate {
		c := cand(name)
		c.TieBreak = tie
		c.SortKey = key
		return c
	}
	rs := Rank([]Candidate{
		mk("bamboo", 1, "x"),
		mk("Igloo", 1, "x"),
		mk("voodoo", 9, "x"),
		mk("taboo", 1, "b"),
		mk("taboo", 1, "a"),
	}, RankOptions{Terms: Terms("oo")}) // all substring-tier
	require.Equal(t, []string{"voodoo", "bamboo", "Igloo", "taboo", "taboo"}, names(rs),
		"TieBreak desc, then case-folded name, then SortKey")
	require.Equal(t, "a", rs[3].Candidate().SortKey)

	rs = Rank([]Candidate{cand("xa"), cand("xb"), cand("xc")},
		RankOptions{Terms: Terms("x"), Limit: 2})
	require.Len(t, rs, 2, "Limit caps after ordering")
}

func TestRankTargetedListsAllOnEmptyQuery(t *testing.T) {
	cands := []Candidate{cand("Zeta"), cand("alpha")}
	rs := Rank(cands, RankOptions{Targeted: true})
	require.Equal(t, []string{"alpha", "Zeta"}, names(rs), "empty targeted query lists alphabetically")
	for _, r := range rs {
		require.Equal(t, ScoreListed, r.Score())
		require.Equal(t, TierTriggered, r.Tier())
		require.Empty(t, r.Positions())
	}

	// With a needle, targeted mode gates and ranks like normal text.
	rs = Rank(cands, RankOptions{Targeted: true, Terms: Terms("zet")})
	require.Equal(t, []string{"Zeta"}, names(rs))
	require.Equal(t, TierPrefix, rs[0].Tier())

	// Untargeted with no terms ranks nothing.
	require.Empty(t, Rank(cands, RankOptions{}))
}

func TestRankClaimed(t *testing.T) {
	hint := func(v float64) *float64 { return &v }
	cands := []Candidate{
		{Display: "42", Texts: []string{"42"}, Hint: hint(80), Payload: 1},
		{Display: "0x2A", Texts: []string{"0x2A"}, Hint: hint(30), Payload: 2},
		{Display: "noscore", Texts: []string{"noscore"}, Payload: 3},
	}
	rs := Rank(cands, RankOptions{Claimed: true, Terms: Terms("6*7")})
	require.Equal(t, []string{"42", "noscore", "0x2A"}, names(rs),
		"claimed results order by self-score hint (nil = neutral 50), never text-gated")
	for _, r := range rs {
		require.Equal(t, TierTriggered, r.Tier())
		require.GreaterOrEqual(t, r.Score(), 86.0)
		require.LessOrEqual(t, r.Score(), 100.0)
		require.Nil(t, r.Positions(), "no engine positions on the triggered tier")
	}
	// The triggered band sits strictly above every text band.
	text := Rank([]Candidate{cand("6*7 notes")}, RankOptions{Terms: Terms("6*7")})
	require.NotEmpty(t, text)
	require.Greater(t, rs[len(rs)-1].Score(), text[0].Score())
}

func TestRankPreRanked(t *testing.T) {
	cands := []Candidate{cand("!calc"), cand("!color"), cand("!config")}
	rs := Rank(cands, RankOptions{PreRanked: true, Limit: 2})
	require.Equal(t, []string{"!calc", "!color"}, names(rs), "given order kept, capped")
	require.Equal(t, 100.0, rs[0].Score())
	require.Equal(t, 99.0, rs[1].Score())
	require.Equal(t, TierTriggered, rs[0].Tier())

	// Long lists floor at the triggered-band base, never below.
	var many []Candidate
	for i := 0; i < 30; i++ {
		many = append(many, cand(fmt.Sprintf("row%02d", i)))
	}
	rs = Rank(many, RankOptions{PreRanked: true})
	require.Equal(t, scoreTriggeredBase, rs[len(rs)-1].Score())
}

func TestRankHintNudgeStaysWithinTier(t *testing.T) {
	hint := func(v float64) *float64 { return &v }
	lo, hi := cand("prefix-lo"), cand("prefix-hi")
	lo.Hint, hi.Hint = hint(0), hint(100)
	rs := Rank([]Candidate{lo, hi}, RankOptions{Terms: Terms("prefix")})
	require.Equal(t, []string{"prefix-hi", "prefix-lo"}, names(rs), "hint reorders within a tier")
	require.Equal(t, 75.0, rs[0].Score())
	require.Equal(t, 71.0, rs[1].Score())

	// ...but can never lift a lower tier above a higher one.
	sub := Candidate{Display: "has prefix inside", Texts: []string{"has prefix inside"}, Hint: hint(100)}
	pre := Candidate{Display: "prefix-plain", Texts: []string{"prefix-plain"}, Hint: hint(0)}
	rs = Rank([]Candidate{sub, pre}, RankOptions{Terms: Terms("prefix")})
	require.Equal(t, []string{"prefix-plain", "has prefix inside"}, names(rs))

	// Out-of-range hints clamp.
	require.Equal(t, 100.0, hintValue(hint(999)))
	require.Equal(t, 0.0, hintValue(hint(-3)))
}

func TestRankedZeroValueNotMinted(t *testing.T) {
	var r Ranked
	require.False(t, r.Minted(), "a source cannot fabricate a minted row")
}

func TestRankPayloadRoundTrip(t *testing.T) {
	type payload struct{ id int }
	c := cand("Firefox")
	c.Payload = payload{id: 7}
	rs := Rank([]Candidate{c}, RankOptions{Terms: Terms("fire")})
	require.Len(t, rs, 1)
	require.Equal(t, payload{id: 7}, rs[0].Candidate().Payload.(payload))
}
