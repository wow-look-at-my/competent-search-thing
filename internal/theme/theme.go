// Package theme resolves the searchbar's design tokens. A theme is a
// flat map from a CLOSED set of token keys (TokenNames) to CSS values;
// the frontend exposes each token as the CSS custom property
// --sb-<token> and every color/size/effect in style.css flows through
// those variables. Two builtin themes are embedded (dark -- the
// original palette -- and light); user themes are JSON files under
// <configDir>/themes/<name>.json and may extend a builtin or another
// user theme. Values are validated against a conservative whitelist so
// a theme file cannot inject arbitrary CSS; the deliberately
// unvalidated escape hatch is <configDir>/themes/custom.css (served by
// internal/app, not this package). Resolution NEVER crashes the app:
// any error falls back to the builtin dark theme.
package theme

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// TokenNames is the closed, ordered set of theme token keys. These
// names are a PUBLIC, STABLE contract: the frontend maps token k to
// the CSS custom property --sb-<k>, the README documents the table,
// and the plugin workstream styles plugin accents and result badges
// against them (accent/accent-fg are the primary plugin-facing knobs;
// badge-bg/badge-fg are reserved for plugin result badges). Never
// rename or remove an entry; adding one requires updating
// builtin/dark.json, builtin/light.json, frontend/src/style.css's
// :root block (a sync test enforces that), and the README table.
var TokenNames = []string{
	"bg",
	"bg-elevated",
	"fg",
	"fg-dim",
	"accent",
	"accent-fg",
	"selection-bg",
	"selection-fg",
	"border",
	"highlight",
	"warning",
	"badge-bg",
	"badge-fg",
	"scrollbar",
	"font-family",
	"font-size",
	"font-size-small",
	"radius",
	"gap",
	"padding",
	"bg-opacity",
	"blur",
}

// DefaultName is the theme every error path falls back to.
const DefaultName = "dark"

// maxChain caps the length of an extends chain (the theme itself plus
// its ancestors) so a deep or cyclic chain cannot stall resolution.
const maxChain = 4

//go:embed builtin/dark.json builtin/light.json
var builtinFS embed.FS

var isToken = func() map[string]bool {
	m := make(map[string]bool, len(TokenNames))
	for _, k := range TokenNames {
		m[k] = true
	}
	return m
}()

