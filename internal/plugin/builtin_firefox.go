package plugin

import (
	"context"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinFirefoxID is the provider id of the frequent-sites provider.
const builtinFirefoxID = "firefox-frequent"

// defaultFrequentSitesMax caps one frequent-sites response when the
// app supplies no config value.
const defaultFrequentSitesMax = 6

// firefoxMinQueryLen mirrors the all_queries trigger default: queries
// shorter than this (in runes, trimmed) never reach the provider.
const firefoxMinQueryLen = 2

// Scoring lives in the shared engine (canonical tier bands); the
// host-first field order below preserves the old ladder's preference.

// SiteInfo is one frequently-visited page supplied by the app layer's
// FrequentSites getter (converted from internal/firefox's Site --
// deliberately not that type, so this package stays pure; the same
// pattern as the appctx types).
type SiteInfo struct {
	URL    string
	Title  string
	Host   string
	Visits int
}

// firefoxProvider is the frequent-sites section: an all-queries
// builtin (no bangs) matching the user's frequently-visited Firefox
// pages against the query. It searches the snapshot supplied by the
// sites getter; results open their URL via an open_url action.
type firefoxProvider struct {
	builtinBase
	sites func() []SiteInfo
	max   int
}

func newFirefoxProvider(sites func() []SiteInfo, max int) *firefoxProvider {
	if max <= 0 {
		max = defaultFrequentSitesMax
	}
	return &firefoxProvider{
		builtinBase: builtinBase{pid: builtinFirefoxID, name: "Frequent Sites"},
		sites:       sites,
		max:         max,
	}
}

// match is the shared all_queries contract (min_query_len 2): every
// query of at least two runes (trimmed) reaches the provider, with no
// boost.
func (p *firefoxProvider) match(query string, _ *AppInfo) (string, int, bool) {
	return allQueriesMatch(query)
}

func (p *firefoxProvider) limit() int { return p.max }

// candidates hands the sites snapshot to the shared engine: match
// fields [host (leading "www." stripped), title, URL] -- host hits
// outrank title hits within a tier, mirroring the old ladder's
// host-first preference -- with the visit count as the tie-break.
func (p *firefoxProvider) candidates(_ context.Context, _ Request) ([]match.Candidate, error) {
	if p.sites == nil {
		return nil, nil
	}
	sites := p.sites()
	out := make([]match.Candidate, 0, len(sites))
	for _, s := range sites {
		host := strings.TrimPrefix(strings.ToLower(s.Host), "www.")
		out = append(out, match.Candidate{
			Display:  siteTitle(s),
			Texts:    []string{host, s.Title, s.URL},
			TieBreak: int64(s.Visits),
			SortKey:  s.URL,
			Payload: Result{
				Title:    siteTitle(s),
				Subtitle: s.URL,
				Icon:     "globe",
				Action:   &Action{Type: ActionOpenURL, Value: s.URL},
			},
		})
	}
	return out, nil
}

// siteTitle is the display title: the page title, or the host when
// the page never had one.
func siteTitle(s SiteInfo) string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Host
}

// Matching and scoring live in the shared engine (internal/match):
// the word-start semantics every section used to reimplement are now
// one definition there.
