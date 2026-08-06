package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The v9 step: three built-in AI providers collapse into ONE
// user-named endpoint. What a user configured keeps working, the
// change is announced, and nothing is invented for a config that
// never configured an AI provider.

// loadRawConfig writes raw into the config dir and Loads it.
func loadRawConfig(t *testing.T, raw string) Config {
	t.Helper()
	dir := setConfigDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0o644))
	c, err := Load()
	require.NoError(t, err)
	return c
}

func TestMigrateV9CarriesTheSelectedProvider(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantKey   string
		wantBase  string
		wantModel string
		wantNote  string
	}{
		{
			name:      "openai by default selector",
			raw:       `{"rootsVersion":8,"preview":{"openai":{"apiKey":"sk-o","model":"gpt-5-mini","maxOutputTokens":1024}}}`,
			wantKey:   "sk-o",
			wantBase:  openAICompatBase,
			wantModel: "gpt-5-mini",
			wantNote:  "your openai settings moved to preview.ai",
		},
		{
			name:      "anthropic selected",
			raw:       `{"rootsVersion":8,"preview":{"aiProvider":"anthropic","anthropic":{"apiKey":"sk-ant","model":"claude-haiku-4-5"}}}`,
			wantKey:   "sk-ant",
			wantBase:  anthropicCompatBase,
			wantModel: "claude-haiku-4-5",
			wantNote:  "your anthropic settings moved to preview.ai",
		},
		{
			name:      "custom selected keeps its own base",
			raw:       `{"rootsVersion":8,"preview":{"aiProvider":"custom","custom":{"baseUrl":"http://localhost:11434/v1","model":"llama3"}}}`,
			wantBase:  "http://localhost:11434/v1",
			wantModel: "llama3",
			wantNote:  "your custom settings moved to preview.ai",
		},
		{
			name:      "a configured base URL beats the built-in endpoint",
			raw:       `{"rootsVersion":8,"preview":{"openai":{"apiKey":"sk-o","baseUrl":"https://proxy.example/v1","model":"m"}}}`,
			wantKey:   "sk-o",
			wantBase:  "https://proxy.example/v1",
			wantModel: "m",
			wantNote:  "your openai settings moved to preview.ai",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := loadRawConfig(t, tc.raw)
			require.Equal(t, tc.wantKey, c.Preview.AI.APIKey)
			require.Equal(t, tc.wantBase, c.Preview.AI.BaseURL)
			require.Equal(t, tc.wantModel, c.Preview.AI.Model)
			require.Equal(t, CurrentRootsVersion(), c.RootsVersion)
			require.Contains(t, strings.Join(c.MigrationNotes, "\n"), tc.wantNote,
				"the collapse is announced -- the scope never changes silently")

			// The migrated value is what a re-Load reads back, and the
			// step never fires twice.
			again, err := Load()
			require.NoError(t, err)
			require.Equal(t, c.Preview.AI, again.Preview.AI)
			require.NotContains(t, strings.Join(again.MigrationNotes, "\n"), "moved to preview.ai",
				"the step is gated on its own version and never re-announces")
		})
	}
}

func TestMigrateV9WarnsAboutTheChangedWireShape(t *testing.T) {
	// A hand-typed base URL may now point at the wrong path (requests
	// go to <baseUrl>/chat/completions), so the step says so.
	c := loadRawConfig(t, `{"rootsVersion":8,"preview":{"aiProvider":"custom","custom":{"baseUrl":"http://localhost:11434","model":"llama3"}}}`)
	notes := strings.Join(c.MigrationNotes, "\n")
	require.Contains(t, notes, "<baseUrl>/chat/completions")

	// A built-in provider with no hand-typed base needs no such
	// warning -- its endpoint was filled in by the step itself.
	d := loadRawConfig(t, `{"rootsVersion":8,"preview":{"openai":{"apiKey":"sk","model":"m"}}}`)
	require.NotContains(t, strings.Join(d.MigrationNotes, "\n"), "chat/completions")
}

func TestMigrateV9NamesTheOneKeyVariable(t *testing.T) {
	// A key-less built-in provider was reading OPENAI_API_KEY /
	// ANTHROPIC_API_KEY; those are gone, so name the replacement.
	c := loadRawConfig(t, `{"rootsVersion":8,"preview":{"openai":{"model":"m"}}}`)
	require.Contains(t, strings.Join(c.MigrationNotes, "\n"), "COMPETENT_SEARCH_AI_API_KEY")
}

func TestMigrateV9AnnouncesACollapseWithNothingToCarry(t *testing.T) {
	// The selected provider was never configured but another section
	// existed: nothing to carry, but the user still has to hear that
	// their sections are gone.
	c := loadRawConfig(t, `{"rootsVersion":8,"preview":{"aiProvider":"anthropic","openai":{"apiKey":"sk"}}}`)
	require.Equal(t, PreviewAIConfig{MaxOutputTokens: DefaultPreviewAITokens}, c.Preview.AI,
		"nothing is carried from an unselected section")
	require.Contains(t, strings.Join(c.MigrationNotes, "\n"), "collapsed into one endpoint")
}

func TestMigrateV9SaysNothingWhenThereWasNoAIConfig(t *testing.T) {
	// The overwhelmingly common case: the pane was never wired to an
	// AI provider. There is nothing to migrate and nothing to say.
	c := loadRawConfig(t, `{"rootsVersion":8,"preview":{"textMaxKB":128}}`)
	require.Equal(t, PreviewAIConfig{MaxOutputTokens: DefaultPreviewAITokens}, c.Preview.AI)
	require.NotContains(t, strings.Join(c.MigrationNotes, "\n"), "preview.ai")
	require.Equal(t, CurrentRootsVersion(), c.RootsVersion, "the version stamp still advances")
}

func TestMigrateV9LeavesAHandWrittenAISectionAlone(t *testing.T) {
	// Someone already wrote the new shape by hand (with a stale
	// version stamp): it wins outright, old sections ignored.
	c := loadRawConfig(t, `{"rootsVersion":8,"preview":{"aiProvider":"openai","openai":{"apiKey":"sk-old"},`+
		`"ai":{"apiKey":"sk-new","baseUrl":"https://mine.example/v1","model":"mine"}}}`)
	require.Equal(t, "sk-new", c.Preview.AI.APIKey)
	require.Equal(t, "https://mine.example/v1", c.Preview.AI.BaseURL)
	require.Equal(t, "mine", c.Preview.AI.Model)
	require.NotContains(t, strings.Join(c.MigrationNotes, "\n"), "moved to preview.ai")
}

func TestMigrateV9SurvivesUnreadableRawBytes(t *testing.T) {
	// The step reads the RAW document; a shape it cannot decode leaves
	// the parsed config standing rather than panicking or clearing it.
	c := Config{Preview: PreviewConfig{AI: PreviewAIConfig{Model: "kept"}}}
	c.migrateAIProvider([]byte(`{"preview":"not an object"}`))
	require.Equal(t, "kept", c.Preview.AI.Model)
	require.Empty(t, c.MigrationNotes)
}

func TestMigrateV9RetiredKeysAreDroppedOnSave(t *testing.T) {
	// The old sections are not struct fields any more, so a save drops
	// them -- and UnknownKeys reports exactly that to the editor.
	raw := `{"rootsVersion":9,"preview":{"aiProvider":"openai","openai":{"apiKey":"sk"}}}`
	require.Equal(t,
		[]string{"preview.aiProvider", "preview.openai"},
		UnknownKeys(json.RawMessage(raw)))
}
