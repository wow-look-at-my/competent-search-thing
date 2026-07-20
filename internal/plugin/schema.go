package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProtocolVersion is the JSON wire protocol version this package
// speaks. Requests are stamped with it; a response declaring any other
// version is rejected wholesale (a missing "v" means version 1).
const ProtocolVersion = 1

// DefaultScore is assigned to results that do not declare a score.
const DefaultScore float64 = 50

// Request is the JSON payload sent to a plugin for one query: written
// to stdin for command plugins, POSTed as the body for HTTP plugins.
//
// Dispatchers should populate Settings with the plugin's settings
// object from config.json (or the literal "{}" when there is none) so
// plugins always see a JSON object.
type Request struct {
	V        int             `json:"v"`
	Query    string          `json:"query"`
	Stripped string          `json:"stripped"`
	Gen      int64           `json:"gen"`
	Targeted bool            `json:"targeted"`
	Bang     string          `json:"bang"`
	Settings json.RawMessage `json:"settings"`
	Context  *RequestContext `json:"context,omitempty"`
}

// RequestContext carries the app-context parts a plugin declared in
// its manifest. Undeclared parts are omitted; when a plugin declares
// nothing, the whole "context" field is absent from the request.
type RequestContext struct {
	FocusedApp    *AppInfo       `json:"focused_app,omitempty"`
	RunningApps   []AppInfo      `json:"running_apps,omitempty"`
	InstalledApps []InstalledApp `json:"installed_apps,omitempty"`
}

// AppInfo describes one running (or the focused) application.
type AppInfo struct {
	Name  string `json:"name"`
	Exe   string `json:"exe"`
	Title string `json:"title"`
	PID   int    `json:"pid"`
}

// InstalledApp describes one installed application. Icon is the
// platform icon ref (a .desktop Icon= value on linux, the absolute
// .app bundle path on darwin, empty on windows) the builtin app
// sources turn into a Result.IconKey; plugins receiving it in their
// request context may ignore it.
type InstalledApp struct {
	Name string `json:"name"`
	Exec string `json:"exec"`
	ID   string `json:"id"`
	Icon string `json:"icon,omitempty"`
}

// Response is a plugin's reply to a Request. A zero V is treated as
// version 1.
type Response struct {
	V       int      `json:"v"`
	Results []Result `json:"results"`
}

