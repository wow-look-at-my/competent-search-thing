package plugin

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Trigger describes when a plugin wants to see a query. A plugin
// matches when ANY of the text paths (prefix, regex, all_queries)
// matches AND the focused-app gate (when present) matches. Bang
// targeting bypasses triggers entirely.
//
// Compile must be called (once) before Match or Boost; manifest
// loading does this. A Trigger is read-only after Compile and safe
// for concurrent use.
type Trigger struct {
	// Prefix matches case-insensitively against the start of the
	// query; the remainder (trimmed) becomes the stripped query.
	Prefix string `json:"prefix"`
	// Regex is a case-insensitive RE2 pattern matched against the RAW
	// query; on match the stripped query is the trimmed raw query.
	Regex string `json:"regex"`
	// AllQueries matches every query, gated by the effective minimum
	// query length (which defaults to 2 when unset).
	AllQueries bool `json:"all_queries"`
	// MinQueryLen is the minimum STRIPPED query length in runes. It
	// gates every text path. When zero and AllQueries is set, the
	// effective minimum is 2.
	MinQueryLen int `json:"min_query_len"`
	// DebounceMS delays dispatch; a newer query cancels the wait.
	// (Consumed by the dispatch pipeline, not by Match.)
	DebounceMS int `json:"debounce_ms"`
	// FocusedApp, when non-nil, restricts the trigger to queries typed
	// while a matching application was focused.
	FocusedApp *FocusedGate `json:"focused_app"`
	// FocusedBoost (0..100) is added to result scores when the
	// focused-app gate matches; see Boost.
	FocusedBoost int `json:"focused_boost"`

	re     *regexp.Regexp
	nameRe *regexp.Regexp
	exeRe  *regexp.Regexp
}

// FocusedGate matches the focused application by name and/or
// executable path (case-insensitive RE2). An empty pattern is a
// wildcard, but at least one pattern must be set.
type FocusedGate struct {
	NameRegex string `json:"name_regex"`
	ExeRegex  string `json:"exe_regex"`
}

// Compile validates and compiles the trigger's regular expressions
// (case-insensitively). Errors name the offending field.
func (t *Trigger) Compile() error {
	if t.Regex != "" {
		re, err := regexp.Compile("(?i)" + t.Regex)
		if err != nil {
			return fmt.Errorf("trigger.regex: %w", err)
		}
		t.re = re
	}
	if g := t.FocusedApp; g != nil {
		if g.NameRegex == "" && g.ExeRegex == "" {
			return fmt.Errorf("trigger.focused_app: name_regex and exe_regex are both empty")
		}
		if g.NameRegex != "" {
			re, err := regexp.Compile("(?i)" + g.NameRegex)
			if err != nil {
				return fmt.Errorf("trigger.focused_app.name_regex: %w", err)
			}
			t.nameRe = re
		}
		if g.ExeRegex != "" {
			re, err := regexp.Compile("(?i)" + g.ExeRegex)
			if err != nil {
				return fmt.Errorf("trigger.focused_app.exe_regex: %w", err)
			}
			t.exeRe = re
		}
	}
	return nil
}

// Match reports whether the trigger fires for query, and returns the
// stripped query the plugin should receive. focused is the app that
// was focused at hotkey press (nil when unknown); a trigger with a
// focused gate never matches when focused is nil.
//
// Text paths are tried in order prefix, regex, all_queries; the first
// match decides the stripped query. The effective minimum query
// length gates all paths, counted in runes of the stripped query.
func (t *Trigger) Match(query string, focused *AppInfo) (stripped string, ok bool) {
	if t.FocusedApp != nil && !t.focusedMatches(focused) {
		return "", false
	}
	stripped, ok = t.textMatch(query)
	if !ok {
		return "", false
	}
	minLen := t.MinQueryLen
	if t.AllQueries && minLen == 0 {
		minLen = 2
	}
	if utf8.RuneCountInString(stripped) < minLen {
		return "", false
	}
	return stripped, true
}

// Boost returns the score boost (0..100) to add to this plugin's
// results: FocusedBoost clamped, when the focused gate is present and
// matches; otherwise 0.
func (t *Trigger) Boost(focused *AppInfo) int {
	if t.FocusedApp == nil || !t.focusedMatches(focused) {
		return 0
	}
	b := t.FocusedBoost
	if b < 0 {
		b = 0
	}
	if b > 100 {
		b = 100
	}
	return b
}

// Claims reports whether the trigger's prefix or regex path matches
// query: the plugin CLAIMED the query (a calculator's "=6*7") rather
// than receiving it from the all-queries fan-out. Consulted only
// after Match already dispatched, so the focused gate and minimum
// length are irrelevant here.
func (t *Trigger) Claims(query string) bool {
	if t.Prefix != "" {
		if _, ok := cutPrefixFold(query, t.Prefix); ok {
			return true
		}
	}
	return t.re != nil && t.re.MatchString(query)
}

// textMatch tries the three text paths in order.
func (t *Trigger) textMatch(query string) (string, bool) {
	if t.Prefix != "" {
		if rest, ok := cutPrefixFold(query, t.Prefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	if t.re != nil && t.re.MatchString(query) {
		return strings.TrimSpace(query), true
	}
	if t.AllQueries {
		return strings.TrimSpace(query), true
	}
	return "", false
}

// focusedMatches applies the gate. Unset patterns are wildcards; a
// pattern that was never compiled fails closed.
func (t *Trigger) focusedMatches(focused *AppInfo) bool {
	if focused == nil {
		return false
	}
	g := t.FocusedApp
	if g.NameRegex != "" && (t.nameRe == nil || !t.nameRe.MatchString(focused.Name)) {
		return false
	}
	if g.ExeRegex != "" && (t.exeRe == nil || !t.exeRe.MatchString(focused.Exe)) {
		return false
	}
	return true
}

// cutPrefixFold strips prefix from the start of s, comparing the
// first len(prefix)-in-runes runes with Unicode case folding.
func cutPrefixFold(s, prefix string) (rest string, ok bool) {
	sr := []rune(s)
	pn := utf8.RuneCountInString(prefix)
	if len(sr) < pn {
		return "", false
	}
	if !strings.EqualFold(string(sr[:pn]), prefix) {
		return "", false
	}
	return string(sr[pn:]), true
}
