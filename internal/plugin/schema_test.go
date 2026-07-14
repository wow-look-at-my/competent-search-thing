package plugin

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fptr(f float64) *float64 { return &f }

// res builds a minimal valid result.
func res(title string) Result { return Result{Title: title} }

func TestSanitizeResponseNil(t *testing.T) {
	results, dropped := SanitizeResponse(nil, false)
	require.Empty(t, results)
	require.Equal(t, []string{"nil response"}, dropped)
}

func TestSanitizeResponseVersion(t *testing.T) {
	tests := []struct {
		name    string
		v       int
		wantOK  bool
		wantMsg string
	}{
		{name: "missing v means 1", v: 0, wantOK: true},
		{name: "explicit 1", v: 1, wantOK: true},
		{name: "future version rejected", v: 2, wantMsg: "unsupported response version 2"},
		{name: "negative version rejected", v: -1, wantMsg: "unsupported response version -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Response{V: tt.v, Results: []Result{res("a"), res("b")}}
			results, dropped := SanitizeResponse(resp, false)
			if tt.wantOK {
				require.Len(t, results, 2)
				require.Empty(t, dropped)
				return
			}
			require.Empty(t, results)
			require.Len(t, dropped, 1)
			require.Contains(t, dropped[0], tt.wantMsg)
			require.Contains(t, dropped[0], "2 results")
		})
	}
}

func TestSanitizeResponseCapsResultCount(t *testing.T) {
	resp := &Response{}
	for i := 0; i < 25; i++ {
		resp.Results = append(resp.Results, res(strings.Repeat("x", i+1)))
	}
	results, dropped := SanitizeResponse(resp, false)
	require.Len(t, results, 20)
	require.Len(t, dropped, 1)
	require.Contains(t, dropped[0], "25 results")
	require.Contains(t, dropped[0], "dropped 5")
	// Order preserved: the first 20 survive.
	require.Equal(t, "x", results[0].Title)
	require.Equal(t, strings.Repeat("x", 20), results[19].Title)
}

func TestSanitizeResponseDoesNotMutateInput(t *testing.T) {
	orig := Result{
		Title:  "  hi\x00there  ",
		Score:  fptr(500),
		Fields: []Field{{Label: "l\x1b", Value: "v"}},
		Action: &Action{Type: ActionRunCommand, Argv: []string{"a\x00b"}},
	}
	resp := &Response{Results: []Result{orig}}
	_, _ = SanitizeResponse(resp, true)
	require.Equal(t, "  hi\x00there  ", resp.Results[0].Title)
	require.Equal(t, float64(500), *resp.Results[0].Score)
	require.Equal(t, "l\x1b", resp.Results[0].Fields[0].Label)
	require.Equal(t, []string{"a\x00b"}, resp.Results[0].Action.Argv)
}

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		want      string
		dropped   bool
		wantInMsg string
	}{
		{name: "kept", title: "4", want: "4"},
		{name: "trimmed", title: "  4  ", want: "4"},
		{name: "empty dropped", title: "", dropped: true, wantInMsg: "empty title"},
		{name: "whitespace only dropped", title: "   ", dropped: true, wantInMsg: "empty title"},
		{name: "control chars only dropped", title: "\n\t\r", dropped: true, wantInMsg: "empty title"},
		{name: "control chars replaced", title: "a\x1b[31mb", want: "a [31mb"},
		{name: "newline replaced", title: "one\ntwo", want: "one two"},
		{
			name:  "capped at 200 runes",
			title: strings.Repeat("\u00e9", 250),
			want:  strings.Repeat("\u00e9", 200),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, dropped := SanitizeResponse(&Response{Results: []Result{res(tt.title)}}, false)
			if tt.dropped {
				require.Empty(t, results)
				require.Len(t, dropped, 1)
				require.Contains(t, dropped[0], tt.wantInMsg)
				require.Contains(t, dropped[0], "result 0")
				return
			}
			require.Len(t, results, 1)
			require.Empty(t, dropped)
			require.Equal(t, tt.want, results[0].Title)
		})
	}
}

func TestSanitizeTextCaps(t *testing.T) {
	r := Result{
		Title:    "t",
		Subtitle: strings.Repeat("s", 400),
		Badge:    strings.Repeat("b", 30),
	}
	results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
	require.Empty(t, dropped)
	require.Len(t, results, 1)
	require.Equal(t, strings.Repeat("s", 300), results[0].Subtitle)
	require.Equal(t, strings.Repeat("b", 24), results[0].Badge)
}

func TestSanitizeControlCharsInTextFields(t *testing.T) {
	r := Result{
		Title:    "t",
		Subtitle: "a\x00b",
		Badge:    "c\td",
		Fields:   []Field{{Label: "l\ne", Value: "v\rf"}},
	}
	results, _ := SanitizeResponse(&Response{Results: []Result{r}}, false)
	require.Len(t, results, 1)
	require.Equal(t, "a b", results[0].Subtitle)
	require.Equal(t, "c d", results[0].Badge)
	require.Equal(t, "l e", results[0].Fields[0].Label)
	require.Equal(t, "v f", results[0].Fields[0].Value)
}