// Result is one virtual search result. Score uses a pointer so that
// "absent" is detectable: SanitizeResponse fills absent scores with
// DefaultScore and clamps the rest to 0..100 -- and the ENGINE
// (internal/match via the registry) then overwrites it with the
// canonical tier band on every emitted row: a plugin's self-score is
// only ever an intra-tier hint, never the wire score.
type Result struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Icon     string `json:"icon,omitempty"`
	// IconKey is INTERNAL-ONLY (trusted builtin sources): an icon
	// resolution key ("app:<ref>") the frontend hands to the bound
	// ResolveIcons method for a real icon image, keeping Icon's glyph
	// while it resolves (or when it misses). SanitizeResponse clears
	// it on every external result -- image-icon resolution is a
	// trusted-source capability, external plugins keep the
	// builtin-name/glyph icon contract.
	IconKey     string   `json:"iconKey,omitempty"`
	Badge       string   `json:"badge,omitempty"`
	AccentColor string   `json:"accent_color,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	Fields      []Field  `json:"fields,omitempty"`
	Action      *Action  `json:"action,omitempty"`
	// Keywords are extra match texts for the engine's text gating:
	// query terms match a result when they match its title OR any
	// keyword. All-queries plugins should fill these with whatever
	// their result should be findable by; triggered (prefix/regex/
	// bang) plugins do not need them.
	Keywords []string `json:"keywords,omitempty"`
	// MatchRanges are per-character highlight ranges on Title:
	// half-open [start, end) RUNE index pairs. Optional on the wire --
	// absent means the engine computes them for text-matched results
	// (and none for the triggered tier); a plugin doing its own
	// matching may supply them and they win over engine positions.
	MatchRanges [][2]int `json:"matchRanges,omitempty"`
}

// Field is one label/value detail line on a result.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Action describes what activating a result does. Value carries the
// payload for open_path/open_url/copy_text/set_query/run_builtin --
// and, for activate_tab, the tab's URL (the open-the-URL fallback when
// the live tab cannot be reached); Argv carries the command line for
// run_command; Window carries the window id (a decimal string, so it
// survives JSON round-trips unmangled) for activate_window. Tab is
// internal-only (the builtin open-tabs provider sets it on
// activate_tab actions): the live-tab routing token
// ("c<conn>:<tab>:<window>", see internal/ffext), re-validated by the
// app before any activation; SanitizeResponse clears it on every
// external action. DesktopID is internal-only too (the builtin app
// launchers set it alongside a run_command Argv): the .desktop entry
// behind the launch, which the app resolves for launch capabilities
// (D-Bus activation, startup notification) so launching an
// already-running single-instance app focuses its window.
type Action struct {
	Type      string   `json:"type"`
	Value     string   `json:"value,omitempty"`
	Argv      []string `json:"argv,omitempty"`
	Window    string   `json:"window,omitempty"`
	Tab       string   `json:"tab,omitempty"`
	DesktopID string   `json:"desktop_id,omitempty"`
}

// Action types. The first four may be returned by external plugins;
// set_query, run_builtin, activate_window and activate_tab are
// internal-only (produced by builtin providers) and are always
// stripped from external plugin responses by SanitizeResponse.
const (
	ActionOpenPath       = "open_path"
	ActionOpenURL        = "open_url"
	ActionCopyText       = "copy_text"
	ActionRunCommand     = "run_command"
	ActionSetQuery       = "set_query"
	ActionRunBuiltin     = "run_builtin"
	ActionActivateWindow = "activate_window"
	ActionActivateTab    = "activate_tab"
)

// Sanitizer limits (see the design doc's response schema).
const (
	maxResultsPerResponse = 20
	maxTitleRunes         = 200
	maxSubtitleRunes      = 300
	maxBadgeRunes         = 24
	maxFieldLabelRunes    = 40
	maxFieldValueRunes    = 200
	maxFields             = 8
	maxIconBytes          = 32
	maxKeywords           = 8
	maxKeywordRunes       = 64
	maxMatchRanges        = 32
	maxActionPathBytes    = 2048
	maxActionURLBytes     = 2048
	maxActionCopyBytes    = 8192
	maxArgvEntries        = 16
	maxArgvEntryBytes     = 1024
)

var (
	accentColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	iconNameRe    = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

// SanitizeResponse validates and clamps everything an external plugin
// returned before it can reach the UI. It never fails: invalid pieces
// are clamped, cleared, or dropped, and every removal (a whole result
// or just its action) is described in dropped as a human-readable
// reason for logging. The input response is not mutated.
//
// allowRunCommand mirrors the manifest's allow_run_command flag: when
// false, any result carrying a run_command action is dropped entirely.
func SanitizeResponse(resp *Response, allowRunCommand bool) (results []Result, dropped []string) {
	if resp == nil {
		return nil, []string{"nil response"}
	}
	v := resp.V
	if v == 0 {
		v = ProtocolVersion
	}
	if v != ProtocolVersion {
		return nil, []string{fmt.Sprintf(
			"unsupported response version %d (want %d); dropped all %d results",
			resp.V, ProtocolVersion, len(resp.Results))}
	}
	in := resp.Results
	if len(in) > maxResultsPerResponse {
		dropped = append(dropped, fmt.Sprintf(
			"response returned %d results; dropped %d beyond the first %d",
			len(in), len(in)-maxResultsPerResponse, maxResultsPerResponse))
		in = in[:maxResultsPerResponse]
	}
	results = make([]Result, 0, len(in))
	for i, r := range in {
		clean, reasons, ok := sanitizeResult(r, i, allowRunCommand)
		dropped = append(dropped, reasons...)
		if ok {
			results = append(results, clean)
		}
	}
	return results, dropped
}

// sanitizeResult clamps one result. ok reports whether the result
// survives at all; reasons describes anything removed.
func sanitizeResult(r Result, idx int, allowRunCommand bool) (clean Result, reasons []string, ok bool) {
	r.Title = truncateRunes(strings.TrimSpace(stripControl(r.Title)), maxTitleRunes)
	if r.Title == "" {
		return Result{}, []string{fmt.Sprintf("result %d: empty title; dropped", idx)}, false
	}
	r.Subtitle = truncateRunes(stripControl(r.Subtitle), maxSubtitleRunes)
	r.Badge = truncateRunes(stripControl(r.Badge), maxBadgeRunes)
	r.Icon = sanitizeIcon(r.Icon)
	// Internal-only: image-icon resolution is reserved for trusted
	// builtin sources (the Action.DesktopID precedent).
	r.IconKey = ""
	if r.AccentColor != "" && !accentColorRe.MatchString(r.AccentColor) {
		r.AccentColor = ""
	}

	score := DefaultScore
	if r.Score != nil && !math.IsNaN(*r.Score) {
		score = *r.Score
	}
	score = math.Min(100, math.Max(0, score))
	r.Score = &score

	if len(r.Keywords) > 0 {
		kws := make([]string, 0, min(len(r.Keywords), maxKeywords))
		for _, kw := range r.Keywords {
			kw = truncateRunes(strings.TrimSpace(stripControl(kw)), maxKeywordRunes)
			if kw == "" {
				continue
			}
			kws = append(kws, kw)
			if len(kws) == maxKeywords {
				break
			}
		}
		if len(kws) == 0 {
			kws = nil
		}
		r.Keywords = kws
	}
	r.MatchRanges = normalizeRanges(r.MatchRanges, utf8.RuneCountInString(r.Title))

	if len(r.Fields) > maxFields {
		r.Fields = r.Fields[:maxFields]
	}
	if len(r.Fields) > 0 {
		fields := make([]Field, len(r.Fields))
		for j, f := range r.Fields {
			fields[j] = Field{
				Label: truncateRunes(stripControl(f.Label), maxFieldLabelRunes),
				Value: truncateRunes(stripControl(f.Value), maxFieldValueRunes),
			}
		}
		r.Fields = fields
	}

	if r.Action != nil {
		action, reason, dropResult := sanitizeAction(*r.Action, idx, allowRunCommand)
		if dropResult {
			return Result{}, []string{reason}, false
		}
		if reason != "" {
			reasons = append(reasons, reason)
		}
		r.Action = action
	}
	return r, reasons, true
}

// sanitizeAction validates one action. It returns the cleaned action
// (nil when the action is stripped), a reason when anything was
// removed, and dropResult=true when the WHOLE result must go (only
// the run_command permission gate does that).
func sanitizeAction(a Action, idx int, allowRunCommand bool) (act *Action, reason string, dropResult bool) {
	switch a.Type {
	case ActionSetQuery, ActionRunBuiltin, ActionActivateWindow, ActionActivateTab:
		return nil, fmt.Sprintf("result %d: internal-only action type %q stripped", idx, a.Type), false
	case ActionOpenPath:
		a.Argv, a.Window, a.Tab, a.DesktopID = nil, "", "", ""
		a.Value = stripControl(a.Value)
		if a.Value == "" || len(a.Value) > maxActionPathBytes || !filepath.IsAbs(a.Value) {
			return nil, fmt.Sprintf(
				"result %d: open_path action needs a non-empty absolute path (max %d bytes); action stripped",
				idx, maxActionPathBytes), false
		}
		return &a, "", false
	case ActionOpenURL:
		a.Argv, a.Window, a.Tab, a.DesktopID = nil, "", "", ""
		a.Value = stripControl(a.Value)
		if len(a.Value) > maxActionURLBytes || !validHTTPURL(a.Value) {
			return nil, fmt.Sprintf(
				"result %d: open_url action needs an http(s) URL (max %d bytes); action stripped",
				idx, maxActionURLBytes), false
		}
		return &a, "", false
	case ActionCopyText:
		a.Argv, a.Window, a.Tab, a.DesktopID = nil, "", "", ""
		a.Value = stripControl(a.Value)
		if a.Value == "" || len(a.Value) > maxActionCopyBytes {
			return nil, fmt.Sprintf(
				"result %d: copy_text action needs a non-empty value (max %d bytes); action stripped",
				idx, maxActionCopyBytes), false
		}
		return &a, "", false
	case ActionRunCommand:
		if !allowRunCommand {
			return nil, fmt.Sprintf(
				"result %d: run_command action but the manifest does not set allow_run_command; result dropped",
				idx), true
		}
		a.Value, a.Window, a.Tab, a.DesktopID = "", "", "", ""
		if len(a.Argv) == 0 || len(a.Argv) > maxArgvEntries {
			return nil, fmt.Sprintf(
				"result %d: run_command action needs 1..%d argv entries; action stripped",
				idx, maxArgvEntries), false
		}
		argv := make([]string, len(a.Argv))
		for j, arg := range a.Argv {
			arg = stripControl(arg)
			if len(arg) > maxArgvEntryBytes {
				return nil, fmt.Sprintf(
					"result %d: run_command argv entry %d exceeds %d bytes; action stripped",
					idx, j, maxArgvEntryBytes), false
			}
			argv[j] = arg
		}
		a.Argv = argv
		return &a, "", false
	default:
		return nil, fmt.Sprintf("result %d: unknown action type %q stripped", idx, a.Type), false
	}
}

// normalizeRanges validates plugin-supplied matchRanges against the
// (post-truncation) title rune length: pairs are clamped into range,
// empty/inverted pairs dropped, the rest sorted and merged, capped at
// maxMatchRanges. Nothing valid left = nil.
func normalizeRanges(rs [][2]int, runeLen int) [][2]int {
	if len(rs) == 0 || runeLen <= 0 {
		return nil
	}
	clamped := make([][2]int, 0, len(rs))
	for _, r := range rs {
		lo, hi := r[0], r[1]
		if lo < 0 {
			lo = 0
		}
		if hi > runeLen {
			hi = runeLen
		}
		if lo >= hi {
			continue
		}
		clamped = append(clamped, [2]int{lo, hi})
	}
	if len(clamped) == 0 {
		return nil
	}
	sort.Slice(clamped, func(i, j int) bool { return clamped[i][0] < clamped[j][0] })
	merged := clamped[:1]
	for _, r := range clamped[1:] {
		last := &merged[len(merged)-1]
		if r[0] <= last[1] {
			if r[1] > last[1] {
				last[1] = r[1]
			}
			continue
		}
		merged = append(merged, r)
	}
	if len(merged) > maxMatchRanges {
		merged = merged[:maxMatchRanges]
	}
	return merged
}

// sanitizeIcon keeps an icon only when it is a builtin icon name
// (lowercase [a-z0-9_-]+) or a short literal glyph such as an emoji
// (at most 32 bytes, no control characters). Anything else clears.
func sanitizeIcon(icon string) string {
	if icon == "" {
		return ""
	}
	if iconNameRe.MatchString(icon) {
		return icon
	}
	if len(icon) > maxIconBytes || strings.ContainsFunc(icon, unicode.IsControl) {
		return ""
	}
	return icon
}

// validHTTPURL reports whether raw parses as an absolute http(s) URL
// with a host.
func validHTTPURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

// stripControl replaces Unicode control characters (including tabs
// and newlines) with spaces so plugin text cannot smuggle terminal
// escapes or paste-injection payloads into the UI or the clipboard.
func stripControl(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// truncateRunes caps s at max runes.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
