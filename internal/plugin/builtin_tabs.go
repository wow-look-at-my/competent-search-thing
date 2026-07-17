package plugin

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"
)

// builtinTabsID is the provider id of the open-tabs provider.
const builtinTabsID = "firefox-tabs"

// defaultOpenTabsMax caps one open-tabs response when the app supplies
// no config value.
const defaultOpenTabsMax = 6

// tabPinnedBadge marks pinned tabs (well under the 24-rune badge cap).
const tabPinnedBadge = "pinned"

// Open-tabs score ladder, most to least specific. An already-open tab
// whose TITLE matches outranks everything (that is the tab the user is
// looking for); note the ordering differs from the frequent-sites
// ladder, where the host wins.
const (
	tabScoreTitleWord   float64 = 85
	tabScoreHostPrefix  float64 = 80
	tabScoreTitleSubstr float64 = 65
	tabScoreURLSubstr   float64 = 55
)

// TabInfo is one open Firefox tab supplied by the app layer's OpenTabs
// getter (converted from internal/firefox's Tab -- deliberately not
// that type, so this package stays pure; the SiteInfo pattern).
type TabInfo struct {
	URL   string
	Title string
	Host  string
	// Pinned marks a pinned tab (surfaced as a badge).
	Pinned bool
	// LastAccessed orders ties: milliseconds since the Unix epoch, 0
	// when unknown.
	LastAccessed int64
}

// allQueriesMatch implements the all_queries trigger contract with the
// default min_query_len of firefoxMinQueryLen, shared by the builtin
// Firefox providers: every query of at least two trimmed runes goes
// through, with no boost.
func allQueriesMatch(query string) (string, int, bool) {
	stripped := strings.TrimSpace(query)
	if utf8.RuneCountInString(stripped) < firefoxMinQueryLen {
		return "", 0, false
	}
	return stripped, 0, true
}

// tabsProvider is the open-tabs section: an all-queries builtin (no
// bangs) matching the user's currently-open Firefox tabs against the
// query. It searches the snapshot supplied by the tabs getter; a
// result's action re-OPENS the URL in the default browser (it cannot
// focus the existing tab -- see the README's honesty note).
type tabsProvider struct {
	builtinBase
	tabs func() []TabInfo
	max  int
}

func newTabsProvider(tabs func() []TabInfo, max int) *tabsProvider {
	if max <= 0 {
		max = defaultOpenTabsMax
	}
	return &tabsProvider{
		builtinBase: builtinBase{pid: builtinTabsID, name: "Open Tabs"},
		tabs:        tabs,
		max:         max,
	}
}

func (p *tabsProvider) match(query string, _ *AppInfo) (string, int, bool) {
	return allQueriesMatch(query)
}

func (p *tabsProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	needle := strings.ToLower(strings.TrimSpace(req.Stripped))
	if needle == "" || p.tabs == nil {
		return nil, nil, nil
	}
	type scored struct {
		tab   TabInfo
		score float64
	}
	var matches []scored
	for _, tb := range p.tabs() {
		score, ok := scoreTab(tb, needle)
		if !ok {
			continue
		}
		matches = append(matches, scored{tab: tb, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].tab.LastAccessed != matches[j].tab.LastAccessed {
			return matches[i].tab.LastAccessed > matches[j].tab.LastAccessed
		}
		ti, tj := strings.ToLower(tabTitle(matches[i].tab)), strings.ToLower(tabTitle(matches[j].tab))
		if ti != tj {
			return ti < tj
		}
		return matches[i].tab.URL < matches[j].tab.URL
	})
	if len(matches) > p.max {
		matches = matches[:p.max]
	}
	results := make([]Result, 0, len(matches))
	for _, m := range matches {
		score := m.score
		r := Result{
			Title:    tabTitle(m.tab),
			Subtitle: m.tab.URL,
			Icon:     "link", // "globe" belongs to frequent-sites
			Score:    &score,
			Action:   &Action{Type: ActionOpenURL, Value: m.tab.URL},
		}
		if m.tab.Pinned {
			r.Badge = tabPinnedBadge
		}
		results = append(results, r)
	}
	return results, nil, nil
}

// tabTitle is the display title: the tab title, or the host when the
// page never had one.
func tabTitle(tb TabInfo) string {
	if strings.TrimSpace(tb.Title) != "" {
		return tb.Title
	}
	return tb.Host
}

// scoreTab ranks one open tab against the lowercased needle: a title
// word-start beats a host prefix ("www." ignored), beats a title
// substring, beats a substring anywhere in the URL (which contains the
// host, so a mid-host hit lands here). No match: ok=false.
func scoreTab(tb TabInfo, needle string) (float64, bool) {
	title := strings.ToLower(tb.Title)
	host := strings.TrimPrefix(strings.ToLower(tb.Host), "www.")
	switch {
	case wordStart(title, needle):
		return tabScoreTitleWord, true
	case strings.HasPrefix(host, needle):
		return tabScoreHostPrefix, true
	case strings.Contains(title, needle):
		return tabScoreTitleSubstr, true
	case strings.Contains(strings.ToLower(tb.URL), needle):
		return tabScoreURLSubstr, true
	}
	return 0, false
}
