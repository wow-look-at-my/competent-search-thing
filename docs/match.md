# internal/match

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/match` -- THE shared matching engine, pure (stdlib only),
consumed by internal/index AND internal/plugin: ONE fold definition
(FoldTable/FoldRune/FoldPattern, the per-string ASCII+rune helpers;
index re-exports them under the old names), ONE tier ladder
(TierTriggered > TierExact > TierPrefix > TierWordStart >
TierSubstring > TierFuzzy > TierNone; MatchTerm per term+target,
word = letter/digit runs), ONE multi-term semantics (Terms =
strings.Fields + per-term fold; MatchFields = every term must match
ANY of the ordered fields, candidate tier = worst per-term best
tier, WorstField = worst best-field index), ONE position-aware
scorer (score.go: the fzf-v2 constants/bonuses, DPState.Align =
DP<=MaxDPUnits else greedy, PrepareASCII/PrepareFold fill
units+bonus, TermScore one-shot, NormalizeScore for band scaling),
ONE per-character position implementation (positions.go: Range =
[2]int half-open RUNE pairs on the DISPLAY string; Positions =
union over terms of the tier-earning occurrence -- prefix start /
word-start occurrence / first substring / AlignPositions = the
backpointered DP recovering the optimal fuzzy alignment, greedy
past the bound; computed only for selected rows, never in scans),
and ONE ranking mint (rank.go: Candidate{Display, Texts, TieBreak,
SortKey, Hint, Payload} deliberately has NO score/position fields;
Ranked's fields are unexported with no constructor so only Rank can
mint; canonical wire bands triggered 86..100 (86+0.14*hint), exact
83, prefix 73, word-start 63, substring 53, ScoreListed 50, fuzzy
16..46+nudge, hint = external self-score demoted to a +/-2
intra-tier nudge; modes: PreRanked = keep order, 100-i floored at
86; Claimed = triggered tier, hint-ordered, source order on ties;
Targeted+no-terms = list all at 50; default = MatchFields gate +
sort tier/WorstField/score/TieBreak/foldedDisplay/Display/SortKey +
cap + Positions). Exhaustively unit-tested (fold parity vs
strings.ToLower, the fire/fox/firefox repro at engine level,
AlignPositions score==Align cross-check on randomized inputs).
