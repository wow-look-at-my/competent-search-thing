package plugin

// SanitizeResponse's per-action-type validation tests, split from
// schema_test.go (which keeps the response/title/icon/score/field
// sanitizer tests and the wire-format pins).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeActionInternalTypesStripped(t *testing.T) {
	for _, typ := range []string{ActionSetQuery, ActionRunBuiltin, ActionActivateWindow, ActionActivateTab} {
		t.Run(typ, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: typ, Value: "x", Window: "42", Tab: "c1:2:3"}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, true)
			require.Len(t, results, 1, "result survives with action stripped")
			require.Nil(t, results[0].Action)
			require.Len(t, dropped, 1)
			require.Contains(t, dropped[0], "internal-only")
			require.Contains(t, dropped[0], typ)
		})
	}
}

func TestSanitizeActionClearsStrayWindow(t *testing.T) {
	// A window id, tab token, or desktop id smuggled onto an external
	// action type is cleared, exactly like a stray argv on a
	// value-carrying action: all three fields are internal-only and
	// only the builtin providers may set them.
	cases := []Action{
		{Type: ActionOpenPath, Value: "/tmp/x", Window: "42", Tab: "c1:2:3", DesktopID: "x.desktop"},
		{Type: ActionOpenURL, Value: "https://example.com/", Window: "42", Tab: "c1:2:3", DesktopID: "x.desktop"},
		{Type: ActionCopyText, Value: "x", Window: "42", Tab: "c1:2:3", DesktopID: "x.desktop"},
		{Type: ActionRunCommand, Argv: []string{"true"}, Window: "42", Tab: "c1:2:3", DesktopID: "x.desktop"},
	}
	for _, a := range cases {
		t.Run(a.Type, func(t *testing.T) {
			action := a
			r := Result{Title: "t", Action: &action}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, true)
			require.Empty(t, dropped)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Action)
			require.Empty(t, results[0].Action.Window)
			require.Empty(t, results[0].Action.Tab,
				"an external plugin must never route a live-tab activation")
			require.Empty(t, results[0].Action.DesktopID,
				"an external plugin must never steer the credentialed launch path")
		})
	}
}

func TestSanitizeActionOpenPath(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		keep    bool
		wantVal string
	}{
		{name: "absolute path kept", value: "/tmp/file.txt", keep: true, wantVal: "/tmp/file.txt"},
		{name: "relative stripped", value: "tmp/file.txt"},
		{name: "empty stripped", value: ""},
		{name: "too long stripped", value: "/" + strings.Repeat("a", 2100)},
		{name: "control chars replaced", value: "/tmp/a\nb", keep: true, wantVal: "/tmp/a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: ActionOpenPath, Value: tt.value, Argv: []string{"junk"}}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			if !tt.keep {
				require.Nil(t, results[0].Action)
				require.Len(t, dropped, 1)
				require.Contains(t, dropped[0], "open_path")
				return
			}
			require.Empty(t, dropped)
			require.NotNil(t, results[0].Action)
			require.Equal(t, ActionOpenPath, results[0].Action.Type)
			require.Equal(t, tt.wantVal, results[0].Action.Value)
			require.Nil(t, results[0].Action.Argv, "argv cleared on value actions")
		})
	}
}

func TestSanitizeActionOpenURL(t *testing.T) {
	tests := []struct {
		name  string
		value string
		keep  bool
	}{
		{name: "http kept", value: "http://example.com/x", keep: true},
		{name: "https kept", value: "https://example.com", keep: true},
		{name: "uppercase scheme kept", value: "HTTP://EXAMPLE.COM/x", keep: true},
		{name: "ftp stripped", value: "ftp://example.com"},
		{name: "file stripped", value: "file:///etc/passwd"},
		{name: "javascript stripped", value: "javascript:alert(1)"},
		{name: "no host stripped", value: "http://"},
		{name: "relative stripped", value: "/just/a/path"},
		{name: "empty stripped", value: ""},
		{name: "garbage stripped", value: "://nope"},
		{name: "too long stripped", value: "http://example.com/" + strings.Repeat("a", 2100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: ActionOpenURL, Value: tt.value}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			if !tt.keep {
				require.Nil(t, results[0].Action)
				require.Len(t, dropped, 1)
				require.Contains(t, dropped[0], "open_url")
				return
			}
			require.Empty(t, dropped)
			require.NotNil(t, results[0].Action)
			require.Equal(t, tt.value, results[0].Action.Value)
		})
	}
}

