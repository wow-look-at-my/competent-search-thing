package theme

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

// Lockstep tests between schemas/theme.schema.json and this package:
// the embedded builtin themes must validate, documents parseTheme
// rejects must fail the schema, the schema's token property set must
// equal TokenNames exactly, and the key-completeness guard covers the
// themeFile top level.

func themeSchemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "schemas", "theme.schema.json")
}

func compileThemeSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(themeSchemaPath(t))
	require.NoError(t, err)
	return sch
}

func validateThemeJSON(sch *jsonschema.Schema, data []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

// TestBuiltinThemesMatchSchema validates the embedded builtin theme
// files -- the same bytes Resolve parses -- against the schema.
func TestBuiltinThemesMatchSchema(t *testing.T) {
	sch := compileThemeSchema(t)
	entries, err := builtinFS.ReadDir("builtin")
	require.NoError(t, err)
	require.Len(t, entries, 2, "expected the dark and light builtins")
	for _, e := range entries {
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		require.NoError(t, err)
		require.NoError(t, validateThemeJSON(sch, data),
			"builtin/%s must match schemas/theme.schema.json", e.Name())
	}
}

// TestThemeSchemaRejectsInvalid mirrors parseTheme: documents the
// loader rejects must fail the schema.
func TestThemeSchemaRejectsInvalid(t *testing.T) {
	sch := compileThemeSchema(t)
	cases := map[string]string{
		"unknown token key": `{"tokens":{"bogus":"#fff"}}`,
		"url() value":       `{"tokens":{"bg":"url(javascript:alert(1))"}}`,
		"named color":       `{"tokens":{"bg":"red"}}`,
		"css injection":     `{"tokens":{"bg":"#fff; background: pwned"}}`,
		"empty value":       `{"tokens":{"bg":""}}`,
		"bad extends name":  `{"extends":"../other"}`,
		"bad font charset":  `{"tokens":{"font-family":"Arial; @import x"}}`,
	}
	for name, doc := range cases {
		require.Error(t, validateThemeJSON(sch, []byte(doc)), "case %q must fail validation", name)
	}

	// A sparse user theme extending a builtin -- the documented usage
	// -- validates, including the "$schema" editor hint.
	require.NoError(t, validateThemeJSON(sch, []byte(
		`{"$schema":"https://raw.githubusercontent.com/wow-look-at-my/competent-search-thing/master/schemas/theme.schema.json",
		  "name":"midnight","extends":"dark","tokens":{"bg":"#0b0b12","accent":"rgba(127, 212, 196, 0.9)","radius":"6px","bg-opacity":"0.9"}}`,
	)))
}

func themeSchemaDoc(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(themeSchemaPath(t))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

// TestThemeSchemaKeyCompleteness is the drift guard: the schema's top
// level mirrors the themeFile struct, and the schema's token property
// set equals TokenNames exactly -- adding a token without updating the
// schema (or vice versa) fails here.
func TestThemeSchemaKeyCompleteness(t *testing.T) {
	doc := themeSchemaDoc(t)
	props, ok := doc["properties"].(map[string]any)
	require.True(t, ok)

	var top []string
	for k := range props {
		if k == "$schema" {
			continue
		}
		top = append(top, k)
	}
	sort.Strings(top)

	typ := reflect.TypeOf(themeFile{})
	var tags []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		require.NotEmpty(t, tag, "field themeFile.%s needs an explicit json tag", f.Name)
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	require.Equal(t, tags, top, "theme.schema.json top level out of sync with themeFile")

	tokensNode, ok := props["tokens"].(map[string]any)
	require.True(t, ok)
	tokenProps, ok := tokensNode["properties"].(map[string]any)
	require.True(t, ok, "theme.schema.json tokens needs a properties object")
	var schemaTokens []string
	for k := range tokenProps {
		schemaTokens = append(schemaTokens, k)
	}
	sort.Strings(schemaTokens)
	wantTokens := append([]string(nil), TokenNames...)
	sort.Strings(wantTokens)
	require.Equal(t, wantTokens, schemaTokens,
		"theme.schema.json tokens out of sync with TokenNames -- update both together")
}