// The value whitelist. Everything a token may hold must match one of
// these shapes (font-family has its own charset); anything else --
// url(...), attr(...), gradients, named colors, javascript: -- is
// rejected. forbidden is a belt-and-braces substring check that runs
// first so the error names the dangerous part.
var (
	themeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hexRe       = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	colorFnRe   = regexp.MustCompile(`^(?:rgb|rgba|hsl|hsla)\([0-9,./% ]+\)$`)
	lengthRe    = regexp.MustCompile(`^-?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:px|em|rem|%)$`)
	numberRe    = regexp.MustCompile(`^-?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)
	fontRe      = regexp.MustCompile(`^[A-Za-z0-9 ,'"-]+$`)
)

var forbidden = []string{"url(", "expression(", "@import", ";", "{", "}"}

// themeFile is the on-disk JSON shape:
//
//	{"name": "solarized", "extends": "dark", "tokens": {"bg": "#002b36"}}
//
// The file's base name (without .json) is the authoritative theme name
// used for lookup and extends references; the "name" field is
// informational. "extends" is optional and resolves builtin-or-user. A
// theme does not have to define every token: anything the extends
// chain leaves unset falls back to the dark builtin's value.
type themeFile struct {
	Name    string            `json:"name"`
	Extends string            `json:"extends"`
	Tokens  map[string]string `json:"tokens"`
}

// Resolve returns the fully resolved token map for the named theme:
// builtin lookup first ("dark", "light"), then
// <configDir>/themes/<name>.json, merged over its extends chain and
// gap-filled from the dark builtin, so the result always covers every
// TokenNames entry. An empty name means dark. On ANY error -- unknown
// theme, invalid name, unreadable or corrupt file, unknown token key,
// value failing the whitelist, extends cycle or overlong chain -- it
// returns the dark builtin's tokens ALONGSIDE the error: callers log
// the error and keep a usable theme, they never crash.
func Resolve(name, configDir string) (map[string]string, error) {
	tokens, err := resolve(name, configDir)
	if err != nil {
		return Dark(), err
	}
	return tokens, nil
}

// Dark returns a copy of the built-in dark theme's tokens -- the hard
// fallback every error path lands on.
func Dark() map[string]string {
	return darkBase()
}

func resolve(name, configDir string) (map[string]string, error) {
	if strings.TrimSpace(name) == "" {
		name = DefaultName
	}
	var chain []themeFile
	seen := map[string]bool{}
	for cur := name; ; {
		if len(chain) >= maxChain {
			return nil, fmt.Errorf("theme %q: extends chain longer than %d themes", name, maxChain)
		}
		if seen[cur] {
			return nil, fmt.Errorf("theme %q: extends cycle at %q", name, cur)
		}
		seen[cur] = true
		tf, err := loadTheme(cur, configDir)
		if err != nil {
			return nil, err
		}
		chain = append(chain, tf)
		if tf.Extends == "" {
			break
		}
		cur = tf.Extends
	}
	// Base coat: dark fills any token the chain leaves unset. Then the
	// chain applies base-first, so children override their ancestors.
	out := darkBase()
	for i := len(chain) - 1; i >= 0; i-- {
		for k, v := range chain[i].Tokens {
			out[k] = v
		}
	}
	return out, nil
}

// loadTheme fetches and validates one theme file by name: embedded
// builtins win, then the user's themes directory. A user theme can
// therefore extend a builtin but never shadow one.
func loadTheme(name, configDir string) (themeFile, error) {
	if data, err := builtinFS.ReadFile("builtin/" + name + ".json"); err == nil {
		return parseTheme(name, data)
	}
	if !themeNameRe.MatchString(name) {
		return themeFile{}, fmt.Errorf("theme %q: invalid name (allowed: letters, digits, %q, %q, %q)", name, ".", "-", "_")
	}
	if configDir == "" {
		return themeFile{}, fmt.Errorf("theme %q: not a builtin and no config directory to search", name)
	}
	path := filepath.Join(configDir, "themes", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return themeFile{}, fmt.Errorf("theme %q: %w", name, err)
	}
	return parseTheme(name, data)
}

// parseTheme decodes and strictly validates one theme file: every
// token key must be in TokenNames and every value must pass
// validateValue. Values are stored trimmed.
func parseTheme(name string, data []byte) (themeFile, error) {
	var tf themeFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return themeFile{}, fmt.Errorf("theme %q: parsing: %w", name, err)
	}
	var unknown []string
	for k := range tf.Tokens {
		if !isToken[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return themeFile{}, fmt.Errorf("theme %q: unknown token key(s): %s", name, strings.Join(unknown, ", "))
	}
	for k, v := range tf.Tokens {
		trimmed := strings.TrimSpace(v)
		if err := validateValue(k, trimmed); err != nil {
			return themeFile{}, fmt.Errorf("theme %q: %w", name, err)
		}
		tf.Tokens[k] = trimmed
	}
	return tf, nil
}

// validateValue enforces the conservative value whitelist: hex colors,
// rgb()/rgba()/hsl()/hsla() with numeric arguments, px/em/rem/%
// lengths, and bare numbers; the font-family token instead takes a
// comma-separated font list over a tight charset. Dangerous substrings
// are rejected by name first.
func validateValue(key, v string) error {
	if v == "" {
		return fmt.Errorf("token %s: empty value", key)
	}
	low := strings.ToLower(v)
	for _, bad := range forbidden {
		if strings.Contains(low, bad) {
			return fmt.Errorf("token %s: forbidden substring %q", key, bad)
		}
	}
	if key == "font-family" {
		if !fontRe.MatchString(v) {
			return fmt.Errorf("token %s: unsupported font list %q", key, v)
		}
		return nil
	}
	if hexRe.MatchString(v) || colorFnRe.MatchString(v) || lengthRe.MatchString(v) || numberRe.MatchString(v) {
		return nil
	}
	return fmt.Errorf("token %s: unsupported value %q (allowed: hex colors, rgb()/rgba()/hsl()/hsla(), px/em/rem/%% lengths, bare numbers)", key, v)
}

// The dark builtin parsed once; darkBase hands out copies. The
// embedded file is compile-time constant and pinned by tests, so the
// parse cannot fail at runtime.
var (
	darkOnce   sync.Once
	darkTokens map[string]string
)

func darkBase() map[string]string {
	darkOnce.Do(func() {
		if data, err := builtinFS.ReadFile("builtin/" + DefaultName + ".json"); err == nil {
			if tf, perr := parseTheme(DefaultName, data); perr == nil {
				darkTokens = tf.Tokens
			}
		}
	})
	out := make(map[string]string, len(darkTokens))
	for k, v := range darkTokens {
		out[k] = v
	}
	return out
}
