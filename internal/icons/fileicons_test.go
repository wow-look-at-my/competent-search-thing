package icons

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// matURI is the expected data URI for one embedded pack SVG.
func matURI(t *testing.T, file string) string {
	t.Helper()
	data, err := materialFS.ReadFile("material/svg/" + file)
	require.NoError(t, err, "embedded pack svg %s", file)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(data)
}

// themedService is a fixture service whose theme getter answers name.
func themedService(t *testing.T, name string) *Service {
	t.Helper()
	f := newFixture(t)
	opt := f.options(t)
	opt.Theme = func() string { return name }
	return NewService(opt)
}

func TestMaterialFileIconNameLookups(t *testing.T) {
	p, err := loadMaterial()
	require.NoError(t, err)
	cases := []struct {
		base, want, why string
	}{
		{"main.go", "go", "simple extension"},
		{"style.css", "css", "simple extension"},
		{"report.pdf", "pdf", "simple extension"},
		{"data.csv", "table", "simple extension"},
		{"app.test.tsx", "test-jsx", "compound extension beats tsx"},
		{"types.d.ts", "typescript-def", "compound extension beats ts"},
		{"MAIN.GO", "go", "case-insensitive extension"},
		{"Dockerfile", "docker", "special filename"},
		{"GO.MOD", "go-mod", "case-insensitive special filename"},
		{"package.json", "nodejs", "special filename beats the json extension"},
		{"README.md", "readme", "special filename beats the md extension"},
		{".gitignore", "git", "dotfile special filename"},
		{"mystery.zzznotreal", "file", "unknown extension = default"},
		{"noextension", "file", "extensionless = default"},
		{"trailingdot.", "file", "trailing dot = default"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, p.fileIconName(tc.base), "%s: %s", tc.base, tc.why)
	}
}

func TestMaterialDirAndDefaults(t *testing.T) {
	svc := themedService(t, "dark")
	got := svc.Resolve([]string{"dir", "file:whatever.zzznotreal", "file:"}, 16)
	require.Equal(t, matURI(t, "folder.svg"), got["dir"], "dir = the pack folder icon")
	require.Equal(t, matURI(t, "file.svg"), got["file:whatever.zzznotreal"],
		"unknown extension = the pack default file icon")
	require.NotContains(t, got, "file:", "an empty basename stays a miss")
}

func TestMaterialLightVariantSelection(t *testing.T) {
	p, err := loadMaterial()
	require.NoError(t, err)
	require.True(t, p.light["blink"], "fixture assumption: blink is light-flagged upstream")
	require.False(t, p.light["go"], "fixture assumption: go has no light variant")

	dark := themedService(t, "dark")
	light := themedService(t, "light")
	darkGot := dark.Resolve([]string{"file:a.blink", "file:main.go", "dir"}, 16)
	lightGot := light.Resolve([]string{"file:a.blink", "file:main.go", "dir"}, 16)

	require.Equal(t, matURI(t, "blink.svg"), darkGot["file:a.blink"])
	require.Equal(t, matURI(t, "blink_light.svg"), lightGot["file:a.blink"],
		"the light theme serves the _light twin of flagged icons")
	require.Equal(t, darkGot["file:main.go"], lightGot["file:main.go"],
		"unflagged icons are identical on both themes")
	require.Equal(t, darkGot["dir"], lightGot["dir"],
		"the folder icon has no light twin")
}

func TestMaterialThemeConsultedPerBatch(t *testing.T) {
	f := newFixture(t)
	theme := "dark"
	opt := f.options(t)
	opt.Theme = func() string { return theme }
	svc := NewService(opt)

	require.Equal(t, matURI(t, "blink.svg"), svc.Resolve([]string{"file:a.blink"}, 16)["file:a.blink"])
	theme = "light"
	require.Equal(t, matURI(t, "blink_light.svg"), svc.Resolve([]string{"file:a.blink"}, 16)["file:a.blink"],
		"a live theme flip re-resolves the variant (cache keys carry the variant file)")
	theme = "dark"
	require.Equal(t, matURI(t, "blink.svg"), svc.Resolve([]string{"file:a.blink"}, 16)["file:a.blink"],
		"both variants coexist in the cache")
}

