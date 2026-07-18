package plugin

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/match"
)

// builtinRewritesID is the provider id of the regex rewrite-rules
// source (disable via plugins.entries["rewrites"], or per rule with
// the rule's disabled flag).
const builtinRewritesID = "rewrites"

// maxRewriteResults caps one rewrites response (multiple rules can
// match the same query; config order is preserved).
const maxRewriteResults = 8

// RewriteRule mirrors one config.json "rewrites" entry without
// importing internal/config (the Entry pattern): a user-defined regex
// -> URL rewrite. Patterns are Go stdlib regexp (RE2: no backtracking,
// linear time -- a handful of compiled rules per keystroke is
// negligible). FULL-MATCH semantics by default: the pattern is
// compiled as ^(?:pattern)$ unless the user anchors it themselves
// (a leading ^ or trailing $ disables the wrapping); write looser
// rules with explicit .* . Replacement and Title expand capture
// groups with the stdlib syntax: $1, ${name}, $$ for a literal $.
type RewriteRule struct {
	Name        string
	Pattern     string
	Replacement string
	Title       string
	Icon        string
	Disabled    bool
}

// rewriteRule is one compiled rule.
type rewriteRule struct {
	name        string
	re          *regexp.Regexp
	replacement string
	title       string
	icon        string
}

// compileRewrites compiles the enabled config rules. Broken rules
// yield one error each (logged loudly by the app via Errors()) and are
// skipped -- never a crash.
func compileRewrites(rules []RewriteRule) ([]rewriteRule, []error) {
	var out []rewriteRule
	var errs []error
	for i, r := range rules {
		if r.Disabled {
			continue
		}
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = r.Pattern
		}
		if r.Pattern == "" || r.Replacement == "" {
			errs = append(errs, fmt.Errorf(
				"rewrite rule %d (%s): pattern and replacement are required; rule skipped", i, name))
			continue
		}
		pat := r.Pattern
		if !strings.HasPrefix(pat, "^") && !strings.HasSuffix(pat, "$") {
			pat = "^(?:" + pat + ")$" // full-match by default
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			errs = append(errs, fmt.Errorf("rewrite rule %d (%s): %v; rule skipped", i, name, err))
			continue
		}
		icon := r.Icon
		if icon == "" {
			icon = "link"
		}
		out = append(out, rewriteRule{
			name:        name,
			re:          re,
			replacement: r.Replacement,
			title:       r.Title,
			icon:        icon,
		})
	}
	return out, errs
}

// rewritesProvider turns pasted identifiers into instant links: each
// matching rule emits ONE result whose engine tier is the triggered
// band (preRanked: config order, top scores), so "XY-12345" with a
// Jira rule is unambiguously the top row. Actions are open_url ONLY
// (v1): a replacement expanding to anything but an absolute http(s)
// URL with a host is dropped (and logged) -- run_command class power
// stays in plugins.
type rewritesProvider struct {
	builtinBase
	rules []rewriteRule
	logf  func(format string, args ...any)
}

func newRewritesProvider(rules []rewriteRule, logf func(format string, args ...any)) *rewritesProvider {
	return &rewritesProvider{
		builtinBase: builtinBase{pid: builtinRewritesID, name: "Rewrites"},
		rules:       rules,
		logf:        logf,
	}
}

// match fires when ANY rule matches the trimmed query -- the source
// CLAIMS such queries (the sanctioned trigger tier).
func (p *rewritesProvider) match(query string, _ *AppInfo) (string, int, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", 0, false
	}
	for _, r := range p.rules {
		if r.re.MatchString(q) {
			return q, 0, true
		}
	}
	return "", 0, false
}

func (p *rewritesProvider) preRanked() bool { return true }
func (p *rewritesProvider) limit() int      { return maxRewriteResults }

func (p *rewritesProvider) candidates(_ context.Context, req Request) ([]match.Candidate, error) {
	q := strings.TrimSpace(req.Stripped)
	if q == "" {
		return nil, nil
	}
	var out []match.Candidate
	for _, r := range p.rules {
		m := r.re.FindStringSubmatchIndex(q)
		if m == nil {
			continue
		}
		url := string(r.re.ExpandString(nil, r.replacement, q, m))
		if !validHTTPURL(url) {
			p.logf("plugin %s: rule %q expanded to %q, not an http(s) URL; dropped", p.pid, r.name, url)
			continue
		}
		title := url
		if r.title != "" {
			title = string(r.re.ExpandString(nil, r.title, q, m))
		}
		out = append(out, match.Candidate{
			Display: title,
			Texts:   []string{title},
			SortKey: url,
			Payload: Result{
				Title:    title,
				Subtitle: r.name,
				Icon:     r.icon,
				Action:   &Action{Type: ActionOpenURL, Value: url},
			},
		})
	}
	return out, nil
}
