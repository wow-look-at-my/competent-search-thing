package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
)

// aiProbeServer answers both AI wire shapes and records the last auth
// material seen (Authorization for OpenAI-compatible, x-api-key for
// Anthropic).
func aiProbeServer(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Value) {
	t.Helper()
	var bearer, apiKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer.Store(r.Header.Get("Authorization"))
		apiKey.Store(r.Header.Get("x-api-key"))
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"status":"completed","model":"m-resolved",
				"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"model":"m-resolved","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &bearer, &apiKey
}

func TestPreviewProviderTestUsesCandidateValues(t *testing.T) {
	srv, bearer, _ := aiProbeServer(t)
	a, _ := newTestApp(t, nil, Options{})
	// An environment key exists, but the CANDIDATE key wins -- the
	// button tests what is in the editor, not what is saved.
	a.plat.getenv = func(key string) string {
		if key == envOpenAIAPIKey {
			return "env-key"
		}
		return ""
	}
	res := a.TestPreviewProvider(PreviewProviderTest{
		Provider: "openai", APIKey: "candidate-key", BaseURL: srv.URL, Model: "m",
	})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "ok: model m-resolved answered", res.Message)
	require.Equal(t, "Bearer candidate-key", bearer.Load())
}

func TestPreviewProviderTestEnvFallbacks(t *testing.T) {
	srv, bearer, apiKey := aiProbeServer(t)
	a, _ := newTestApp(t, nil, Options{})
	a.plat.getenv = func(key string) string {
		switch key {
		case envOpenAIAPIKey:
			return "openai-env"
		case envAnthropicAPIKey:
			return "anthropic-env"
		case envAnthropicBaseURL:
			return srv.URL
		}
		return ""
	}
	// An empty candidate key resolves through the provider's env
	// fallback, exactly like the live dispatcher.
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "openai", BaseURL: srv.URL, Model: "m"})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "Bearer openai-env", bearer.Load())

	// The Anthropic base URL falls back to ANTHROPIC_BASE_URL too.
	res = a.TestPreviewProvider(PreviewProviderTest{Provider: "anthropic", Model: "claude-haiku-4-5"})
	require.True(t, res.OK, res.Message)
	require.Equal(t, "anthropic-env", apiKey.Load())
}

func TestPreviewProviderTestCustomHasNoEnvFallback(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.plat.getenv = func(key string) string {
		if key == envOpenAIBaseURL {
			return "http://never-used.example"
		}
		return ""
	}
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "custom", Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "custom: no base URL (preview.custom.baseUrl)", res.Message,
		"custom is the user-typed endpoint -- no environment fills it in")
}

func TestPreviewProviderTestKagi(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":{"search":[{"url":"https://r.example","title":"R","snippet":"s"}]}}`))
	}))
	defer srv.Close()
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "kagi", APIKey: "k", BaseURL: srv.URL})
	require.True(t, res.OK)
	require.Equal(t, "ok: search answered with 1 result (1 credit spent)", res.Message)
}

func TestPreviewProviderTestHonestFailures(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	res := a.TestPreviewProvider(PreviewProviderTest{Provider: "watson"})
	require.False(t, res.OK)
	require.Equal(t, `test: unknown provider "watson"`, res.Message)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer srv.Close()
	res = a.TestPreviewProvider(PreviewProviderTest{Provider: "OpenAI ", APIKey: "bad", BaseURL: srv.URL, Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "openai: HTTP 401: Incorrect API key provided", res.Message,
		"the outcome carries the HTTP status and terse provider message; provider names case-fold")
	require.NotContains(t, res.Message, "bad")
}

// TestGetPreviewConfigAIProviderSelection pins the per-provider
// configured-ness rules the strip button consumes.
func TestGetPreviewConfigAIProviderSelection(t *testing.T) {
	// Anthropic selected: its key (config or environment) decides.
	a, _ := newTestApp(t, nil, Options{Preview: config.PreviewConfig{
		Enabled:    true,
		AIProvider: config.AIProviderAnthropic,
	}})
	info := a.GetPreviewConfig()
	require.Equal(t, "anthropic", info.AIProvider)
	require.False(t, info.AIConfigured)
	a.plat.getenv = func(key string) string {
		if key == envAnthropicAPIKey {
			return "sk-ant-env"
		}
		return ""
	}
	require.True(t, a.GetPreviewConfig().AIConfigured, "ANTHROPIC_API_KEY counts")

	// An OpenAI key alone does NOT light the button while anthropic
	// is selected -- configured-ness follows the selection.
	b, _ := newTestApp(t, nil, Options{Preview: config.PreviewConfig{
		Enabled:    true,
		AIProvider: config.AIProviderAnthropic,
		OpenAI:     config.PreviewOpenAIConfig{APIKey: "sk"},
	}})
	require.False(t, b.GetPreviewConfig().AIConfigured)

	// Custom selected: base URL plus model, key optional.
	c, _ := newTestApp(t, nil, Options{Preview: config.PreviewConfig{
		Enabled:    true,
		AIProvider: config.AIProviderCustom,
		Custom:     config.PreviewCustomConfig{BaseURL: "http://localhost:11434"},
	}})
	info = c.GetPreviewConfig()
	require.Equal(t, "custom", info.AIProvider)
	require.False(t, info.AIConfigured, "a model is required too")
	d, _ := newTestApp(t, nil, Options{Preview: config.PreviewConfig{
		Enabled:    true,
		AIProvider: config.AIProviderCustom,
		Custom:     config.PreviewCustomConfig{BaseURL: "http://localhost:11434", Model: "llama3"},
	}})
	require.True(t, d.GetPreviewConfig().AIConfigured)

	// The zero-value selector reads as openai.
	e, _ := newTestApp(t, nil, Options{})
	require.Equal(t, "openai", e.GetPreviewConfig().AIProvider)
}

// TestStartPreviewWiresAnthropicProvider drives the real dispatcher
// through the app wiring with the anthropic provider selected.
func TestStartPreviewWiresAnthropicProvider(t *testing.T) {
	srv, _, apiKey := aiProbeServer(t)
	opt := previewTestOptions()
	opt.Preview.AIProvider = config.AIProviderAnthropic
	opt.Preview.Anthropic.APIKey = "sk-ant-config"
	opt.Preview.Anthropic.BaseURL = srv.URL
	a, r := newTestApp(t, nil, opt)
	a.Startup(context.Background())
	d := a.previewDispatcher()
	require.NotNil(t, d)
	require.True(t, d.AIConfigured())

	a.FetchAIPreview("q", 1)
	p := lastPreviewPayload(t, r, 1)
	require.Equal(t, preview.KindAI, p.Kind)
	require.Equal(t, "ok", p.AI.Answer)
	require.Equal(t, "m-resolved", p.AI.Model)
	require.Equal(t, "sk-ant-config", apiKey.Load(), "the config key rides x-api-key")
}