func TestMaterialDataURIRoundTrip(t *testing.T) {
	svc := themedService(t, "")
	uri := svc.Resolve([]string{"file:main.go"}, 16)["file:main.go"]
	require.True(t, strings.HasPrefix(uri, "data:image/svg+xml;base64,"))
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/svg+xml;base64,"))
	require.NoError(t, err)
	want, err := materialFS.ReadFile("material/svg/go.svg")
	require.NoError(t, err)
	require.Equal(t, want, decoded, "the URI carries the embedded bytes verbatim")
	require.Contains(t, string(decoded), "<svg", "and they are an svg document")
}

/* --- integrity gates over the embedded pack ------------------------- */

// Byte budgets: the balloon tripwires. Measured at vendoring time:
// 609 files, 511983 bytes total, largest file travis.svg at 21722
// bytes (the pack's own maximum -- which is why the per-file cap sits
// at 22 KiB). A pack refresh that balloons past these fails here
// first; re-measure and bump deliberately, never blindly.
const (
	materialMaxFileBytes  = 22 * 1024
	materialMaxTotalBytes = 524288 // 512 KiB
)

func TestMaterialIntegrity(t *testing.T) {
	p, err := loadMaterial()
	require.NoError(t, err, "the embedded mapping parses")
	require.NotEmpty(t, p.mapping.FileExtensions)
	require.NotEmpty(t, p.mapping.FileNames)

	embedded := map[string]bool{}
	total := 0
	entries, err := fs.ReadDir(materialFS, "material/svg")
	require.NoError(t, err)
	for _, e := range entries {
		data, err := materialFS.ReadFile("material/svg/" + e.Name())
		require.NoError(t, err)
		embedded[e.Name()] = true
		total += len(data)
		require.LessOrEqual(t, len(data), materialMaxFileBytes,
			"%s exceeds the per-file cap", e.Name())
		for i, b := range data {
			require.True(t, b >= 0x20 && b <= 0x7e || b == '\n' || b == '\t',
				"%s: non-ASCII or control byte 0x%02x at %d", e.Name(), b, i)
		}
	}
	require.LessOrEqual(t, total, materialMaxTotalBytes, "total embedded bytes")

	// Every mapping value (and the two defaults) resolves to an
	// embedded SVG -- no dangling references.
	want := map[string]bool{}
	values := []string{p.mapping.DefaultFile, p.mapping.Folder}
	for _, v := range p.mapping.FileExtensions {
		values = append(values, v)
	}
	for _, v := range p.mapping.FileNames {
		values = append(values, v)
	}
	valueSet := map[string]bool{}
	for _, v := range values {
		require.True(t, embedded[v+".svg"], "mapping value %q has no embedded svg", v)
		want[v+".svg"] = true
		valueSet[v] = true
	}
	// Every light-flagged name is a shipped mapping value and has its
	// _light twin embedded.
	for _, name := range p.mapping.Light {
		require.True(t, valueSet[name], "light-flagged %q is not a mapping value", name)
		require.True(t, embedded[name+"_light.svg"], "light-flagged %q has no _light.svg", name)
		want[name+"_light.svg"] = true
	}
	// And the reverse: nothing embedded is unreachable dead weight.
	for f := range embedded {
		require.True(t, want[f], "embedded %s is unreachable from the mapping", f)
	}
}

// TestMaterialMappingKeysNormalized pins the converter's key
// contract the resolver relies on: lowercase, no slash-scoped file
// names, and the raw file bytes pure ASCII (non-ASCII keys arrive
// \u-escaped).
func TestMaterialMappingKeysNormalized(t *testing.T) {
	p, err := loadMaterial()
	require.NoError(t, err)
	for k := range p.mapping.FileExtensions {
		require.Equal(t, strings.ToLower(k), k, "extension key %q not lowercase", k)
	}
	for k := range p.mapping.FileNames {
		require.Equal(t, strings.ToLower(k), k, "file name key %q not lowercase", k)
		require.NotContains(t, k, "/", "file name key %q is path-scoped", k)
	}
	raw, err := materialFS.ReadFile("material/mapping.json")
	require.NoError(t, err)
	for i, b := range raw {
		require.True(t, b >= 0x20 && b <= 0x7e || b == '\n',
			"mapping.json: non-ASCII byte 0x%02x at %d", b, i)
	}
	var m materialMapping
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, "file", m.DefaultFile)
	require.Equal(t, "folder", m.Folder)
}
