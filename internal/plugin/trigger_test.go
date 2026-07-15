package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// compiled compiles a trigger, failing the test on error.
func compiled(t *testing.T, tr Trigger) *Trigger {
	t.Helper()
	require.NoError(t, tr.Compile())
	return &tr
}

func TestTriggerCompile(t *testing.T) {
	tests := []struct {
		name    string
		tr      Trigger
		wantErr string
	}{
		{name: "empty trigger compiles", tr: Trigger{}},
		{name: "valid regex", tr: Trigger{Regex: "^[0-9]+$"}},
		{name: "bad regex names field", tr: Trigger{Regex: "(["}, wantErr: "trigger.regex"},
		{name: "valid gate name only", tr: Trigger{FocusedApp: &FocusedGate{NameRegex: "firefox"}}},
		{name: "valid gate exe only", tr: Trigger{FocusedApp: &FocusedGate{ExeRegex: "/usr/"}}},
		{name: "gate both empty rejected", tr: Trigger{FocusedApp: &FocusedGate{}}, wantErr: "trigger.focused_app"},
		{name: "gate bad name regex", tr: Trigger{FocusedApp: &FocusedGate{NameRegex: "(("}}, wantErr: "trigger.focused_app.name_regex"},
		{name: "gate bad exe regex", tr: Trigger{FocusedApp: &FocusedGate{ExeRegex: "(("}}, wantErr: "trigger.focused_app.exe_regex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tr.Compile()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestTriggerMatchPrefix(t *testing.T) {
	tr := compiled(t, Trigger{Prefix: "="})
	tests := []struct {
		name         string
		query        string
		wantStripped string
		wantOK       bool
	}{
		{name: "basic", query: "=2+2", wantStripped: "2+2", wantOK: true},
		{name: "remainder trimmed", query: "=  2+2  ", wantStripped: "2+2", wantOK: true},
		{name: "exact prefix only", query: "=", wantStripped: "", wantOK: true},
		{name: "no prefix", query: "2+2", wantOK: false},
		{name: "empty query", query: "", wantOK: false},
		{name: "prefix not at start", query: " =2", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, ok := tr.Match(tt.query, nil)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantStripped, stripped)
		})
	}
}

func TestTriggerMatchPrefixCaseInsensitive(t *testing.T) {
	tr := compiled(t, Trigger{Prefix: "calc:"})
	stripped, ok := tr.Match("CALC: 2+2", nil)
	require.True(t, ok)
	require.Equal(t, "2+2", stripped)

	// And the other direction: uppercase prefix, lowercase query.
	tr = compiled(t, Trigger{Prefix: "CALC:"})
	stripped, ok = tr.Match("calc:2", nil)
	require.True(t, ok)
	require.Equal(t, "2", stripped)
}

func TestTriggerMatchPrefixMultibyte(t *testing.T) {
	// EqualFold folds across case for non-ASCII too; the prefix length
	// is counted in runes, not bytes.
	tr := compiled(t, Trigger{Prefix: "\u00e9"})
	stripped, ok := tr.Match("\u00c9x", nil)
	require.True(t, ok)
	require.Equal(t, "x", stripped)

	// Query shorter (in runes) than the prefix.
	tr = compiled(t, Trigger{Prefix: "abc"})
	_, ok = tr.Match("ab", nil)
	require.False(t, ok)
}

func TestTriggerMatchRegex(t *testing.T) {
	tr := compiled(t, Trigger{Regex: `^\s*[0-9(). +*/-]+$`})
	tests := []struct {
		name         string
		query        string
		wantStripped string
		wantOK       bool
	}{
		{name: "matches raw", query: "2+2", wantStripped: "2+2", wantOK: true},
		{name: "stripped is trimmed raw", query: "  2+2 ", wantStripped: "2+2", wantOK: true},
		{name: "no match", query: "hello", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, ok := tr.Match(tt.query, nil)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantStripped, stripped)
		})
	}

	// Case-insensitive compile.
	ci := compiled(t, Trigger{Regex: "^hex$"})
	_, ok := ci.Match("HEX", nil)
	require.True(t, ok)

	// The regex runs against the RAW query, not the trimmed one.
	anchored := compiled(t, Trigger{Regex: "^[0-9]+$"})
	_, ok = anchored.Match(" 42", nil)
	require.False(t, ok, "leading space breaks an anchored raw-query regex")
}

func TestTriggerMatchRegexWithoutCompileFailsClosed(t *testing.T) {
	tr := &Trigger{Regex: "^x$"}
	_, ok := tr.Match("x", nil)
	require.False(t, ok, "uncompiled regex path must not match")
}

func TestTriggerMatchAllQueries(t *testing.T) {
	tr := compiled(t, Trigger{AllQueries: true})
	tests := []struct {
		name         string
		query        string
		wantStripped string
		wantOK       bool
	}{
		{name: "two runes match (default min)", query: "hi", wantStripped: "hi", wantOK: true},
		{name: "one rune gated", query: "h", wantOK: false},
		{name: "empty gated", query: "", wantOK: false},
		{name: "whitespace only gated", query: "   ", wantOK: false},
		{name: "stripped is trimmed", query: "  hey  ", wantStripped: "hey", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, ok := tr.Match(tt.query, nil)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantStripped, stripped)
		})
	}

	// Explicit MinQueryLen 1 overrides the default of 2.
	one := compiled(t, Trigger{AllQueries: true, MinQueryLen: 1})
	stripped, ok := one.Match("h", nil)
	require.True(t, ok)
	require.Equal(t, "h", stripped)
}

