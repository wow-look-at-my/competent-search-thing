package plugin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// The formal JSON Schemas under schemas/ document the plugin manifest
// and the v1 wire protocol for plugin authors. These tests keep them
// in lockstep with the Go validators in this package: the shipped
// example manifests and canned wire payloads built from the Go structs
// must validate, schema-rejected inputs must mirror what the Go side
// rejects (or clamps -- the schemas are deliberately stricter, see the
// schema descriptions), and the key-completeness guards fail when a
// struct field is added without updating the schema (or vice versa).

// schemasDir returns the repo's schemas/ directory, resolved relative
// to this source file so the tests work from any working directory.
func schemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "schemas")
}

// compileSchema compiles one schema file from schemas/.
func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(filepath.Join(schemasDir(t), name))
	require.NoError(t, err, "schema %s must compile", name)
	return sch
}

// validateJSON validates raw JSON bytes against a compiled schema.
func validateJSON(sch *jsonschema.Schema, data []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

// TestSchemasAllCompile compiles every schema file in schemas/ --
// catching JSON syntax errors, bad regexes, and structural mistakes --
// and pins the expected file set so a new format's schema cannot be
// forgotten (or silently dropped).
func TestSchemasAllCompile(t *testing.T) {
	dir := schemasDir(t)
	matches, err := filepath.Glob(filepath.Join(dir, "*.schema.json"))
	require.NoError(t, err)
	var names []string
	for _, m := range matches {
		names = append(names, filepath.Base(m))
	}
	sort.Strings(names)
	require.Equal(t, []string{
		"config.schema.json",
		"plugin-manifest.schema.json",
		"plugin-request.schema.json",
		"plugin-response.schema.json",
		"theme.schema.json",
	}, names, "schemas/ must hold exactly the five documented schemas")
	for _, n := range names {
		compileSchema(t, n)
	}
}

// TestShippedExampleManifestsMatchSchema validates every shipped
// example plugin manifest against plugin-manifest.schema.json.
func TestShippedExampleManifestsMatchSchema(t *testing.T) {
	sch := compileSchema(t, "plugin-manifest.schema.json")
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	glob := filepath.Join(filepath.Dir(thisFile), "..", "..", "examples", "plugins", "*", "manifest.json")
	matches, err := filepath.Glob(glob)
	require.NoError(t, err)
	require.Len(t, matches, 3, "expected the three shipped example manifests")
	for _, m := range matches {
		data, err := os.ReadFile(m)
		require.NoError(t, err)
		require.NoError(t, validateJSON(sch, data), "%s must match the manifest schema", m)
	}
}

// TestManifestSchemaRejectsInvalid mirrors the Go-side manifest
// validation: inputs the loader rejects must fail the schema too.
func TestManifestSchemaRejectsInvalid(t *testing.T) {
	sch := compileSchema(t, "plugin-manifest.schema.json")
	cases := map[string]string{
		"bad id":               `{"id":"Bad ID","type":"command","command":{"argv":["x"]}}`,
		"bad type":             `{"id":"a","type":"shell","command":{"argv":["x"]}}`,
		"command without cmd":  `{"id":"a","type":"command"}`,
		"http without http":    `{"id":"a","type":"http"}`,
		"empty argv entry":     `{"id":"a","type":"command","command":{"argv":[""]}}`,
		"bad context":          `{"id":"a","type":"command","command":{"argv":["x"]},"context":["windows"]}`,
		"empty focused gate":   `{"id":"a","type":"command","command":{"argv":["x"]},"trigger":{"prefix":"=","focused_app":{}}}`,
		"bad manifest version": `{"v":2,"id":"a","type":"command","command":{"argv":["x"]}}`,
	}
	for name, doc := range cases {
		require.Error(t, validateJSON(sch, []byte(doc)), "case %q must fail validation", name)
	}
	require.NoError(t, validateJSON(sch, []byte(
		`{"id":"a","type":"command","command":{"argv":["x"]},"trigger":{"focused_app":{"name_regex":"firefox"}}}`,
	)), "a one-pattern focused gate is valid")
}

// TestWireRequestMatchesSchema marshals a fully populated Request --
// built from the Go structs, so the wire schema tracks the real
// serialization -- and validates it.
func TestWireRequestMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "plugin-request.schema.json")
	req := Request{
		V:        ProtocolVersion,
		Query:    "!calc 2+2",
		Stripped: "2+2",
		Gen:      42,
		Targeted: true,
		Bang:     "calc",
		Settings: json.RawMessage(`{"precision":4}`),
		Context: &RequestContext{
			FocusedApp:    &AppInfo{Name: "firefox", Exe: "/usr/lib/firefox/firefox", Title: "Mozilla Firefox", PID: 1234},
			RunningApps:   []AppInfo{{Name: "kitty", Exe: "/usr/bin/kitty", Title: "~/src", PID: 4321}},
			InstalledApps: []InstalledApp{{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop"}},
		},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	require.NoError(t, validateJSON(sch, data))

	// The minimal untargeted request (no declared context) validates
	// too: Settings is always a JSON object on the wire.
	minimal, err := json.Marshal(Request{V: 1, Settings: json.RawMessage(`{}`)})
	require.NoError(t, err)
	require.NoError(t, validateJSON(sch, minimal))
}

// TestWireResponseMatchesSchema marshals a fully populated Response
// exercising every result knob and every external action type, and
// validates it.
func TestWireResponseMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "plugin-response.schema.json")
	score := 90.0
	resp := Response{
		V: ProtocolVersion,
		Results: []Result{
			{
				Title:       "4",
				Subtitle:    "2+2",
				Icon:        "calculator",
				Badge:       "=",
				AccentColor: "#8db8ff",
				Score:       &score,
				Fields: []Field{
					{Label: "Hex", Value: "0x4"},
					{Label: "Binary", Value: "0b100"},
				},
				Action: &Action{Type: ActionCopyText, Value: "4"},
			},
			{Title: "Open the report", Action: &Action{Type: ActionOpenPath, Value: "/tmp/report.pdf"}},
			{Title: "Docs", Action: &Action{Type: ActionOpenURL, Value: "https://example.com/docs"}},
			{Title: "Editor", Action: &Action{Type: ActionRunCommand, Argv: []string{"code", "--new-window"}}},
			{Title: "Plain row without an action"},
		},
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	require.NoError(t, validateJSON(sch, data))
}

// TestResponseSchemaRejectsWhatTheSanitizerClamps asserts the
// authoring-aid contract: the schema REJECTS inputs the Go sanitizer
// would silently clamp, strip, or drop, so plugin authors notice.
func TestResponseSchemaRejectsWhatTheSanitizerClamps(t *testing.T) {
	sch := compileSchema(t, "plugin-response.schema.json")
	longTitle := strings.Repeat("x", 201)
	cases := map[string]string{
		"score above 100":      `{"v":1,"results":[{"title":"a","score":101}]}`,
		"score below 0":        `{"v":1,"results":[{"title":"a","score":-1}]}`,
		"empty title":          `{"v":1,"results":[{"title":""}]}`,
		"title too long":       `{"v":1,"results":[{"title":"` + longTitle + `"}]}`,
		"bad accent color":     `{"v":1,"results":[{"title":"a","accent_color":"red"}]}`,
		"internal set_query":   `{"v":1,"results":[{"title":"a","action":{"type":"set_query","value":"x"}}]}`,
		"internal run_builtin": `{"v":1,"results":[{"title":"a","action":{"type":"run_builtin","value":"quit"}}]}`,
		"internal activate_window": `{"v":1,"results":[{"title":"a",` +
			`"action":{"type":"activate_window","window":"42"}}]}`,
		"window on an external action": `{"v":1,"results":[{"title":"a",` +
			`"action":{"type":"copy_text","value":"x","window":"42"}}]}`,
		"desktop_id on an external action": `{"v":1,"results":[{"title":"a",` +
			`"action":{"type":"run_command","argv":["code"],"desktop_id":"code.desktop"}}]}`,
		"iconKey on an external result": `{"v":1,"results":[{"title":"a",` +
			`"iconKey":"app:firefox"}]}`,
		"run_command no argv": `{"v":1,"results":[{"title":"a","action":{"type":"run_command"}}]}`,
		"argv with 17 items": `{"v":1,"results":[{"title":"a","action":{"type":"run_command","argv":[` +
			strings.Repeat(`"x",`, 16) + `"x"]}}]}`,
		"wrong protocol version": `{"v":2,"results":[{"title":"a"}]}`,
		"nine fields": `{"v":1,"results":[{"title":"a","fields":[` +
			strings.Repeat(`{"label":"l","value":"v"},`, 8) + `{"label":"l","value":"v"}]}]}`,
	}
	for name, doc := range cases {
		require.Error(t, validateJSON(sch, []byte(doc)), "case %q must fail validation", name)
	}

	// 21 results: the sanitizer keeps the first 20; the schema rejects.
	many := `{"v":1,"results":[` + strings.Repeat(`{"title":"a"},`, 20) + `{"title":"a"}]}`
	require.Error(t, validateJSON(sch, []byte(many)), "more than 20 results must fail validation")
}

// jsonTagNames collects the wire names of a struct's exported json
// fields (skipping json:"-").
func jsonTagNames(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	require.Equal(t, reflect.Struct, typ.Kind())
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		require.NotEmpty(t, name, "field %s.%s needs an explicit json tag", typ.Name(), f.Name)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// schemaProperties returns the sorted property names of a schema
// object within a schema document: the top level when defName is "",
// else $defs/<defName>.
func schemaProperties(t *testing.T, file, defName string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(schemasDir(t), file))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	node := doc
	if defName != "" {
		defs, ok := doc["$defs"].(map[string]any)
		require.True(t, ok, "%s needs a $defs object", file)
		node, ok = defs[defName].(map[string]any)
		require.True(t, ok, "%s needs $defs/%s", file, defName)
	}
	props, ok := node["properties"].(map[string]any)
	require.True(t, ok, "%s: %q needs a properties object", file, defName)
	var names []string
	for k := range props {
		if k == "$schema" { // editor hint, not a Go field
			continue
		}
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// requireKeysMatch is the drift guard core: schema properties and
// struct json tags must be the same set, so adding a Go field without
// updating the schema (or vice versa) fails the build.
func requireKeysMatch(t *testing.T, file, defName string, typ reflect.Type) {
	t.Helper()
	require.Equal(t, jsonTagNames(t, typ), schemaProperties(t, file, defName),
		"%s (%s) out of sync with %s -- update the schema and the struct together",
		file, map[bool]string{true: "$defs/" + defName, false: "top level"}[defName != ""], typ.Name())
}

// TestManifestSchemaKeyCompleteness pins the manifest schema to the
// Manifest struct family.
func TestManifestSchemaKeyCompleteness(t *testing.T) {
	requireKeysMatch(t, "plugin-manifest.schema.json", "", reflect.TypeOf(Manifest{}))
	requireKeysMatch(t, "plugin-manifest.schema.json", "trigger", reflect.TypeOf(Trigger{}))
	requireKeysMatch(t, "plugin-manifest.schema.json", "focusedGate", reflect.TypeOf(FocusedGate{}))
	requireKeysMatch(t, "plugin-manifest.schema.json", "commandSpec", reflect.TypeOf(CommandSpec{}))
	requireKeysMatch(t, "plugin-manifest.schema.json", "httpSpec", reflect.TypeOf(HTTPSpec{}))
}

// TestWireSchemasKeyCompleteness pins the wire schemas to the
// Request/Response struct families.
func TestWireSchemasKeyCompleteness(t *testing.T) {
	requireKeysMatch(t, "plugin-request.schema.json", "", reflect.TypeOf(Request{}))
	requireKeysMatch(t, "plugin-request.schema.json", "requestContext", reflect.TypeOf(RequestContext{}))
	requireKeysMatch(t, "plugin-request.schema.json", "appInfo", reflect.TypeOf(AppInfo{}))
	requireKeysMatch(t, "plugin-request.schema.json", "installedApp", reflect.TypeOf(InstalledApp{}))
	requireKeysMatch(t, "plugin-response.schema.json", "", reflect.TypeOf(Response{}))
	requireKeysMatch(t, "plugin-response.schema.json", "result", reflect.TypeOf(Result{}))
	requireKeysMatch(t, "plugin-response.schema.json", "field", reflect.TypeOf(Field{}))
	requireKeysMatch(t, "plugin-response.schema.json", "action", reflect.TypeOf(Action{}))
}
