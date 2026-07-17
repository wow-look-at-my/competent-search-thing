package plugin

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// builtinFirefoxID is the provider id of the frequent-sites provider.
const builtinFirefoxID = "firefox-frequent"

// defaultFrequentSitesMax caps one frequent-sites response when the
// app supplies no config value.
const defaultFrequentSitesMax = 6

// firefoxMinQueryLen mirrors the all_queries trigger default: queries
// shorter than this (in runes, trimmed) never reach the provider.
const firefoxMinQueryLen = 2

// Frequent-sites score ladder, most to least specific.
const (
	siteScoreHostPrefix float64 = 95
	siteScoreTitleWord  float64 = 80
	siteScoreHostSubstr float64 = 70
	siteScoreAnySubstr  float64 = 60
)

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

// match implements the all_queries trigger contract with the default
// min_query_len of 2: every query of at least two runes (trimmed)
// reaches the provider, with no boost.
func (p *firefoxProvider) match(query string, _ *AppInfo) (string, int, bool) {
	stripped := strings.TrimSpace(query)
	if utf8.RuneCountInString(stripped) < firefoxMinQueryLen {
		return "", 0, false
	}
	return stripped, 0, true
}

func (p *firefoxProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	needle := strings.ToLower(strings.TrimSpace(req.Stripped))
	if needle == "" || p.sites == nil {
		return nil, nil, nil
	}
	type scored struct {
		site  SiteInfo
		score float64
	}
	var matches []scored
	for _, s := range p.sites() {
		score, ok := scoreSite(s, needle)
		if !ok {
			continue
		}
		matches = append(matches, scored{site: s, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].site.Visits != matches[j].site.Visits {
			return matches[i].site.Visits > matches[j].site.Visits
		}
		ti, tj := strings.ToLower(siteTitle(matches[i].site)), strings.ToLower(siteTitle(matches[j].site))
		if ti != tj {
			return ti < tj
		}
		return matches[i].site.URL < matches[j].site.URL
	})
	if len(matches) > p.max {
		matches = matches[:p.max]
	}
	results := make([]Result, 0, len(matches))
	for _, m := range matches {
		score := m.score
		results = append(results, Result{
			Title:    siteTitle(m.site),
			Subtitle: m.site.URL,
			Icon:     "globe",
			Score:    &score,
			Action:   &Action{Type: ActionOpenURL, Value: m.site.URL},
		})
	}
	return results, nil, nil
}

// siteTitle is the display title: the page title, or the host when
// the page never had one.
func siteTitle(s SiteInfo) string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Host
}

// scoreSite ranks one site against the lowercased needle: host prefix
// ("www." ignored) beats a title word-start, beats a host substring,
// beats a substring anywhere in the title or URL. No match: ok=false.
func scoreSite(s SiteInfo, needle string) (float64, bool) {
	host := strings.TrimPrefix(strings.ToLower(s.Host), "www.")
	title := strings.ToLower(s.Title)
	switch {
	case strings.HasPrefix(host, needle):
		return siteScoreHostPrefix, true
	case wordStart(title, needle):
		return siteScoreTitleWord, true
	case strings.Contains(host, needle):
		return siteScoreHostSubstr, true
	case strings.Contains(title, needle) || strings.Contains(strings.ToLower(s.URL), needle):
		return siteScoreAnySubstr, true
	}
	return 0, false
}

// wordStart reports whether needle occurs in s starting a word: at
// index 0 or right after a rune that is not a letter or digit. Both
// strings are expected lowercased.
func wordStart(s, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return false
		}
		at := from + i
		if at == 0 {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(s[:at])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
		from = at + 1
	}
}