func TestTriggerMinQueryLenGatesAllPaths(t *testing.T) {
	// Prefix path: min counts runes of the STRIPPED query.
	tr := compiled(t, Trigger{Prefix: "=", MinQueryLen: 3})
	_, ok := tr.Match("=ab", nil)
	require.False(t, ok, "2-rune stripped query below min 3")
	stripped, ok := tr.Match("=abc", nil)
	require.True(t, ok)
	require.Equal(t, "abc", stripped)

	// Runes, not bytes.
	tr = compiled(t, Trigger{Prefix: "=", MinQueryLen: 2})
	stripped, ok = tr.Match("= \u00e9\u00e9 ", nil)
	require.True(t, ok)
	require.Equal(t, "\u00e9\u00e9", stripped)

	// Regex path is gated too.
	re := compiled(t, Trigger{Regex: "^[0-9]+$", MinQueryLen: 3})
	_, ok = re.Match("42", nil)
	require.False(t, ok)
	_, ok = re.Match("123", nil)
	require.True(t, ok)

	// Without AllQueries a zero MinQueryLen stays zero: bare prefix ok.
	bare := compiled(t, Trigger{Prefix: "="})
	stripped, ok = bare.Match("=", nil)
	require.True(t, ok)
	require.Equal(t, "", stripped)
}

func TestTriggerMatchPathPrecedence(t *testing.T) {
	// Prefix wins over regex/all_queries for the stripped value.
	tr := compiled(t, Trigger{Prefix: "=", Regex: "^.*$", AllQueries: true, MinQueryLen: 1})
	stripped, ok := tr.Match("=x", nil)
	require.True(t, ok)
	require.Equal(t, "x", stripped, "prefix path decides the stripped query")

	// When the prefix does not match, the regex path still fires.
	stripped, ok = tr.Match("y", nil)
	require.True(t, ok)
	require.Equal(t, "y", stripped)
}

func TestTriggerMatchNoPathsNeverMatches(t *testing.T) {
	tr := compiled(t, Trigger{})
	for _, q := range []string{"", "x", "hello world"} {
		_, ok := tr.Match(q, nil)
		require.False(t, ok, "query %q", q)
	}
}

func TestTriggerFocusedGate(t *testing.T) {
	firefox := &AppInfo{Name: "Firefox", Exe: "/usr/lib/firefox/firefox", Title: "Mozilla", PID: 1}
	editor := &AppInfo{Name: "code", Exe: "/usr/share/code/code", PID: 2}

	tr := compiled(t, Trigger{Prefix: "=", FocusedApp: &FocusedGate{NameRegex: "^fire"}})
	tests := []struct {
		name    string
		focused *AppInfo
		wantOK  bool
	}{
		{name: "matching app (case-insensitive)", focused: firefox, wantOK: true},
		{name: "non-matching app", focused: editor, wantOK: false},
		{name: "nil focused fails the gate", focused: nil, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, ok := tr.Match("=2", tt.focused)
			require.Equal(t, tt.wantOK, ok)
			if ok {
				require.Equal(t, "2", stripped)
			}
		})
	}

	// The gate alone is not a match: the text paths still gate.
	_, ok := tr.Match("nope", firefox)
	require.False(t, ok)

	// Exe-only gate: name is a wildcard.
	exe := compiled(t, Trigger{Prefix: "=", FocusedApp: &FocusedGate{ExeRegex: "firefox$"}})
	_, ok = exe.Match("=2", firefox)
	require.True(t, ok)
	_, ok = exe.Match("=2", editor)
	require.False(t, ok)

	// Both patterns set: both must match.
	both := compiled(t, Trigger{Prefix: "=", FocusedApp: &FocusedGate{NameRegex: "fire", ExeRegex: "/nomatch/"}})
	_, ok = both.Match("=2", firefox)
	require.False(t, ok)
}

func TestTriggerFocusedGateUncompiledFailsClosed(t *testing.T) {
	tr := &Trigger{Prefix: "=", FocusedApp: &FocusedGate{NameRegex: "fire"}}
	_, ok := tr.Match("=2", &AppInfo{Name: "firefox"})
	require.False(t, ok, "uncompiled gate must fail closed")
}

func TestTriggerBoost(t *testing.T) {
	firefox := &AppInfo{Name: "firefox", Exe: "/usr/bin/firefox"}
	tests := []struct {
		name    string
		tr      Trigger
		focused *AppInfo
		want    int
	}{
		{name: "no gate means zero", tr: Trigger{FocusedBoost: 40}, focused: firefox, want: 0},
		{
			name:    "gate match returns boost",
			tr:      Trigger{FocusedBoost: 40, FocusedApp: &FocusedGate{NameRegex: "fire"}},
			focused: firefox,
			want:    40,
		},
		{
			name:    "boost clamped high",
			tr:      Trigger{FocusedBoost: 150, FocusedApp: &FocusedGate{NameRegex: "fire"}},
			focused: firefox,
			want:    100,
		},
		{
			name:    "boost clamped low",
			tr:      Trigger{FocusedBoost: -5, FocusedApp: &FocusedGate{NameRegex: "fire"}},
			focused: firefox,
			want:    0,
		},
		{
			name:    "gate mismatch means zero",
			tr:      Trigger{FocusedBoost: 40, FocusedApp: &FocusedGate{NameRegex: "^chrome$"}},
			focused: firefox,
			want:    0,
		},
		{
			name: "nil focused means zero",
			tr:   Trigger{FocusedBoost: 40, FocusedApp: &FocusedGate{NameRegex: "fire"}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := compiled(t, tt.tr)
			require.Equal(t, tt.want, tr.Boost(tt.focused))
		})
	}
}
