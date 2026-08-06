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

// The one AI client: an OpenAI-compatible chat-completions endpoint
// the user names in full. These pin the wire shape, the honest
// truncation marker, and that the key never leaves the header.

func TestAIClientRequestShape(t *testing.T) {
	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	var gotPath, gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"model":"m-resolved","choices":[{"finish_reason":"stop","message":{"content":"the answer"}}]}`))
	}))
	defer srv.Close()

	c := NewAIClient("sk-secret", "m", 64)
	c.BaseURL = srv.URL + "/v1"
	require.Equal(t, "m", c.Model())
	answer, model, err := c.Ask(context.Background(), "the prompt")
	require.NoError(t, err)
	require.Equal(t, "the answer", answer)
	require.Equal(t, "m-resolved", model, "the server-resolved model wins when reported")

	require.Equal(t, "/v1/chat/completions", gotPath, "the base URL carries its own version segment")
	require.Equal(t, "Bearer sk-secret", gotAuth)
	require.Equal(t, "application/json", gotType)
	require.Equal(t, "m", got.Model)
	require.Equal(t, 64, got.MaxTokens)
	require.Len(t, got.Messages, 1)
	require.Equal(t, "user", got.Messages[0].Role)
	require.Equal(t, "the prompt", got.Messages[0].Content)
}

func TestAIClientKeylessSendsNoAuthorization(t *testing.T) {
	// Local servers expect NO header at all; an empty bearer is worse
	// than none (some servers reject it outright).
	sawAuth := "unset"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := NewAIClient("", "llama3", 16)
	c.BaseURL = srv.URL
	answer, model, err := c.Ask(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "ok", answer)
	require.Equal(t, "llama3", model, "an unreported model falls back to the configured one")
	require.Equal(t, "", sawAuth)
}

func TestAIClientMarksTruncatedAnswers(t *testing.T) {
	// An answer cut off by the token cap must never read as complete.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"half an ans"}}]}`))
	}))
	defer srv.Close()

	c := NewAIClient("k", "m", 4)
	c.BaseURL = srv.URL
	answer, _, err := c.Ask(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "half an ans\n[truncated by maxOutputTokens]", answer)
}

func TestAIClientErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"object error message", http.StatusUnauthorized, `{"error":{"message":"Incorrect API key"}}`, "ai: HTTP 401: Incorrect API key"},
		{"bare string error", http.StatusBadRequest, `{"error":"model not found"}`, "ai: HTTP 400: model not found"},
		{"unparseable body", http.StatusServiceUnavailable, `<html>down</html>`, "ai: HTTP 503"},
		{"200 with an error envelope", http.StatusOK, `{"error":{"message":"quota exhausted"}}`, "ai: quota exhausted"},
		{"200 with no choices", http.StatusOK, `{"choices":[]}`, "ai: empty answer"},
		{"malformed json", http.StatusOK, `{not json`, "ai: malformed response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewAIClient("sk-secret", "m", 16)
			c.BaseURL = srv.URL
			_, _, err := c.Ask(context.Background(), "p")
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
			require.NotContains(t, err.Error(), "sk-secret", "the key never reaches an error")
			require.NotContains(t, err.Error(), "<html>", "the raw body never reaches an error")
		})
	}
}

func TestAIClientCapsTheProviderMessage(t *testing.T) {
	long := strings.Repeat("e", providerErrMsgCap*2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": long}})
	}))
	defer srv.Close()

	c := NewAIClient("k", "m", 16)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.Error(t, err)
	require.Less(t, len(err.Error()), len(long), "a hostile provider message cannot flood the pane")
}

func TestAIClientWithoutBaseURLNamesTheKnob(t *testing.T) {
	// Nothing is dialed and no default endpoint is invented.
	c := NewAIClient("k", "m", 16)
	_, _, err := c.Ask(context.Background(), "p")
	require.EqualError(t, err, errAINoBase)
}

func TestAIClientHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewAIClient("k", "m", 16)
	c.BaseURL = "http://127.0.0.1:1"
	_, _, err := c.Ask(ctx, "p")
	require.Error(t, err)
}
