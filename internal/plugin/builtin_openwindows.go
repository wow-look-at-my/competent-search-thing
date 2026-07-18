package plugin

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
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

// Scoring lives in the shared engine (internal/match Rank): the
// window rows carry the canonical tier bands like every other source,
// with the title as the primary match field.

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

func (p *windowsProvider) limit() int { return maxWindowResults }

// candidates hands the window snapshot to the shared engine: match
// fields [title, app] (title hits outrank app hits within a tier),
// the window id as the deterministic tie-break key.
func (p *windowsProvider) candidates(_ context.Context, _ Request) ([]match.Candidate, error) {
	if p.windows == nil {
		return nil, nil
	}
	ws := p.windows()
	out := make([]match.Candidate, 0, len(ws))
	for _, w := range ws {
		if w.Title == "" {
			continue // nothing to show (the sources skip these already)
		}
		out = append(out, match.Candidate{
			Display: w.Title,
			Texts:   []string{w.Title, w.App},
			SortKey: w.ID,
			Payload: Result{
				Title:    w.Title,
				Subtitle: w.App,
				Icon:     "app",
				Action:   &Action{Type: ActionActivateWindow, Window: w.ID},
			},
		})
	}
	return out, nil
}