func TestSanitizeIconRules(t *testing.T) {
	tests := []struct {
		name string
		icon string
		want string
	}{
		{name: "empty stays empty", icon: "", want: ""},
		{name: "builtin name kept", icon: "calculator", want: "calculator"},
		{name: "builtin with digits dash underscore", icon: "a-b_c9", want: "a-b_c9"},
		{name: "emoji kept", icon: "\U0001F600", want: "\U0001F600"},
		{name: "short literal kept", icon: "Calc", want: "Calc"},
		{name: "too long cleared", icon: strings.Repeat("x", 33) + "!", want: ""},
		{name: "34 bytes cleared", icon: strings.Repeat("\u00e9", 17), want: ""},
		{name: "32 bytes kept", icon: strings.Repeat("\u00e9", 16), want: strings.Repeat("\u00e9", 16)},
		{name: "control char cleared", icon: "a\x1bb!", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Icon: tt.icon}
			results, _ := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			require.Equal(t, tt.want, results[0].Icon)
		})
	}
}

func TestSanitizeAccentColor(t *testing.T) {
	tests := []struct {
		color string
		want  string
	}{
		{color: "", want: ""},
		{color: "#abc", want: "#abc"},
		{color: "#a6e3a1", want: "#a6e3a1"},
		{color: "#ABCDEF", want: "#ABCDEF"},
		{color: "#AbC", want: "#AbC"},
		{color: "#abcd", want: ""},
		{color: "#abcde", want: ""},
		{color: "#abcdef00", want: ""},
		{color: "red", want: ""},
		{color: "#ggg", want: ""},
		{color: "abc", want: ""},
		{color: " #abc", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.color, func(t *testing.T) {
			r := Result{Title: "t", AccentColor: tt.color}
			results, _ := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			require.Equal(t, tt.want, results[0].AccentColor)
		})
	}
}

func TestSanitizeScore(t *testing.T) {
	tests := []struct {
		name  string
		score *float64
		want  float64
	}{
		{name: "absent defaults to 50", score: nil, want: 50},
		{name: "kept in range", score: fptr(73.5), want: 73.5},
		{name: "zero kept", score: fptr(0), want: 0},
		{name: "hundred kept", score: fptr(100), want: 100},
		{name: "negative clamped", score: fptr(-5), want: 0},
		{name: "huge clamped", score: fptr(250), want: 100},
		{name: "positive infinity clamped", score: fptr(math.Inf(1)), want: 100},
		{name: "negative infinity clamped", score: fptr(math.Inf(-1)), want: 0},
		{name: "NaN defaults to 50", score: fptr(math.NaN()), want: 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result{Title: "t", Score: tt.score}
			results, _ := SanitizeResponse(&Response{Results: []Result{r}}, false)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Score)
			require.Equal(t, tt.want, *results[0].Score)
		})
	}
}

func TestSanitizeFields(t *testing.T) {
	t.Run("nil fields stay nil", func(t *testing.T) {
		results, _ := SanitizeResponse(&Response{Results: []Result{res("t")}}, false)
		require.Nil(t, results[0].Fields)
	})
	t.Run("excess fields truncated to 8", func(t *testing.T) {
		r := Result{Title: "t"}
		for i := 0; i < 10; i++ {
			r.Fields = append(r.Fields, Field{Label: "l", Value: "v"})
		}
		results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, false)
		require.Empty(t, dropped)
		require.Len(t, results[0].Fields, 8)
	})
	t.Run("label and value capped", func(t *testing.T) {
		r := Result{Title: "t", Fields: []Field{{
			Label: strings.Repeat("l", 50),
			Value: strings.Repeat("v", 250),
		}}}
		results, _ := SanitizeResponse(&Response{Results: []Result{r}}, false)
		require.Equal(t, strings.Repeat("l", 40), results[0].Fields[0].Label)
		require.Equal(t, strings.Repeat("v", 200), results[0].Fields[0].Value)
	})
}

