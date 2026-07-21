package preview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeKagiSuccessSpendsOneCredit(t *testing.T) {
	var gotLimit int
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search", r.URL.Path)
		var body struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		gotLimit = body.Limit
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"search":[{"url":"https://r.example","title":"R","snippet":"s"}]}}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{Provider: "kagi", APIKey: "k", BaseURL: srv.URL})
	require.True(t, res.OK)
	require.Equal(t, "ok: search answered with 1 result (1 credit spent)", res.Message)
	require.Equal(t, 1, gotLimit, "the probe asks for exactly one result")
	require.Equal(t, "Bearer k", gotAuth)
}

func TestProbeKagiFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":[{"message":"Invalid API Key"}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{Provider: "kagi"})
	require.False(t, res.OK)
	require.Equal(t, errWebNoKey, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "kagi", APIKey: "k", BaseURL: "not a url"})
	require.False(t, res.OK)
	require.Equal(t, errWebBadBase, res.Message)
	require.NotContains(t, res.Message, "not a url")

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "kagi", APIKey: "bad", BaseURL: srv.URL})
	require.False(t, res.OK)
	require.Equal(t, "kagi: HTTP 401: Invalid API Key", res.Message,
		"the honest outcome carries the HTTP status and the terse provider message")
	require.NotContains(t, res.Message, "bad")
}

func TestProbeOpenAISuccess(t *testing.T) {
	var gotMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/responses", r.URL.Path)
		var body struct {
			MaxOutputTokens int `json:"max_output_tokens"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		gotMaxTokens = body.MaxOutputTokens
		_, _ = w.Write([]byte(`{"status":"completed","model":"gpt-5-mini-resolved",
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{
		Provider: "openai", APIKey: "sk", BaseURL: srv.URL, Model: "gpt-5-mini",
	})
	require.True(t, res.OK)
	require.Equal(t, "ok: model gpt-5-mini-resolved answered", res.Message)
	require.Equal(t, probeMaxOutputTokens, gotMaxTokens, "the probe spends a tiny token cap")
}

func TestProbeOpenAIFailures(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "openai"})
	require.Equal(t, errAINoKey, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "openai", APIKey: "k"})
	require.Equal(t, errAINoModel, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "openai", APIKey: "k", Model: "m", BaseURL: "ftp://x"})
	require.Equal(t, errAIBadBase, res.Message)
}

func TestProbeAnthropicSuccessAndAuthFailure(t *testing.T) {
	var gotVersion string
	var gotMaxTokens int
	authorized := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		gotVersion = r.Header.Get("anthropic-version")
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		gotMaxTokens = body.MaxTokens
		if !authorized {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"claude-haiku-4-5-resolved","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{
		Provider: "anthropic", APIKey: "sk-ant", BaseURL: srv.URL, Model: "claude-haiku-4-5",
	})
	require.True(t, res.OK)
	require.Equal(t, "ok: model claude-haiku-4-5-resolved answered", res.Message)
	require.Equal(t, anthropicVersion, gotVersion)
	require.Equal(t, probeMaxOutputTokens, gotMaxTokens)

	authorized = false
	res = ProbeProvider(context.Background(), ProbeParams{
		Provider: "anthropic", APIKey: "sk-ant", BaseURL: srv.URL, Model: "claude-haiku-4-5",
	})
	require.False(t, res.OK)
	require.Equal(t, "anthropic: HTTP 401: invalid x-api-key", res.Message)
	require.NotContains(t, res.Message, "sk-ant")
}

func TestProbeAnthropicFailures(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "anthropic"})
	require.Equal(t, errAnthropicNoKey, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "anthropic", APIKey: "k"})
	require.Equal(t, errAnthropicNoModel, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "anthropic", APIKey: "k", Model: "m", BaseURL: "://"})
	require.Equal(t, errAnthropicBadBase, res.Message)
}

func TestProbeCustomKeylessSuccess(t *testing.T) {
	sawAuth := "unset"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"completed","model":"llama3",
			"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{Provider: "custom", BaseURL: srv.URL, Model: "llama3"})
	require.True(t, res.OK)
	require.Equal(t, "ok: model llama3 answered", res.Message)
	require.Equal(t, "", sawAuth, "a keyless probe sends no Authorization header")
}

func TestProbeCustomFailuresNameCustomKeys(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "custom", Model: "m"})
	require.Equal(t, errCustomNoBase, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "custom", BaseURL: "http://h.example"})
	require.Equal(t, errCustomNoModel, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "custom", BaseURL: "h.example", Model: "m"})
	require.Equal(t, errCustomBadBase, res.Message)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	res = ProbeProvider(context.Background(), ProbeParams{Provider: "custom", BaseURL: srv.URL, Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "custom: HTTP 503", res.Message, "custom errors name the custom provider")
}

func TestProbeRejectsUnknownProviderAndOversizeInputs(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "watson"})
	require.False(t, res.OK)
	require.Equal(t, `test: unknown provider "watson"`, res.Message)

	long := strings.Repeat("x", probeMaxKeyBytes+1)
	res = ProbeProvider(context.Background(), ProbeParams{Provider: "kagi", APIKey: long})
	require.Equal(t, "test: API key too long", res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "kagi", APIKey: "k", BaseURL: strings.Repeat("y", probeMaxBaseBytes+1)})
	require.Equal(t, "test: base URL too long", res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "openai", APIKey: "k", Model: strings.Repeat("z", probeMaxModelBytes+1)})
	require.Equal(t, "test: model name too long", res.Message)
}

func TestProbeProviderNameIsNormalized(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "  Kagi "})
	require.Equal(t, errWebNoKey, res.Message, "provider names trim and case-fold")
}
