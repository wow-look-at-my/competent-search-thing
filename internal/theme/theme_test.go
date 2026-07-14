package theme

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTheme drops a theme JSON into dir/themes/<name>.json.
func writeTheme(t *testing.T, dir, name, body string) {
	t.Helper()
	themes := filepath.Join(dir, "themes")
	require.NoError(t, os.MkdirAll(themes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(themes, name+".json"), []byte(body), 0o644))
}

// requireComplete asserts the map covers exactly the closed token set.
func requireComplete(t *testing.T, tokens map[string]string) {
	t.Helper()
	require.Len(t, tokens, len(TokenNames))
	for _, k := range TokenNames {
		require.Contains(t, tokens, k, "token %s missing", k)
		require.NotEmpty(t, tokens[k], "token %s empty", k)
	}
}

func TestTokenNamesAreUniqueAndStable(t *testing.T) {
	require.Len(t, TokenNames, 22, "the token set is a closed public contract")
	seen := map[string]bool{}
	for _, k := range TokenNames {
		require.False(t, seen[k], "duplicate token %s", k)
		seen[k] = true
	}
	// Spot-pin a few names the plugin workstream depends on.
	for _, k := range []string{"accent", "accent-fg", "badge-bg", "badge-fg", "highlight"} {
		require.True(t, seen[k], "public token %s must exist", k)
	}
}

func TestResolveBuiltinDark(t *testing.T) {
	tokens, err := Resolve("dark", "")
	require.NoError(t, err)
	requireComplete(t, tokens)
	require.Equal(t, "#18181c", tokens["bg"])
	require.Equal(t, "#f2f2f5", tokens["fg"])
	require.Equal(t, "0.97", tokens["bg-opacity"])
	require.Equal(t, `system-ui, -apple-system, "Segoe UI", sans-serif`, tokens["font-family"])
}

func TestResolveBuiltinLightExtendsDark(t *testing.T) {
	tokens, err := Resolve("light", "")
	require.NoError(t, err)
	requireComplete(t, tokens)
	require.Equal(t, "#f7f7f9", tokens["bg"], "light overrides the palette")
	require.Equal(t, "#1b1b22", tokens["fg"])
	require.Equal(t, "14px", tokens["font-size"], "metrics inherit from dark")
	require.Equal(t, "10px", tokens["radius"])
	require.Equal(t, "0px", tokens["blur"])
}

func TestResolveEmptyNameIsDark(t *testing.T) {
	for _, name := range []string{"", "   ", "\t"} {
		tokens, err := Resolve(name, t.TempDir())
		require.NoError(t, err)
		require.Equal(t, Dark(), tokens)
	}
}

func TestResolveUserThemeWithExtendsChain(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "base", `{"extends": "light", "tokens": {"accent": "#ff0000", "gap": "6px"}}`)
	writeTheme(t, dir, "leaf", `{"name": "leaf", "extends": "base", "tokens": {"accent": "#00ff00"}}`)

	tokens, err := Resolve("leaf", dir)
	require.NoError(t, err)
	requireComplete(t, tokens)
	require.Equal(t, "#00ff00", tokens["accent"], "leaf overrides base")
	require.Equal(t, "6px", tokens["gap"], "base overrides light")
	require.Equal(t, "#f7f7f9", tokens["bg"], "light shows through")
	require.Equal(t, "14px", tokens["font-size"], "dark fills the rest")
}

func TestResolveUserThemeGapsFillFromDark(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "tiny", `{"tokens": {"bg": "#000000"}}`)
	tokens, err := Resolve("tiny", dir)
	require.NoError(t, err)
	requireComplete(t, tokens)
	require.Equal(t, "#000000", tokens["bg"])
	require.Equal(t, Dark()["fg"], tokens["fg"], "unset tokens take the dark values")
}

func TestResolveValueWhitespaceIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "pad", `{"tokens": {"bg": "  #101010  "}}`)
	tokens, err := Resolve("pad", dir)
	require.NoError(t, err)
	require.Equal(t, "#101010", tokens["bg"])
}

func TestResolveErrorsFallBackToDark(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "cycle-a", `{"extends": "cycle-b", "tokens": {}}`)
	writeTheme(t, dir, "cycle-b", `{"extends": "cycle-a", "tokens": {}}`)
	writeTheme(t, dir, "selfish", `{"extends": "selfish", "tokens": {}}`)
	writeTheme(t, dir, "chain4", `{"extends": "chain3", "tokens": {}}`)
	writeTheme(t, dir, "chain3", `{"extends": "chain2", "tokens": {}}`)
	writeTheme(t, dir, "chain2", `{"extends": "chain1", "tokens": {}}`)
	writeTheme(t, dir, "chain1", `{"extends": "dark", "tokens": {}}`)
	writeTheme(t, dir, "corrupt", `{not json`)
	writeTheme(t, dir, "unknown-key", `{"tokens": {"bg": "#111111", "shadow": "#222222", "aura": "1px"}}`)
	writeTheme(t, dir, "orphan", `{"extends": "no-such-parent", "tokens": {}}`)

	cases := []struct {
		name    string
		theme   string
		dir     string
		errPart string
	}{
		{"missing file", "nope", dir, "nope"},
		{"missing dir", "anything", filepath.Join(dir, "does-not-exist"), "anything"},
		{"empty config dir", "user-thing", "", "no config directory"},
		{"invalid name slash", "../evil", dir, "invalid name"},
		{"invalid name space", "a b", dir, "invalid name"},
		{"cycle", "cycle-a", dir, "cycle"},
		{"self extend", "selfish", dir, "cycle"},
		{"chain too deep", "chain4", dir, "chain longer than 4"},
		{"corrupt json", "corrupt", dir, "parsing"},
		{"unknown keys named", "unknown-key", dir, "aura, shadow"},
		{"extends unknown parent", "orphan", dir, "no-such-parent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens, err := Resolve(tc.theme, tc.dir)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errPart)
			require.Equal(t, Dark(), tokens, "every error hands back the dark builtin")
		})
	}
}

func TestResolveInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		value   string
		errPart string
	}{
		{"url injection", "bg", "url(http://evil/x)", `forbidden substring "url("`},
		{"expression injection", "fg", "expression(alert(1))", `forbidden substring "expression("`},
		{"at import", "border", "@import 'x'", `forbidden substring "@import"`},
		{"semicolon", "accent", "#fff; background: red", `forbidden substring ";"`},
		{"open brace", "accent", "#fff }{", `forbidden substring "{"`},
		{"close brace", "accent", "#fff}", `forbidden substring "}"`},
		{"empty value", "bg", "   ", "empty value"},
		{"named color", "bg", "red", "unsupported value"},
		{"javascript scheme", "bg", "javascript:alert(1)", "unsupported value"},
		{"bad hex", "bg", "#12345g", "unsupported value"},
		{"hex wrong length", "bg", "#12345", "unsupported value"},
		{"unbalanced rgb", "bg", "rgb(1, 2, 3", "unsupported value"},
		{"color fn with letters", "bg", "rgb(calc(1), 2, 3)", "unsupported value"},
		{"bad unit", "font-size", "12pt", "unsupported value"},
		{"length with spaces", "padding", "1 6px", "unsupported value"},
		{"var reference", "bg", "var(--sb-fg)", "unsupported value"},
		{"font family bad char", "font-family", "Arial(", "unsupported font list"},
		{"font family semicolon", "font-family", "Arial; x", `forbidden substring ";"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"tokens": {"` + tc.token + `": ` + jsonString(tc.value) + `}}`
			writeTheme(t, dir, "bad", body)
			tokens, err := Resolve("bad", dir)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errPart)
			require.Contains(t, err.Error(), tc.token, "error names the token")
			require.Equal(t, Dark(), tokens)
		})
	}
}

func TestResolveValidValueShapes(t *testing.T) {
	cases := []struct {
		token string
		value string
	}{
		{"bg", "#abc"},
		{"bg", "#abcd"},
		{"bg", "#a1b2c3"},
		{"bg", "#a1b2c3d4"},
		{"fg", "rgb(1, 2, 3)"},
		{"fg", "rgba(1, 2, 3, 0.5)"},
		{"fg", "hsl(210, 40%, 50%)"},
		{"fg", "hsla(210, 40%, 50%, 0.5)"},
		{"scrollbar", "rgb(0 0 0 / 20%)"},
		{"font-size", "1.25rem"},
		{"padding", "0.5em"},
		{"radius", "50%"},
		{"blur", "12px"},
		{"gap", ".5em"},
		{"bg-opacity", "0.5"},
		{"bg-opacity", ".9"},
		{"gap", "0"},
		{"padding", "-4px"},
		{"font-family", "Iosevka, 'JetBrains Mono', \"Segoe UI\", sans-serif"},
	}
	for _, tc := range cases {
		t.Run(tc.token+" "+tc.value, func(t *testing.T) {
			dir := t.TempDir()
			writeTheme(t, dir, "ok", `{"tokens": {"`+tc.token+`": `+jsonString(tc.value)+`}}`)
			tokens, err := Resolve("ok", dir)
			require.NoError(t, err)
			require.Equal(t, tc.value, tokens[tc.token])
		})
	}
}

func TestBuiltinsCannotBeShadowed(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "dark", `{"tokens": {"bg": "#ff0000"}}`)
	tokens, err := Resolve("dark", dir)
	require.NoError(t, err)
	require.Equal(t, "#18181c", tokens["bg"], "the embedded dark wins over a user dark.json")
}

func TestDarkReturnsIsolatedCopies(t *testing.T) {
	a := Dark()
	a["bg"] = "mutated"
	b := Dark()
	require.Equal(t, "#18181c", b["bg"], "callers cannot poison the shared base")
	got, err := Resolve("dark", "")
	require.NoError(t, err)
	require.Equal(t, "#18181c", got["bg"])
}

// jsonString quotes a value as a JSON string literal.
func jsonString(s string) string {
	out := `"`
	for _, r := range s {
		switch r {
		case '"':
			out += `\"`
		case '\\':
			out += `\\`
		default:
			out += string(r)
		}
	}
	return out + `"`
}
