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
	// Token is the live-tab routing token when the companion-extension
	// bridge supplied this row (internal/ffext's "c<conn>:<tab>:<window>"
	// shape); empty for sessionstore-snapshot rows. A tokened row's
	// action SWITCHES to the existing tab (activate_tab, URL kept as
	// the fallback); a token-less row keeps the open-the-URL action.
	Token string
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
// row carrying a bridge token SWITCHES to the existing tab
// (activate_tab -- the companion extension focuses the exact tab and
// its window), while a sessionstore-snapshot row re-OPENS the URL in
// the default browser (see the README's tab-switching section).
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
		action := &Action{Type: ActionOpenURL, Value: tb.URL}
		if tb.Token != "" {
			// A live-bridge row: switch to the exact tab; Value keeps
			// the URL so the app can fall back to opening it when the
			// tab is gone or the bridge died mid-flight.
			action = &Action{Type: ActionActivateTab, Tab: tb.Token, Value: tb.URL}
		}
		res := Result{
			Title:    tabTitle(tb),
			Subtitle: tb.URL,
			Icon:     "link", // "globe" belongs to frequent-sites
			IconKey:  faviconIconKey(tb.URL),
			Action:   action,
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