func TestSanitizeActionCopyText(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		keep    bool
		wantVal string
	}{
		{name: "kept", value: "4", keep: true, wantVal: "4"},
		{name: "empty stripped", value: ""},
		{name: "too long stripped", value: strings.Repeat("a", 8193)},
		{name: "max length kept", value: strings.Repeat("a", 8192), keep: true, wantVal: strings.Repeat("a", 8192)},
		{name: "paste injection flattened", value: "ls\nrm -rf /", keep: true, wantVal: "ls rm -rf /"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: ActionCopyText, Value: tt.value}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			if !tt.keep {
				require.Nil(t, results[0].Action)
				require.Len(t, dropped, 1)
				require.Contains(t, dropped[0], "copy_text")
				return
			}
			require.Empty(t, dropped)
			require.NotNil(t, results[0].Action)
			require.Equal(t, tt.wantVal, results[0].Action.Value)
		})
	}
}

func TestSanitizeActionRunCommandGate(t *testing.T) {
	r := Result{Title: "t", Action: &Action{Type: ActionRunCommand, Argv: []string{"echo", "hi"}}}
	results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
	require.Empty(t, results, "whole result dropped without allow_run_command")
	require.Len(t, dropped, 1)
	require.Contains(t, dropped[0], "allow_run_command")
	require.Contains(t, dropped[0], "result dropped")
}

func TestSanitizeActionRunCommandAllowed(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		keep     bool
		wantArgv []string
	}{
		{name: "valid kept", argv: []string{"echo", "hi"}, keep: true, wantArgv: []string{"echo", "hi"}},
		{name: "single entry kept", argv: []string{"true"}, keep: true, wantArgv: []string{"true"}},
		{name: "empty argv stripped", argv: nil},
		{name: "too many entries stripped", argv: make([]string, 17)},
		{name: "sixteen entries kept", argv: func() []string {
			a := make([]string, 16)
			for i := range a {
				a[i] = "x"
			}
			return a
		}(), keep: true, wantArgv: func() []string {
			a := make([]string, 16)
			for i := range a {
				a[i] = "x"
			}
			return a
		}()},
		{name: "oversized entry stripped", argv: []string{"echo", strings.Repeat("a", 1025)}},
		{name: "control chars replaced", argv: []string{"echo", "a\nb"}, keep: true, wantArgv: []string{"echo", "a b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: ActionRunCommand, Value: "junk", Argv: tt.argv}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, true)
			require.Len(t, results, 1, "result always survives when run_command is allowed")
			if !tt.keep {
				require.Nil(t, results[0].Action)
				require.Len(t, dropped, 1)
				require.Contains(t, dropped[0], "run_command")
				return
			}
			require.Empty(t, dropped)
			require.NotNil(t, results[0].Action)
			require.Equal(t, tt.wantArgv, results[0].Action.Argv)
			require.Empty(t, results[0].Action.Value, "value cleared on run_command")
		})
	}
}

func TestSanitizeActionUnknownType(t *testing.T) {
	for _, typ := range []string{"dance", "", "OPEN_PATH"} {
		t.Run("type "+typ, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: typ, Value: "x"}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, true)
			require.Len(t, results, 1)
			require.Nil(t, results[0].Action)
			require.Len(t, dropped, 1)
			require.Contains(t, dropped[0], "unknown action type")
		})
	}
}
