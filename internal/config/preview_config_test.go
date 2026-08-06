package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// The preview section's defaults, Normalize repairs, and round-trip
// -- split from config_test.go for the file-length budget.

func TestPreviewConfig(t *testing.T) {
	setConfigDir(t)
	require.Equal(t, PreviewConfig{
		Enabled:       Bool(true),
		WindowWidth:   1100,
		WindowHeight:  700,
		TextMaxKB:     256,
		ImageMaxEdge:  800,
		DirMaxEntries: 200,
		Kagi:          PreviewKagiConfig{MaxResults: 8},
		AI:            PreviewAIConfig{MaxOutputTokens: 1024},
	}, Default().Preview,
		"the preview pane defaults ON (v8) with every knob populated -- except the AI endpoint and model, which have NO default: nothing is sent anywhere until the user names a server")

	// A config predating the preview block normalizes to the defaults
	// -- which since v8 means the pane is ON (nil repairs to true).
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.Equal(t, DefaultPreview(), c.Preview)

	// Zero and negative knobs are repaired; real values -- the API
	// keys and base URLs verbatim included -- survive. The base URLs
	// are passthrough like the Firefox profileDirs: Normalize never
	// touches them (validation happens at the consumer), and an odd
	// spelling -- trailing slash included -- survives byte-for-byte.
	c = Config{Preview: PreviewConfig{
		Enabled:       Bool(true),
		WindowWidth:   0,
		WindowHeight:  -1,
		TextMaxKB:     512,
		ImageMaxEdge:  0,
		DirMaxEntries: 50,
		Kagi: PreviewKagiConfig{
			APIKey:     "kagi-secret",
			BaseURL:    "https://kagi.internal.example/",
			MaxResults: 0,
		},
		AI: PreviewAIConfig{
			APIKey:          "sk-secret",
			BaseURL:         "not even a url",
			Model:           "",
			MaxOutputTokens: -5,
		},
	}}
	c.Normalize()
	require.Equal(t, Bool(true), c.Preview.Enabled)
	require.Equal(t, DefaultPreviewWindowWidth, c.Preview.WindowWidth)
	require.Equal(t, DefaultPreviewWindowHeight, c.Preview.WindowHeight)
	require.Equal(t, 512, c.Preview.TextMaxKB)
	require.Equal(t, DefaultPreviewImageMaxEdge, c.Preview.ImageMaxEdge)
	require.Equal(t, 50, c.Preview.DirMaxEntries)
	require.Equal(t, "kagi-secret", c.Preview.Kagi.APIKey, "the key is never touched")
	require.Equal(t, "https://kagi.internal.example/", c.Preview.Kagi.BaseURL, "the base URL is never touched")
	require.Equal(t, DefaultPreviewKagiMax, c.Preview.Kagi.MaxResults)
	require.Equal(t, "sk-secret", c.Preview.AI.APIKey, "the key is never touched")
	require.Equal(t, "not even a url", c.Preview.AI.BaseURL, "the base URL is never touched")
	require.Equal(t, "", c.Preview.AI.Model,
		"ai.model has no invented default -- an arbitrary server's models are unknowable")
	require.Equal(t, DefaultPreviewAITokens, c.Preview.AI.MaxOutputTokens)

	// Real values survive Normalize untouched.
	c = Config{Preview: PreviewConfig{
		WindowWidth:  1920,
		WindowHeight: 1080,
		AI:           PreviewAIConfig{BaseURL: "http://localhost:11434/v1", Model: "llama3"},
	}}
	c.Normalize()
	require.Equal(t, 1920, c.Preview.WindowWidth)
	require.Equal(t, 1080, c.Preview.WindowHeight)
	require.Equal(t, "http://localhost:11434/v1", c.Preview.AI.BaseURL)
	require.Equal(t, "llama3", c.Preview.AI.Model)

	// The block round-trips through Save/Load.
	in := Default()
	in.Preview.Enabled = Bool(true)
	in.Preview.WindowWidth = 1440
	in.Preview.Kagi.APIKey = "kagi-secret"
	in.Preview.AI.APIKey = "sk-secret"
	in.Preview.AI.BaseURL = "https://api.example/v1"
	in.Preview.AI.Model = "some-model"
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.Equal(t, Bool(true), got.Preview.Enabled)
	require.Equal(t, 1440, got.Preview.WindowWidth)
	require.Equal(t, "kagi-secret", got.Preview.Kagi.APIKey)
	require.Equal(t, "sk-secret", got.Preview.AI.APIKey)
	require.Equal(t, "https://api.example/v1", got.Preview.AI.BaseURL)
	require.Equal(t, "some-model", got.Preview.AI.Model)
}
