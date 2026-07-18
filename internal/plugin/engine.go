package plugin

// The registry's engine glue: THE single path between result sources
// and the wire. Builtin providers are candidate sources -- they return
// match.Candidate rows, which by construction carry no score and no
// positions -- and everything they emit is minted by match.Rank here.
// External plugin responses are sanitized first and then pass through
// the same engine: a plugin that CLAIMED the query (bang target or
// prefix/regex trigger) enters at the triggered tier with its
// self-score demoted to the intra-tier hint; an all-queries plugin's
// results must text-match the query (title + declared keywords) or
// they are dropped. No source -- builtin, external, or future -- can
// hand the UI a score or highlight the engine did not mint (the only
// exception: a plugin's own sanitized matchRanges win over engine
// positions, because the plugin knows its alignment best).

import (
	"context"
	"fmt"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// candidateSource is the builtin-provider shape: raw candidates in,
// nothing self-ranked out. limit caps the source's ranked section;
// preRanked marks sources whose candidate list is already
// query-derived and ordered (bang suggestions, app commands, rewrite
// rules) -- the engine mints those in order at the triggered band.
type candidateSource interface {
	provider
	candidates(ctx context.Context, req Request) ([]match.Candidate, error)
	limit() int
	preRanked() bool
}

// resultProvider is the external-plugin shape (wire results +
// sanitizer reasons). Only *externalProvider implements it in
// production; the routing test enforces that every registered
// provider is one of the two shapes.
type resultProvider interface {
	provider
	query(ctx context.Context, req Request) ([]Result, []string, error)
}

// sourceResults runs one candidate source through the engine: the
// single choke point for builtin results.
func sourceResults(src candidateSource, ctx context.Context, req Request, fuzzyDisabled bool) ([]Result, error) {
	cands, err := src.candidates(ctx, req)
	if err != nil || len(cands) == 0 {
		return nil, err
	}
	ranked := match.Rank(cands, match.RankOptions{
		Terms:         match.Terms(req.Stripped),
		FuzzyDisabled: fuzzyDisabled,
		Limit:         src.limit(),
		Targeted:      req.Targeted,
		PreRanked:     src.preRanked(),
	})
	return mintResults(ranked, false), nil
}

// rankExternal passes a sanitized external response through the
// engine. claimed selects the triggered tier (no text gating); the
// normal path text-matches every result against its title + keywords
// and DROPS misses (reported in dropped for throttled logging).
func rankExternal(results []Result, req Request, claimed, fuzzyDisabled bool) ([]Result, []string) {
	if len(results) == 0 {
		return nil, nil
	}
	cands := make([]match.Candidate, 0, len(results))
	for _, res := range results {
		texts := make([]string, 0, 1+len(res.Keywords))
		texts = append(texts, res.Title)
		texts = append(texts, res.Keywords...)
		cands = append(cands, match.Candidate{
			Display: res.Title,
			Texts:   texts,
			Hint:    res.Score, // sanitizer guarantees non-nil, 0..100
			SortKey: res.Subtitle,
			Payload: res,
		})
	}
	opts := match.RankOptions{
		Terms:         match.Terms(req.Stripped),
		FuzzyDisabled: fuzzyDisabled,
		Limit:         maxResultsPerResponse,
		Claimed:       claimed,
	}
	ranked := match.Rank(cands, opts)
	var dropped []string
	if !claimed && len(ranked) < len(cands) {
		dropped = append(dropped, fmt.Sprintf(
			"engine: %d result(s) matched no query term in their title/keywords; dropped (claim the query with a prefix/regex trigger or a bang for untethered results)",
			len(cands)-len(ranked)))
	}
	return mintResults(ranked, true), dropped
}

// mintResults converts engine-ranked candidates whose Payload is a
// Result into wire rows: the engine's score is stamped
// unconditionally (whatever a source wrote in the payload's Score is
// overwritten), and the engine's positions become matchRanges --
// except that a plugin's own sanitized matchRanges are kept when
// keepPluginRanges is set and the engine computed none (triggered
// tier) or the plugin supplied its own.
func mintResults(ranked []match.Ranked, keepPluginRanges bool) []Result {
	out := make([]Result, 0, len(ranked))
	for _, rk := range ranked {
		res, ok := rk.Candidate().Payload.(Result)
		if !ok {
			continue // sources always mint Result payloads; drop garbage
		}
		s := rk.Score()
		res.Score = &s
		if keepPluginRanges && len(res.MatchRanges) > 0 {
			// The plugin's own (sanitized) alignment wins.
		} else {
			res.MatchRanges = rk.Positions()
		}
		out = append(out, res)
	}
	return out
}
