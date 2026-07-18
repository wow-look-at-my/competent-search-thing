package plugin

import (
	"context"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinSuggestID is the provider id of the bang-suggestions builtin.
const builtinSuggestID = "bangs"

// maxBangSuggestions caps one suggestions response.
const maxBangSuggestions = 12

// bangSuggestProvider answers bare/partial/ambiguous bang queries
// ("!", "!ca") with the matching bangs as virtual results. It is never
// trigger-matched or bang-targeted: Dispatch special-cases it. Each
// suggestion carries an internal set_query action that replaces the
// input with the completed bang, preserving whatever followed it
// ("!ca 2+2" suggests "!calc 2+2").
type bangSuggestProvider struct {
	builtinBase
	reg *Registry
}

func newBangSuggestProvider(reg *Registry) *bangSuggestProvider {
	return &bangSuggestProvider{
		builtinBase: builtinBase{pid: builtinSuggestID, name: "Commands"},
		reg:         reg,
	}
}

func (p *bangSuggestProvider) limit() int      { return maxBangSuggestions }
func (p *bangSuggestProvider) preRanked() bool { return true }

// candidates lists the completions in suggestion order: the list is
// query-derived routing state, so the source is preRanked -- the
// engine mints descending triggered-band scores that keep the order
// stable in the UI.
func (p *bangSuggestProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	bq, ok := p.reg.bangs.Parse(req.Query)
	if !ok {
		return nil, nil // only dispatched for bang-shaped queries
	}
	ordered := p.candidateBangs(bq.Name)
	primary := p.reg.bangs.Primary()
	out := make([]match.Candidate, 0, len(ordered))
	for _, b := range ordered {
		subtitle := ""
		if prov, ok := p.reg.byID[b.ProviderID]; ok {
			subtitle = prov.displayName()
		}
		title := primary + b.Bang
		out = append(out, match.Candidate{
			// Titles use the primary sigil; set_query keeps the sigil
			// the user actually typed.
			Display: title,
			Texts:   []string{title},
			Payload: Result{
				Title:    title,
				Subtitle: subtitle,
				Icon:     "hash",
				Action:   &Action{Type: ActionSetQuery, Value: bq.Sigil + b.Bang + " " + bq.Rest},
			},
		})
	}
	return out, nil
}

// candidateBangs lists the bangs to suggest for a typed partial name:
// the resolved bang (exact, alias, or unique prefix) first when there
// is one -- so "!calc" and alias hits surface their canonical bang on
// top -- followed by the remaining prefix candidates in sorted order.
func (p *bangSuggestProvider) candidateBangs(partial string) []BangInfo {
	cands := p.reg.bangs.Candidates(partial)
	pid, canonical, ok := p.reg.bangs.Resolve(partial)
	if !ok {
		return cands
	}
	out := make([]BangInfo, 0, len(cands)+1)
	out = append(out, BangInfo{Bang: canonical, ProviderID: pid})
	for _, c := range cands {
		if c.Bang != canonical {
			out = append(out, c)
		}
	}
	return out
}
