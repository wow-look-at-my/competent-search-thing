package plugin

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinTabsID is the provider id of the open-tabs provider.
const builtinTabsID = "firefox-tabs"

// defaultOpenTabsMax caps one open-tabs response when the app supplies
// no config value.
const defaultOpenTabsMax = 6

// tabPinnedBadge marks pinned tabs (well under the 24-rune badge cap).
const tabPinnedBadge = "pinned"

// Scoring lives in the shared engine (canonical tier bands); the
// title-first field order below preserves the old ladder's preference
// for the tab whose TITLE matches.

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

func (p *tabsProvider) limit() int { return p.max }

// candidates hands the tabs snapshot to the shared engine: match
// fields [title, host ("www." stripped), URL] -- the TITLE outranks
// the host within a tier here, unlike frequent-sites (an already-open
// tab whose title matches is the tab the user is looking for) -- with
// last-accessed as the tie-break.
func (p *tabsProvider) candidates(_ context.Context, _ Request) ([]match.Candidate, error) {
	if p.tabs == nil {
		return nil, nil
	}
	tabs := p.tabs()
	out := make([]match.Candidate, 0, len(tabs))
	for _, tb := range tabs {
		host := strings.TrimPrefix(strings.ToLower(tb.Host), "www.")
		res := Result{
			Title:    tabTitle(tb),
			Subtitle: tb.URL,
			Icon:     "link", // "globe" belongs to frequent-sites
			Action:   &Action{Type: ActionOpenURL, Value: tb.URL},
		}
		if tb.Pinned {
			res.Badge = tabPinnedBadge
		}
		out = append(out, match.Candidate{
			Display:  tabTitle(tb),
			Texts:    []string{tb.Title, host, tb.URL},
			TieBreak: tb.LastAccessed,
			SortKey:  tb.URL,
			Payload:  res,
		})
	}
	return out, nil
}

// tabTitle is the display title: the tab title, or the host when the
// page never had one.
func tabTitle(tb TabInfo) string {
	if strings.TrimSpace(tb.Title) != "" {
		return tb.Title
	}
	return tb.Host
}

// Matching and scoring live in the shared engine (internal/match).
