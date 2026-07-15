package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writePlugin creates root/dirName/manifest.json with content.
func writePlugin(t *testing.T, root, dirName, content string) string {
	t.Helper()
	d := filepath.Join(root, dirName)
	require.NoError(t, os.MkdirAll(d, 0o755))
	p := filepath.Join(d, "manifest.json")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// loadOne loads a single manifest through the real LoadDir pipeline.
func loadOne(t *testing.T, content string) (*Manifest, error) {
	t.Helper()
	root := t.TempDir()
	writePlugin(t, root, "p", content)
	ms, errs := LoadDir(root)
	if len(errs) > 0 {
		require.Empty(t, ms, "a single broken manifest must not load")
		require.Len(t, errs, 1)
		return nil, errs[0]
	}
	require.Len(t, ms, 1)
	return ms[0], nil
}

func TestManifestValidCommandDefaults(t *testing.T) {
	m, err := loadOne(t, `{
		"id": "calc",
		"type": "command",
		"command": {"argv": ["python3", "calc.py"]},
		"trigger": {"prefix": "="}
	}`)
	require.NoError(t, err)
	require.Equal(t, 1, m.V, "missing v defaults to 1")
	require.Equal(t, "calc", m.ID)
	require.Equal(t, "calc", m.Name, "name defaults to id")
	require.Equal(t, TypeCommand, m.Type)
	require.Equal(t, []string{"python3", "calc.py"}, m.Command.Argv)
	require.Equal(t, 1500, m.TimeoutMS, "timeout defaults to 1500")
	require.Equal(t, []string{"calc"}, m.Bangs, "bangs default to [id]")
	require.Nil(t, m.Context)
	require.False(t, m.AllowRunCommand)
	require.NotEmpty(t, m.Dir)
	require.Equal(t, "p", filepath.Base(m.Dir), "Dir points at the manifest directory")

	// The trigger came out compiled: Match works immediately.
	stripped, ok := m.Trigger.Match("=2+2", nil)
	require.True(t, ok)
	require.Equal(t, "2+2", stripped)
}

func TestManifestValidHTTPExplicit(t *testing.T) {
	m, err := loadOne(t, `{
		"v": 1,
		"id": "color",
		"name": "Color Swatch",
		"type": "http",
		"http": {"url": "http://127.0.0.1:8765/query", "headers": {"X-Api-Key": "k"}},
		"trigger": {"prefix": "#"},
		"bangs": ["Color", "col", "color"],
		"context": ["focused", "running", "installed"],
		"timeout_ms": 5000,
		"allow_run_command": true
	}`)
	require.NoError(t, err)
	require.Equal(t, "Color Swatch", m.Name)
	require.Equal(t, TypeHTTP, m.Type)
	require.Equal(t, "http://127.0.0.1:8765/query", m.HTTP.URL)
	require.Equal(t, map[string]string{"X-Api-Key": "k"}, m.HTTP.Headers)
	require.Equal(t, []string{"color", "col"}, m.Bangs, "bangs lowercased and deduped, order kept")
	require.Equal(t, []string{"focused", "running", "installed"}, m.Context)
	require.Equal(t, 5000, m.TimeoutMS)
	require.True(t, m.AllowRunCommand)
}

func TestManifestValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "bad json", content: `{not json`, wantErr: "parsing"},
		{
			name:    "future version",
			content: `{"v": 2, "id": "x", "type": "command", "command": {"argv": ["a"]}}`,
			wantErr: "unsupported manifest version 2",
		},
		{
			name:    "missing id",
			content: `{"type": "command", "command": {"argv": ["a"]}}`,
			wantErr: `id ""`,
		},
		{
			name:    "uppercase id",
			content: `{"id": "Calc", "type": "command", "command": {"argv": ["a"]}}`,
			wantErr: `id "Calc"`,
		},
		{
			name:    "id starting with dash",
			content: `{"id": "-x", "type": "command", "command": {"argv": ["a"]}}`,
			wantErr: "id",
		},
		{
			name:    "id too long",
			content: `{"id": "a234567890123456789012345678901234", "type": "command", "command": {"argv": ["a"]}}`,
			wantErr: "id",
		},
		{name: "missing type", content: `{"id": "x"}`, wantErr: `type ""`},
		{
			name:    "unknown type",
			content: `{"id": "x", "type": "socket"}`,
			wantErr: `type "socket"`,
		},
		{
			name:    "command without section",
			content: `{"id": "x", "type": "command"}`,
			wantErr: "command.argv",
		},
		{
			name:    "command empty argv",
			content: `{"id": "x", "type": "command", "command": {"argv": []}}`,
			wantErr: "command.argv",
		},
		{
			name:    "command empty argv entry",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a", ""]}}`,
			wantErr: "command.argv[1]",
		},
		{
			name:    "http without section",
			content: `{"id": "x", "type": "http"}`,
			wantErr: "http.url",
		},
		{
			name:    "http garbage url",
			content: `{"id": "x", "type": "http", "http": {"url": "not a url"}}`,
			wantErr: "http.url",
		},
		{
			name:    "http ftp scheme",
			content: `{"id": "x", "type": "http", "http": {"url": "ftp://example.com/q"}}`,
			wantErr: "http.url",
		},
		{
			name:    "http url without host",
			content: `{"id": "x", "type": "http", "http": {"url": "http://"}}`,
			wantErr: "http.url",
		},
		{
			name:    "bad trigger regex",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "trigger": {"regex": "(["}}`,
			wantErr: "trigger.regex",
		},
		{
			name:    "focused gate both empty",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "trigger": {"prefix": "=", "focused_app": {}}}`,
			wantErr: "trigger.focused_app",
		},
		{
			name:    "empty bangs and no trigger",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "bangs": []}`,
			wantErr: "unreachable",
		},
		{
			name:    "invalid bang",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "bangs": ["-bad"]}`,
			wantErr: `bang "-bad"`,
		},
		{
			name:    "invalid context part",
			content: `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "context": ["clipboard"]}`,
			wantErr: `context "clipboard"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadOne(t, tt.content)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Contains(t, err.Error(), "manifest.json", "errors are path-prefixed")
		})
	}
}

func TestManifestTimeoutClamps(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "missing defaults", in: "", want: 1500},
		{name: "explicit kept", in: `"timeout_ms": 500,`, want: 500},
		{name: "too small clamped", in: `"timeout_ms": 50,`, want: 100},
		{name: "negative clamped", in: `"timeout_ms": -5,`, want: 100},
		{name: "too big clamped", in: `"timeout_ms": 20000,`, want: 10000},
		{name: "max kept", in: `"timeout_ms": 10000,`, want: 10000},
		{name: "min kept", in: `"timeout_ms": 100,`, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := loadOne(t, `{"id": "x", "type": "command", "command": {"argv": ["a"]}, `+tt.in+` "trigger": {"prefix": "="}}`)
			require.NoError(t, err)
			require.Equal(t, tt.want, m.TimeoutMS)
		})
	}
}

func TestManifestBangsAndContextNormalization(t *testing.T) {
	// Explicit empty bangs are legal with a trigger: trigger-only plugin.
	m, err := loadOne(t, `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "bangs": [], "trigger": {"prefix": "="}}`)
	require.NoError(t, err)
	require.Empty(t, m.Bangs)

	// No trigger but default bangs: bang-targeted-only plugin.
	m, err = loadOne(t, `{"id": "ps", "type": "command", "command": {"argv": ["a"]}}`)
	require.NoError(t, err)
	require.Nil(t, m.Trigger)
	require.Equal(t, []string{"ps"}, m.Bangs)

	// Context deduped, order kept.
	m, err = loadOne(t, `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "context": ["running", "focused", "running"]}`)
	require.NoError(t, err)
	require.Equal(t, []string{"running", "focused"}, m.Context)

	// Explicit empty context stays empty.
	m, err = loadOne(t, `{"id": "x", "type": "command", "command": {"argv": ["a"]}, "context": []}`)
	require.NoError(t, err)
	require.Empty(t, m.Context)
}

func TestLoadDirMissingAndEmpty(t *testing.T) {
	ms, errs := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
	require.Nil(t, ms)
	require.Nil(t, errs)

	ms, errs = LoadDir(t.TempDir())
	require.Nil(t, ms)
	require.Nil(t, errs)
}

func TestLoadDirOnFileReportsError(t *testing.T) {
	f := filepath.Join(t.TempDir(), "plugins")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
	ms, errs := LoadDir(f)
	require.Nil(t, ms)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "plugins dir")
}

func TestLoadDirSkipsNonPlugins(t *testing.T) {
	root := t.TempDir()
	// A stray file and a directory without a manifest are silently ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.txt"), []byte("hi"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "empty-dir"), 0o755))
	writePlugin(t, root, "real", `{"id": "x", "type": "command", "command": {"argv": ["a"]}}`)

	ms, errs := LoadDir(root)
	require.Empty(t, errs)
	require.Len(t, ms, 1)
	require.Equal(t, "x", ms[0].ID)
}

func TestLoadDirSortedAndIsolatesErrors(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "b-second", `{"id": "beta", "type": "command", "command": {"argv": ["a"]}}`)
	writePlugin(t, root, "a-first", `{"id": "alpha", "type": "command", "command": {"argv": ["a"]}}`)
	broken := writePlugin(t, root, "broken", `{nope`)

	ms, errs := LoadDir(root)
	require.Len(t, ms, 2, "valid manifests load despite a broken sibling")
	require.Equal(t, "alpha", ms[0].ID, "directories load in sorted order")
	require.Equal(t, "beta", ms[1].ID)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), broken)
}

func TestLoadDirDuplicateIDFirstWins(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "zz-later", `{"id": "same", "name": "Later", "type": "command", "command": {"argv": ["a"]}}`)
	writePlugin(t, root, "aa-early", `{"id": "same", "name": "Early", "type": "command", "command": {"argv": ["a"]}}`)

	ms, errs := LoadDir(root)
	require.Len(t, ms, 1)
	require.Equal(t, "Early", ms[0].Name, "alphabetically-first directory wins")
	require.Equal(t, filepath.Join(root, "aa-early"), ms[0].Dir)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "duplicate plugin id")
	require.Contains(t, errs[0].Error(), filepath.Join(root, "zz-later", "manifest.json"))
	require.Contains(t, errs[0].Error(), filepath.Join(root, "aa-early", "manifest.json"))
}

func TestLoadDirManifestIsDirectory(t *testing.T) {
	root := t.TempDir()
	// plugins/weird/manifest.json is itself a directory: unreadable.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "weird", "manifest.json"), 0o755))
	ms, errs := LoadDir(root)
	require.Empty(t, ms)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), filepath.Join(root, "weird", "manifest.json"))
}
