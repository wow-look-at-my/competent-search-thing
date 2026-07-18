package match

// Multi-term, multi-field combination: a term matches a candidate when
// it matches ANY of the candidate's fields (taking its best tier over
// them); the candidate matches when ALL terms match, in any order.

// FieldsResult summarizes how a term set matched one candidate's
// ordered field list.
type FieldsResult struct {
	// Tier is the candidate's tier: the WORST tier over the terms
	// (each term contributing its best tier over the fields). A
	// multi-term match is therefore TierExact/TierPrefix only when
	// every term reaches it; typically it is TierSubstring (every term
	// a substring somewhere) or TierFuzzy (>= 1 term subsequence-only).
	Tier Tier
	// WorstField is the worst best-field index over the terms: 0 when
	// every term matched the primary field, higher when some term only
	// matched a later (lower-priority) field. Orders candidates within
	// a tier (title matches beat host matches).
	WorstField int
	// Score is the summed per-term alignment score, each term scored
	// against its best field (the shared position-aware scorer).
	Score int32
	// Units is the total pattern unit count over the terms, for
	// NormalizeScore.
	Units int
}

// MatchFields matches every term against the ordered field list.
// Empty fields are skipped. ok=false when any term matches no field
// (or terms/fields are empty).
func MatchFields(fields []string, terms []Term, allowFuzzy bool) (FieldsResult, bool) {
	if len(terms) == 0 || len(fields) == 0 {
		return FieldsResult{Tier: TierNone}, false
	}
	res := FieldsResult{}
	for _, t := range terms {
		bestTier := TierNone
		bestField := -1
		for fi, f := range fields {
			if f == "" {
				continue
			}
			tier := MatchTerm(f, t, allowFuzzy)
			if tier < bestTier {
				bestTier, bestField = tier, fi
				if tier == TierExact && fi == 0 {
					break // cannot improve
				}
			}
		}
		if bestTier == TierNone {
			return FieldsResult{Tier: TierNone}, false
		}
		if bestTier > res.Tier {
			res.Tier = bestTier
		}
		if bestField > res.WorstField {
			res.WorstField = bestField
		}
		res.Score += TermScore(fields[bestField], t)
		res.Units += len(t.Units())
	}
	return res, true
}
