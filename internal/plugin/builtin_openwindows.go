package plugin

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// builtinWindowsID is the provider id of the open-windows search.
// (The file is builtin_openwindows.go only because a _windows.go
// suffix would be a GOOS build constraint.)
const builtinWindowsID = "windows"

// maxWindowResults caps one open-windows response.
const maxWindowResults = 8

// minWindowQueryRunes gates the all-queries matching, mirroring the
// manifest trigger's all_queries default minimum.
const minWindowQueryRunes = 2

// Open-windows ranking scores: title word-start beats an app-name
// prefix beats a title substring beats an app-name substring. They
// deliberately sit below the file index's exact/prefix tiers so a
// window row never crowds out a precise file hit.
const (
	windowScoreTitleWordStart = 85
	windowScoreAppPrefix      = 80
	windowScoreTitleSubstring = 65
	windowScoreAppSubstring   = 60
)

// WindowInfo describes one open window for the builtin Open Windows
// provider (see Options.OpenWindows). ID is the window-system
// identity AS A STRING: it rides inside activate_window actions,
// which round-trip through the frontend as JSON, and JSON numbers
// would not survive the full uint32 range unmangled. Deliberately NOT
// part of the external-plugin request context -- window lists never
// go on the wire.
type WindowInfo struct {
	ID    string
	Title string
	App   string
	PID   int
}

// windowsProvider searches the open-window titles snapshot; a result
// activates (focuses) its window via the internal-only
// activate_window action. Unlike the other builtins it has no bangs
// and is not registry-special-cased: it joins the normal trigger
// fan-out, matching every query of at least minWindowQueryRunes runes
// (an all_queries trigger).
type windowsProvider struct {
	builtinBase
	windows func() []WindowInfo
}

func newWindowsProvider(windows func() []WindowInfo) *windowsProvider {
	return &windowsProvider{
		builtinBase: builtinBase{pid: builtinWindowsID, name: "Open Windows"},
		windows:     windows,
	}
}

// match implements the all-queries trigger: any query whose trimmed
// text has at least minWindowQueryRunes runes matches, with that
// trimmed text as the search needle and no boost -- the same
// stripping Trigger.Match applies on its all_queries path.
func (p *windowsProvider) match(query string, _ *AppInfo) (string, int, bool) {
	stripped := strings.TrimSpace(query)
	if utf8.RuneCountInString(stripped) < minWindowQueryRunes {
		return "", 0, false
	}
	return stripped, 0, true
}

func (p *windowsProvider) query(_ context.Context, req Request) ([]Result, []string, error) {
	if p.windows == nil {
		return nil, nil, nil
	}
	needle := strings.ToLower(req.Stripped)
	if needle == "" {
		return nil, nil, nil
	}
	type entry struct {
		w     WindowInfo
		score float64
	}
	var matches []entry
	for _, w := range p.windows() {
		if w.Title == "" {
			continue // nothing to show (the sources skip these already)
		}
		title := strings.ToLower(w.Title)
		app := strings.ToLower(w.App)
		var score float64
		switch {
		case wordStart(title, needle):
			score = windowScoreTitleWordStart
		case strings.HasPrefix(app, needle):
			score = windowScoreAppPrefix
		case strings.Contains(title, needle):
			score = windowScoreTitleSubstring
		case strings.Contains(app, needle):
			score = windowScoreAppSubstring
		default:
			continue
		}
		matches = append(matches, entry{w: w, score: score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		ti, tj := strings.ToLower(matches[i].w.Title), strings.ToLower(matches[j].w.Title)
		if ti != tj {
			return ti < tj
		}
		return matches[i].w.Title < matches[j].w.Title
	})
	if len(matches) > maxWindowResults {
		matches = matches[:maxWindowResults]
	}
	results := make([]Result, 0, len(matches))
	for _, m := range matches {
		score := m.score
		results = append(results, Result{
			Title:    m.w.Title,
			Subtitle: m.w.App,
			Icon:     "app",
			Score:    &score,
			Action:   &Action{Type: ActionActivateWindow, Window: m.w.ID},
		})
	}
	return results, nil, nil
}

// wordStart reports whether needle occurs in haystack at a word start:
// at the very beginning, or right after a rune that is neither a
// letter nor a digit (so "main" starts a word in "app - main.go" and
// "foo-main" but not in "domain"). Both strings are expected
// pre-lowercased.
func wordStart(haystack, needle string) bool {
	for from := 0; ; {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		if i == 0 {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(haystack[:i])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
		from = i + 1
	}
}
