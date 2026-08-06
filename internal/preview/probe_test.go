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

func TestProbeAISuccess(t *testing.T) {
	var gotMaxTokens int
	var gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		gotModel, gotMaxTokens = body.Model, body.MaxTokens
		_, _ = w.Write([]byte(`{"model":"llama3-resolved",
			"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{
		Provider: "ai", APIKey: "sk", BaseURL: srv.URL + "/v1", Model: "llama3",
	})
	require.True(t, res.OK)
	require.Equal(t, "ok: model llama3-resolved answered", res.Message)
	require.Equal(t, "llama3", gotModel)
	require.Equal(t, "Bearer sk", gotAuth)
	require.Equal(t, probeMaxOutputTokens, gotMaxTokens, "the probe spends a tiny token cap")
}

func TestProbeAIKeylessSendsNoAuthHeader(t *testing.T) {
	// The local-server shape: no key configured, so no Authorization
	// header at all (an empty bearer would be rejected outright).
	sawAuth := "unset"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"model":"llama3","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	res := ProbeProvider(context.Background(), ProbeParams{Provider: "ai", BaseURL: srv.URL, Model: "llama3"})
	require.True(t, res.OK)
	require.Equal(t, "ok: model llama3 answered", res.Message)
	require.Equal(t, "", sawAuth, "a keyless probe sends no Authorization header")
}

func TestProbeAIFailures(t *testing.T) {
	// The key is optional; the endpoint and model are not, and each
	// failure names its own knob.
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "ai", Model: "m"})
	require.Equal(t, errAINoBase, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "ai", BaseURL: "http://h.example"})
	require.Equal(t, errAINoModel, res.Message)

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "ai", BaseURL: "ftp://x", Model: "m"})
	require.Equal(t, errAIBadBase, res.Message)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()
	res = ProbeProvider(context.Background(), ProbeParams{Provider: "ai", APIKey: "sk-secret", BaseURL: srv.URL, Model: "m"})
	require.False(t, res.OK)
	require.Equal(t, "ai: HTTP 401: invalid api key", res.Message)
	require.NotContains(t, res.Message, "sk-secret")
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

	res = ProbeProvider(context.Background(), ProbeParams{Provider: "ai", APIKey: "k", Model: strings.Repeat("z", probeMaxModelBytes+1)})
	require.Equal(t, "test: model name too long", res.Message)
}

func TestProbeProviderNameIsNormalized(t *testing.T) {
	res := ProbeProvider(context.Background(), ProbeParams{Provider: "  Kagi "})
	require.Equal(t, errWebNoKey, res.Message, "provider names trim and case-fold")
}
