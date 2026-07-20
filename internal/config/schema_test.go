package config

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

// Lockstep tests between schemas/config.schema.json and this package:
// the default config must validate, invalid documents must fail, and
// the key-completeness guard fails when a Config field is added
// without updating the schema (or vice versa).

func configSchemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := goruntime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "schemas", "config.schema.json")
}

func compileConfigSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	sch, err := jsonschema.NewCompiler().Compile(configSchemaPath(t))
	require.NoError(t, err)
	return sch
}

func validateConfigJSON(sch *jsonschema.Schema, data []byte) error {
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

// TestDefaultConfigMatchesSchema marshals Default() -- the exact
// document Save writes on first run -- and validates it.
func TestDefaultConfigMatchesSchema(t *testing.T) {
	sch := compileConfigSchema(t)
	data, err := json.MarshalIndent(Default(), "", "  ")
	require.NoError(t, err)
	require.NoError(t, validateConfigJSON(sch, data),
		"the default config must match schemas/config.schema.json")

	// A populated config exercising every optional branch validates
	// too (built from the Go structs so renames are caught here).
	full := Config{
		Roots:                 []string{"/home/me"},
		RootsVersion:          currentRootsVersion,
		Excludes:              []string{".git", "*.tmp", "/home/*/secret"},
		Hotkey:                "ctrl+shift+k",
		RescanIntervalMinutes: 30,
		MaxResults:            100,
		Search: SearchConfig{
			FuzzyDisabled: true,
			Frecency: FrecencyConfig{
				Disabled:       true,
				HalfLifeDays:   7,
				WeightFrecency: 2.5,
				WeightRecency:  -1, // negative = the documented per-signal off switch
				WeightCwd:      0.5,
				WeightNoise:    1,
				TierJumpCount:  5,
			},
			Priors: PriorsConfig{Enabled: true},
			Telemetry: TelemetryConfig{
				Enabled:       true,
				MaxSizeKB:     1024,
				RetainQueries: true,
			},
			Arbiter: ArbiterConfig{Enabled: true},
		},
		Watcher: WatcherConfig{
			MaxWatches:    -1,
			SweepMinutes:  45,
			SweepDisabled: true,
			WatchExcludes: []string{"node_modules", "/home/*/scratch"},
			Backend:       WatcherBackendFanotify,
		},
		Theme: "light",
		Plugins: PluginsConfig{
			Disabled: false,
			Entries: map[string]PluginEntry{
				"calc": {Disabled: true, Settings: json.RawMessage(`{"precision":4}`)},
			},
		},
		Bangs: BangsConfig{
			Sigils:  []string{"!", "/", "@"},
			Aliases: map[string]string{"math": "calc"},
		},
		Tray:    TrayConfig{Disabled: true},
		History: HistoryConfig{PersistDisabled: true},
		Stats:   StatsConfig{Disabled: true},
		Window:  WindowConfig{Translucent: true, Width: 900, Height: 640},
		Firefox: FirefoxConfig{
			FrequentSites: FrequentSitesConfig{
				MinVisitsMonth: 20,
				MinVisitsWeek:  2,
				RefreshMinutes: 30,
				MaxResults:     10,
				ProfileDir:     "/home/me/.mozilla/firefox/abc.default",
			},
			OpenTabs: OpenTabsConfig{
				MaxResults: 8,
				ProfileDir: "/home/me/.mozilla/firefox/abc.default",
			},
		},
		Preview: PreviewConfig{
			Enabled:       true,
			WindowWidth:   1920,
			WindowHeight:  1000,
			TextMaxKB:     512,
			ImageMaxEdge:  1200,
			DirMaxEntries: 500,
			Kagi: PreviewKagiConfig{
				APIKey:     "kagi-secret",
				BaseURL:    "https://kagi.internal.example",
				MaxResults: 5,
			},
			OpenAI: PreviewOpenAIConfig{
				APIKey:          "sk-secret",
				BaseURL:         "http://llm.local:8080/openai",
				Model:           "gpt-5",
				MaxOutputTokens: 2048,
			},
		},
	}
	data, err = json.Marshal(full)
	require.NoError(t, err)
	require.NoError(t, validateConfigJSON(sch, data))

	// Every backend enum value validates (kept in lockstep with the
	// schema's enum; "kqueue" stays a rejected runtime-only label,
	// see TestConfigSchemaRejectsInvalid).
	for _, b := range []string{WatcherBackendAuto, WatcherBackendFanotify, WatcherBackendInotify, WatcherBackendFSEvents} {
		require.NoError(t, validateConfigJSON(sch, []byte(`{"watcher":{"backend":"`+b+`"}}`)),
			"backend %q validates", b)
	}
}

// TestConfigSchemaRejectsInvalid mirrors the Go-side validation and
// normalization: documents the app would reject or silently repair
// must fail the schema, so editors flag them.
func TestConfigSchemaRejectsInvalid(t *testing.T) {
	sch := compileConfigSchema(t)
	cases := map[string]string{
		"two-char sigil":                   `{"bangs":{"sigils":["ab"]}}`,
		"letter sigil":                     `{"bangs":{"sigils":["x"]}}`,
		"digit sigil":                      `{"bangs":{"sigils":["7"]}}`,
		"space sigil":                      `{"bangs":{"sigils":[" "]}}`,
		"negative rescan":                  `{"rescanIntervalMinutes":-5}`,
		"negative rootsVersion":            `{"rootsVersion":-1}`,
		"non-integer rootsVersion":         `{"rootsVersion":"2"}`,
		"zero maxResults":                  `{"maxResults":0}`,
		"search fuzzy typo":                `{"search":{"fuzzyDisabld":true}}`,
		"non-bool search fuzzyDisabled":    `{"search":{"fuzzyDisabled":"yes"}}`,
		"non-integer maxWatches":           `{"watcher":{"maxWatches":"lots"}}`,
		"negative sweepMinutes":            `{"watcher":{"sweepMinutes":-5}}`,
		"watcher sweep typo":               `{"watcher":{"sweepDisabld":true}}`,
		"non-bool sweepDisabled":           `{"watcher":{"sweepDisabled":"yes"}}`,
		"non-array watchExcludes":          `{"watcher":{"watchExcludes":"node_modules"}}`,
		"empty watchExcludes pattern":      `{"watcher":{"watchExcludes":[""]}}`,
		"unknown watcher backend":          `{"watcher":{"backend":"kqueue"}}`,
		"misspelled watcher backend":       `{"watcher":{"backend":"inotfy"}}`,
		"empty watcher backend":            `{"watcher":{"backend":""}}`,
		"non-string watcher backend":       `{"watcher":{"backend":true}}`,
		"priors key typo":                  `{"search":{"priors":{"enabld":true}}}`,
		"non-bool priors enabled":          `{"search":{"priors":{"enabled":"yes"}}}`,
		"non-object priors":                `{"search":{"priors":true}}`,
		"unknown priors key":               `{"search":{"priors":{"enabled":true,"halfLifeDays":7}}}`,
		"priors misspelled in search":      `{"search":{"prios":{"enabled":true}}}`,
		"arbiter key typo":                 `{"search":{"arbiter":{"enabld":true}}}`,
		"non-bool arbiter enabled":         `{"search":{"arbiter":{"enabled":"yes"}}}`,
		"non-object arbiter":               `{"search":{"arbiter":true}}`,
		"unknown arbiter key":              `{"search":{"arbiter":{"enabled":true,"minPicks":10}}}`,
		"frecency key typo":                `{"search":{"frecency":{"halfLifeDay":7}}}`,
		"non-bool frecency disabled":       `{"search":{"frecency":{"disabled":"yes"}}}`,
		"zero frecency half-life":          `{"search":{"frecency":{"halfLifeDays":0}}}`,
		"negative frecency half-life":      `{"search":{"frecency":{"halfLifeDays":-1}}}`,
		"zero frecency weight":             `{"search":{"frecency":{"weightFrecency":0}}}`,
		"zero recency weight":              `{"search":{"frecency":{"weightRecency":0}}}`,
		"zero cwd weight":                  `{"search":{"frecency":{"weightCwd":0}}}`,
		"zero noise weight":                `{"search":{"frecency":{"weightNoise":0}}}`,
		"zero tier jump":                   `{"search":{"frecency":{"tierJumpCount":0}}}`,
		"non-number frecency weight":       `{"search":{"frecency":{"weightNoise":"1"}}}`,
		"non-bool telemetry enabled":       `{"search":{"telemetry":{"enabled":"yes"}}}`,
		"zero telemetry maxSizeKB":         `{"search":{"telemetry":{"maxSizeKB":0}}}`,
		"negative telemetry maxSizeKB":     `{"search":{"telemetry":{"maxSizeKB":-1}}}`,
		"telemetry key typo":               `{"search":{"telemetry":{"retainQuerys":true}}}`,
		"non-bool retainQueries":           `{"search":{"telemetry":{"retainQueries":"yes"}}}`,
		"bad theme name":                   `{"theme":"../evil"}`,
		"bad plugin entry id":              `{"plugins":{"entries":{"Bad ID":{}}}}`,
		"non-object settings":              `{"plugins":{"entries":{"calc":{"settings":"loud"}}}}`,
		"unknown top-level typo":           `{"maxResluts":50}`,
		"tray disabled typo":               `{"tray":{"disabld":true}}`,
		"non-bool tray disabled":           `{"tray":{"disabled":"yes"}}`,
		"history persist typo":             `{"history":{"persistDisabld":true}}`,
		"non-bool history persistDisabled": `{"history":{"persistDisabled":"yes"}}`,
		"stats disabled typo":              `{"stats":{"disabld":true}}`,
		"non-bool stats disabled":          `{"stats":{"disabled":"yes"}}`,
		"window translucent typo":          `{"window":{"translucnet":true}}`,
		"non-bool window translucent":      `{"window":{"translucent":"yes"}}`,
		"zero window width":                `{"window":{"width":0}}`,
		"too-small window height":          `{"window":{"height":100}}`,
		"non-integer window width":         `{"window":{"width":"780"}}`,
		"zero firefox month":               `{"firefox":{"frequentSites":{"minVisitsMonth":0}}}`,
		"negative firefox week":            `{"firefox":{"frequentSites":{"minVisitsWeek":-1}}}`,
		"zero firefox refresh":             `{"firefox":{"frequentSites":{"refreshMinutes":0}}}`,
		"zero firefox max":                 `{"firefox":{"frequentSites":{"maxResults":0}}}`,
		"firefox key typo":                 `{"firefox":{"frequentSites":{"profileDirr":"/x"}}}`,
		"non-string profileDir":            `{"firefox":{"frequentSites":{"profileDir":7}}}`,
		"unknown firefox block":            `{"firefox":{"telemetry":{}}}`,
		"zero openTabs max":                `{"firefox":{"openTabs":{"maxResults":0}}}`,
		"negative openTabs max":            `{"firefox":{"openTabs":{"maxResults":-2}}}`,
		"openTabs key typo":                `{"firefox":{"openTabs":{"maxResluts":6}}}`,
		"non-string tabs dir":              `{"firefox":{"openTabs":{"profileDir":7}}}`,
		"non-bool preview enabled":         `{"preview":{"enabled":"yes"}}`,
		"zero preview windowWidth":         `{"preview":{"windowWidth":0}}`,
		"negative preview windowHeight":    `{"preview":{"windowHeight":-1}}`,
		"zero preview textMaxKB":           `{"preview":{"textMaxKB":0}}`,
		"zero preview imageMaxEdge":        `{"preview":{"imageMaxEdge":0}}`,
		"zero preview dirMaxEntries":       `{"preview":{"dirMaxEntries":0}}`,
		"preview key typo":                 `{"preview":{"enbled":true}}`,
		"non-string kagi apiKey":           `{"preview":{"kagi":{"apiKey":7}}}`,
		"non-string kagi baseUrl":          `{"preview":{"kagi":{"baseUrl":7}}}`,
		"zero kagi maxResults":             `{"preview":{"kagi":{"maxResults":0}}}`,
		"kagi key typo":                    `{"preview":{"kagi":{"apikey":"x"}}}`,
		"non-string openai baseUrl":        `{"preview":{"openai":{"baseUrl":false}}}`,
		"empty openai model":               `{"preview":{"openai":{"model":""}}}`,
		"zero openai maxOutputTokens":      `{"preview":{"openai":{"maxOutputTokens":0}}}`,
		"openai key typo":                  `{"preview":{"openai":{"modle":"gpt-5-mini"}}}`,
	}
	for name, doc := range cases {
		require.Error(t, validateConfigJSON(sch, []byte(doc)), "case %q must fail validation", name)
	}

	// The "$schema" editor hint is explicitly allowed.
	require.NoError(t, validateConfigJSON(sch, []byte(
		`{"$schema":"https://raw.githubusercontent.com/wow-look-at-my/competent-search-thing/master/schemas/config.schema.json","theme":"dark"}`,
	)))
}

func configJSONTagNames(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	require.Equal(t, reflect.Struct, typ.Kind())
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
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

func configSchemaProperties(t *testing.T, defName string) []string {
	t.Helper()
	data, err := os.ReadFile(configSchemaPath(t))
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	node := doc
	if defName != "" {
		defs, ok := doc["$defs"].(map[string]any)
		require.True(t, ok)
		node, ok = defs[defName].(map[string]any)
		require.True(t, ok, "config schema needs $defs/%s", defName)
	}
	props, ok := node["properties"].(map[string]any)
	require.True(t, ok)
	var names []string
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// TestConfigSchemaKeyCompleteness is the drift guard: every json tag
// on the config structs appears in the schema and vice versa.
func TestConfigSchemaKeyCompleteness(t *testing.T) {
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(Config{})), configSchemaProperties(t, ""),
		"config.schema.json top level out of sync with Config")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(SearchConfig{})), configSchemaProperties(t, "searchConfig"),
		"config.schema.json $defs/searchConfig out of sync with SearchConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(WatcherConfig{})), configSchemaProperties(t, "watcherConfig"),
		"config.schema.json $defs/watcherConfig out of sync with WatcherConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(FrecencyConfig{})), configSchemaProperties(t, "frecencyConfig"),
		"config.schema.json $defs/frecencyConfig out of sync with FrecencyConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PriorsConfig{})), configSchemaProperties(t, "priorsConfig"),
		"config.schema.json $defs/priorsConfig out of sync with PriorsConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(ArbiterConfig{})), configSchemaProperties(t, "arbiterConfig"),
		"config.schema.json $defs/arbiterConfig out of sync with ArbiterConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(TelemetryConfig{})), configSchemaProperties(t, "searchTelemetryConfig"),
		"config.schema.json $defs/searchTelemetryConfig out of sync with TelemetryConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PluginsConfig{})), configSchemaProperties(t, "pluginsConfig"),
		"config.schema.json $defs/pluginsConfig out of sync with PluginsConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PluginEntry{})), configSchemaProperties(t, "pluginEntry"),
		"config.schema.json $defs/pluginEntry out of sync with PluginEntry")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(BangsConfig{})), configSchemaProperties(t, "bangsConfig"),
		"config.schema.json $defs/bangsConfig out of sync with BangsConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(TrayConfig{})), configSchemaProperties(t, "trayConfig"),
		"config.schema.json $defs/trayConfig out of sync with TrayConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(HistoryConfig{})), configSchemaProperties(t, "historyConfig"),
		"config.schema.json $defs/historyConfig out of sync with HistoryConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(StatsConfig{})), configSchemaProperties(t, "statsConfig"),
		"config.schema.json $defs/statsConfig out of sync with StatsConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(WindowConfig{})), configSchemaProperties(t, "windowConfig"),
		"config.schema.json $defs/windowConfig out of sync with WindowConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(FirefoxConfig{})), configSchemaProperties(t, "firefoxConfig"),
		"config.schema.json $defs/firefoxConfig out of sync with FirefoxConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(FrequentSitesConfig{})), configSchemaProperties(t, "frequentSitesConfig"),
		"config.schema.json $defs/frequentSitesConfig out of sync with FrequentSitesConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(OpenTabsConfig{})), configSchemaProperties(t, "openTabsConfig"),
		"config.schema.json $defs/openTabsConfig out of sync with OpenTabsConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(RewriteRule{})), configSchemaProperties(t, "rewriteRule"),
		"config.schema.json $defs/rewriteRule out of sync with RewriteRule")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PreviewConfig{})), configSchemaProperties(t, "previewConfig"),
		"config.schema.json $defs/previewConfig out of sync with PreviewConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PreviewKagiConfig{})), configSchemaProperties(t, "previewKagiConfig"),
		"config.schema.json $defs/previewKagiConfig out of sync with PreviewKagiConfig")
	require.Equal(t, configJSONTagNames(t, reflect.TypeOf(PreviewOpenAIConfig{})), configSchemaProperties(t, "previewOpenAIConfig"),
		"config.schema.json $defs/previewOpenAIConfig out of sync with PreviewOpenAIConfig")
}
