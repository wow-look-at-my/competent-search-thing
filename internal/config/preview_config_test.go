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
		Enabled:       false,
		WindowWidth:   1600,
		WindowHeight:  800,
		TextMaxKB:     256,
		ImageMaxEdge:  800,
		DirMaxEntries: 200,
		AIProvider:    "openai",
		Kagi:          PreviewKagiConfig{MaxResults: 8},
		OpenAI:        PreviewOpenAIConfig{Model: "gpt-5-mini", MaxOutputTokens: 1024},
		Anthropic:     PreviewAnthropicConfig{Model: "claude-haiku-4-5", MaxOutputTokens: 1024},
		Custom:        PreviewCustomConfig{MaxOutputTokens: 1024},
	}, Default().Preview, "the preview pane defaults off with every knob populated")

	// A config predating the preview block normalizes to the defaults
	// (still disabled).
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"roots":["/data"]}`), &c))
	c.Normalize()
	require.Equal(t, DefaultPreview(), c.Preview)

	// Zero and negative knobs and an empty model are repaired; real
	// values -- the API keys and base URLs verbatim included --
	// survive. The base URLs are passthrough like the Firefox
	// profileDirs: Normalize never touches them (validation happens at
	// the consumer), and an odd spelling -- trailing slash included --
	// survives byte-for-byte.
	c = Config{Preview: PreviewConfig{
		Enabled:       true,
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
		OpenAI: PreviewOpenAIConfig{
			APIKey:          "sk-secret",
			BaseURL:         "not even a url",
			Model:           "",
			MaxOutputTokens: -5,
		},
		AIProvider: "  Anthropic ",
		Anthropic: PreviewAnthropicConfig{
			APIKey:          "sk-ant-secret",
			Model:           "",
			MaxOutputTokens: 0,
		},
		Custom: PreviewCustomConfig{
			BaseURL:         "http://localhost:11434",
			Model:           "",
			MaxOutputTokens: -2,
		},
	}}
	c.Normalize()
	require.True(t, c.Preview.Enabled)
	require.Equal(t, DefaultPreviewWindowWidth, c.Preview.WindowWidth)
	require.Equal(t, DefaultPreviewWindowHeight, c.Preview.WindowHeight)
	require.Equal(t, 512, c.Preview.TextMaxKB)
	require.Equal(t, DefaultPreviewImageMaxEdge, c.Preview.ImageMaxEdge)
	require.Equal(t, 50, c.Preview.DirMaxEntries)
	require.Equal(t, "kagi-secret", c.Preview.Kagi.APIKey, "the key is never touched")
	require.Equal(t, "https://kagi.internal.example/", c.Preview.Kagi.BaseURL, "the base URL is never touched")
	require.Equal(t, DefaultPreviewKagiMax, c.Preview.Kagi.MaxResults)
	require.Equal(t, "sk-secret", c.Preview.OpenAI.APIKey, "the key is never touched")
	require.Equal(t, "not even a url", c.Preview.OpenAI.BaseURL, "the base URL is never touched")
	require.Equal(t, DefaultPreviewOpenAIModel, c.Preview.OpenAI.Model)
	require.Equal(t, DefaultPreviewOpenAITokens, c.Preview.OpenAI.MaxOutputTokens)
	require.Equal(t, AIProviderAnthropic, c.Preview.AIProvider,
		"the provider selector trims and lowercases")
	require.Equal(t, "sk-ant-secret", c.Preview.Anthropic.APIKey, "the key is never touched")
	require.Equal(t, DefaultPreviewAnthropicModel, c.Preview.Anthropic.Model)
	require.Equal(t, DefaultPreviewAnthropicTokens, c.Preview.Anthropic.MaxOutputTokens)
	require.Equal(t, "http://localhost:11434", c.Preview.Custom.BaseURL, "the base URL is never touched")
	require.Equal(t, "", c.Preview.Custom.Model,
		"custom.model has no invented default -- an unknown server's models are unknowable")
	require.Equal(t, DefaultPreviewCustomTokens, c.Preview.Custom.MaxOutputTokens)

	// An unknown provider selector repairs to the default.
	c = Config{Preview: PreviewConfig{AIProvider: "watson"}}
	c.Normalize()
	require.Equal(t, DefaultPreviewAIProvider, c.Preview.AIProvider)

	// Real values survive Normalize untouched.
	c = Config{Preview: PreviewConfig{
		WindowWidth:  1920,
		WindowHeight: 1080,
		OpenAI:       PreviewOpenAIConfig{Model: "gpt-5"},
	}}
	c.Normalize()
	require.Equal(t, 1920, c.Preview.WindowWidth)
	require.Equal(t, 1080, c.Preview.WindowHeight)
	require.Equal(t, "gpt-5", c.Preview.OpenAI.Model)

	// The block round-trips through Save/Load.
	in := Default()
	in.Preview.Enabled = true
	in.Preview.WindowWidth = 1440
	in.Preview.AIProvider = AIProviderAnthropic
	in.Preview.Kagi.APIKey = "kagi-secret"
	in.Preview.OpenAI.APIKey = "sk-secret"
	in.Preview.Anthropic.APIKey = "sk-ant-secret"
	require.NoError(t, Save(in))
	got, err := Load()
	require.NoError(t, err)
	require.True(t, got.Preview.Enabled)
	require.Equal(t, 1440, got.Preview.WindowWidth)
	require.Equal(t, AIProviderAnthropic, got.Preview.AIProvider)
	require.Equal(t, "kagi-secret", got.Preview.Kagi.APIKey)
	require.Equal(t, "sk-secret", got.Preview.OpenAI.APIKey)
	require.Equal(t, "sk-ant-secret", got.Preview.Anthropic.APIKey)
	require.Equal(t, DefaultPreviewOpenAIModel, got.Preview.OpenAI.Model)
	require.Equal(t, DefaultPreviewAnthropicModel, got.Preview.Anthropic.Model)
}
