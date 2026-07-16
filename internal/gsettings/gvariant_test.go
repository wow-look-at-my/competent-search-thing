package gsettings

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStringArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		ok   bool
	}{
		{"empty annotated", "@as []", []string{}, true},
		{"empty bare", "[]", []string{}, true},
		{"empty with spaces", "[  ]", []string{}, true},
		{"single", "['<Alt>space']", []string{"<Alt>space"}, true},
		{"multiple", "['<Super>space', 'XF86Keyboard']", []string{"<Super>space", "XF86Keyboard"}, true},
		{"trailing newline", "['<Alt>space']\n", []string{"<Alt>space"}, true},
		{"double quoted", `["it's here", 'b']`, []string{"it's here", "b"}, true},
		{"escaped quote", `['a\'b']`, []string{"a'b"}, true},
		{"escaped backslash", `['a\\b']`, []string{`a\b`}, true},
		{"empty string element", "['']", []string{""}, true},
		{"no spaces after comma", "['a','b']", []string{"a", "b"}, true},
		{"non-array int", "6", nil, false},
		{"non-array bool", "true", nil, false},
		{"non-array string", "'hi'", nil, false},
		{"array of ints", "[1, 2]", nil, false},
		{"unterminated element", "['a", nil, false},
		{"unterminated array", "['a'", nil, false},
		{"garbage separator", "['a' 'b']", nil, false},
		{"bare annotation", "@as", nil, false},
		{"empty input", "", nil, false},
		{"tuple", "(uint32 1, 'x')", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseStringArray(tc.in)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestParseVariantStringValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"single quoted", "'<Alt>space'\n", "<Alt>space", true},
		{"double quoted", `"cmd 'x'"`, "cmd 'x'", true},
		{"empty string", "''", "", true},
		{"escapes", `'a\'b\\c'`, `a'b\c`, true},
		{"newline escape", `'a\nb'`, "a\nb", true},
		{"tab escape", `'a\tb'`, "a\tb", true},
		{"cr escape", `'a\rb'`, "a\rb", true},
		{"not a string", "6", "", false},
		{"trailing junk", "'a' b", "", false},
		{"unterminated", "'a", "", false},
		{"dangling escape", `'a\`, "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseVariantStringValue(tc.in)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestQuoteVariantString(t *testing.T) {
	require.Equal(t, "'plain'", quoteVariantString("plain"))
	require.Equal(t, `'with space'`, quoteVariantString("with space"))
	require.Equal(t, `'it\'s'`, quoteVariantString("it's"))
	require.Equal(t, `'back\\slash'`, quoteVariantString(`back\slash`))

	// Round trip through the parser.
	for _, s := range []string{"", "plain", "it's", `a\'b"c`, "<Control><Alt>space"} {
		got, ok := parseVariantStringValue(quoteVariantString(s))
		require.True(t, ok, "round trip of %q parses", s)
		require.Equal(t, s, got)
	}
}

func TestQuoteVariantArray(t *testing.T) {
	require.Equal(t, "[]", quoteVariantArray(nil))
	require.Equal(t, "['a']", quoteVariantArray([]string{"a"}))
	require.Equal(t, "['a', 'b c']", quoteVariantArray([]string{"a", "b c"}))

	got, ok := parseStringArray(quoteVariantArray([]string{"x", "it's"}))
	require.True(t, ok)
	require.Equal(t, []string{"x", "it's"}, got)
}