func TestSanitizeActionInternalTypesStripped(t *testing.T) {
	for _, typ := range []string{ActionSetQuery, ActionRunBuiltin} {
		t.Run(typ, func(t *testing.T) {
			r := Result{Title: "t", Action: &Action{Type: typ, Value: "x"}}
			results, dropped := SanitizeResponse(&Response{Results: []Result{r}}, true)
			require.Len(t, results, 1, "result survives with action stripped")
			require.Nil(t, results[0].Action)
			require.Len(t, dropped, 1)
			require.Contains(t, dropped[0], "internal-only")
			require.Contains(t, dropped[0], typ)
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

func TestSanitizeMultipleReasonsAggregate(t *testing.T) {
	resp := &Response{Results: []Result{
		{Title: ""},                                                     // dropped: empty title
		{Title: "ok", Action: &Action{Type: ActionSetQuery}},            // action stripped
		{Title: "run", Action: &Action{Type: ActionRunCommand}},         // dropped: gate
		{Title: "fine"},                                                 // clean
		{Title: "bad url", Action: &Action{Type: ActionOpenURL, Value: "nope"}}, // action stripped
	}}
	results, dropped := SanitizeResponse(resp, false)
	require.Len(t, results, 3)
	require.Equal(t, "ok", results[0].Title)
	require.Equal(t, "fine", results[1].Title)
	require.Equal(t, "bad url", results[2].Title)
	require.Len(t, dropped, 4)
}

func TestRequestWireFormat(t *testing.T) {
	req := Request{
		V:        ProtocolVersion,
		Query:    "!calc 2+2",
		Stripped: "2+2",
		Gen:      7,
		Targeted: true,
		Bang:     "calc",
		Settings: json.RawMessage(`{"precision":2}`),
		Context: &RequestContext{
			FocusedApp:    &AppInfo{Name: "firefox", Exe: "/usr/bin/firefox", Title: "Mozilla", PID: 1234},
			RunningApps:   []AppInfo{{Name: "code", Exe: "/usr/bin/code", Title: "VS Code", PID: 99}},
			InstalledApps: []InstalledApp{{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"}},
		},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"v": 1,
		"query": "!calc 2+2",
		"stripped": "2+2",
		"gen": 7,
		"targeted": true,
		"bang": "calc",
		"settings": {"precision": 2},
		"context": {
			"focused_app": {"name": "firefox", "exe": "/usr/bin/firefox", "title": "Mozilla", "pid": 1234},
			"running_apps": [{"name": "code", "exe": "/usr/bin/code", "title": "VS Code", "pid": 99}],
			"installed_apps": [{"name": "Firefox", "exec": "firefox %u", "id": "firefox.desktop"}]
		}
	}`, string(data))
}

func TestRequestOmitsContextWhenAbsent(t *testing.T) {
	data, err := json.Marshal(Request{V: 1, Query: "q", Stripped: "q", Settings: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"context"`)

	// Declared-but-partial context omits the missing parts.
	data, err = json.Marshal(Request{V: 1, Settings: json.RawMessage(`{}`), Context: &RequestContext{
		RunningApps: []AppInfo{{Name: "a"}},
	}})
	require.NoError(t, err)
	require.Contains(t, string(data), `"running_apps"`)
	require.NotContains(t, string(data), `"focused_app"`)
	require.NotContains(t, string(data), `"installed_apps"`)
}

func TestResponseWireFormat(t *testing.T) {
	raw := `{
		"v": 1,
		"results": [{
			"title": "4",
			"subtitle": "2 + 2",
			"icon": "calculator",
			"badge": "CALC",
			"accent_color": "#a6e3a1",
			"score": 100,
			"fields": [{"label": "Hex", "value": "0x4"}],
			"action": {"type": "copy_text", "value": "4"}
		}]
	}`
	var resp Response
	require.NoError(t, json.Unmarshal([]byte(raw), &resp))
	require.Equal(t, 1, resp.V)
	require.Len(t, resp.Results, 1)
	r := resp.Results[0]
	require.Equal(t, "4", r.Title)
	require.Equal(t, "2 + 2", r.Subtitle)
	require.Equal(t, "calculator", r.Icon)
	require.Equal(t, "CALC", r.Badge)
	require.Equal(t, "#a6e3a1", r.AccentColor)
	require.NotNil(t, r.Score)
	require.Equal(t, float64(100), *r.Score)
	require.Equal(t, []Field{{Label: "Hex", Value: "0x4"}}, r.Fields)
	require.NotNil(t, r.Action)
	require.Equal(t, ActionCopyText, r.Action.Type)
	require.Equal(t, "4", r.Action.Value)

	// Absent score decodes as nil (detectably absent).
	var bare Response
	require.NoError(t, json.Unmarshal([]byte(`{"results":[{"title":"x"}]}`), &bare))
	require.Equal(t, 0, bare.V, "missing v decodes as zero and is treated as 1 by the sanitizer")
	require.Nil(t, bare.Results[0].Score)
}

func TestSanitizedResultMarshalsScore(t *testing.T) {
	results, _ := SanitizeResponse(&Response{Results: []Result{res("t")}}, false)
	data, err := json.Marshal(results[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"title":"t","score":50}`, string(data))
}

func TestActionTypeConstants(t *testing.T) {
	require.Equal(t, "open_path", ActionOpenPath)
	require.Equal(t, "open_url", ActionOpenURL)
	require.Equal(t, "copy_text", ActionCopyText)
	require.Equal(t, "run_command", ActionRunCommand)
	require.Equal(t, "set_query", ActionSetQuery)
	require.Equal(t, "run_builtin", ActionRunBuiltin)
	require.Equal(t, 1, ProtocolVersion)
	require.Equal(t, float64(50), DefaultScore)
}
