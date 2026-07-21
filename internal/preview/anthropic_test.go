package preview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// anthropicOKBody builds one minimal successful Messages API answer.
func anthropicOKBody(model, stopReason string, texts ...string) string {
	type block struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	blocks := make([]block, 0, len(texts))
	for _, t := range texts {
		blocks = append(blocks, block{Type: "text", Text: t})
	}
	b, _ := json.Marshal(map[string]any{
		"model":       model,
		"stop_reason": stopReason,
		"content":     blocks,
	})
	return string(b)
}

func TestAnthropicAskSuccess(t *testing.T) {
	var gotPath, gotMethod, gotKey, gotVersion, gotCT string
	var gotBody struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotCT = r.Header.Get("Content-Type")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = w.Write([]byte(`{"model":"claude-haiku-4-5-resolved","stop_reason":"end_turn",
			"content":[{"type":"text","text":"Hello"},{"type":"thinking","text":"skip me"},{"type":"text","text":" world"}]}`))
	}))
	defer srv.Close()

	c := NewAnthropicClient("sk-ant-test", "claude-haiku-4-5", 64)
	c.BaseURL = srv.URL
	require.Equal(t, "claude-haiku-4-5", c.Model())

	answer, model, err := c.Ask(context.Background(), "say hi")
	require.NoError(t, err)
	require.Equal(t, "Hello world", answer, "only text blocks concatenate")
	require.Equal(t, "claude-haiku-4-5-resolved", model, "the server-resolved model is reported")

	require.Equal(t, "/v1/messages", gotPath)
	require.Equal(t, http.MethodPost, gotMethod)
	require.Equal(t, "sk-ant-test", gotKey, "auth rides the x-api-key header")
	require.Equal(t, anthropicVersion, gotVersion)
	require.Equal(t, "application/json", gotCT)
	require.Equal(t, "claude-haiku-4-5", gotBody.Model)
	require.Equal(t, 64, gotBody.MaxTokens)
	require.Len(t, gotBody.Messages, 1)
	require.Equal(t, "user", gotBody.Messages[0].Role)
	require.Equal(t, "say hi", gotBody.Messages[0].Content)
}

func TestAnthropicAskTruncationMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(anthropicOKBody("m", "max_tokens", "partial answer")))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	answer, _, err := c.Ask(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "partial answer\n[truncated by maxOutputTokens]", answer)
}

func TestAnthropicAskMarkerOnlyAnswerIsLegal(t *testing.T) {
	// A cap so tight nothing was emitted before it: the marker alone
	// is the answer (the openai.go reasoning-model stance).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(anthropicOKBody("m", "max_tokens")))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 1)
	c.BaseURL = srv.URL
	answer, _, err := c.Ask(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "[truncated by maxOutputTokens]", answer)
}

func TestAnthropicAskEmptyAnswerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(anthropicOKBody("m", "end_turn")))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.ErrorContains(t, err, `anthropic: empty answer (stop_reason "end_turn")`)
}

func TestAnthropicAskErrorEnvelopeIn200(t *testing.T) {
	// Defensive: an error object inside a 2xx body still fails tersely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.ErrorContains(t, err, "anthropic: Overloaded")
}

func TestAnthropicHTTPErrorParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()
	c := NewAnthropicClient("sk-ant-secret", "m", 8)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.ErrorContains(t, err, "anthropic: HTTP 401: invalid x-api-key")
	require.NotContains(t, err.Error(), "sk-ant-secret", "the key never appears in errors")
}

func TestAnthropicHTTPErrorWithoutEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded in plain text"))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.EqualError(t, err, "anthropic: HTTP 500")
	require.NotContains(t, err.Error(), "exploded", "the raw body is never quoted")
}

func TestAnthropicMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	_, _, err := c.Ask(context.Background(), "p")
	require.ErrorContains(t, err, "anthropic: malformed response")
}

func TestAnthropicConfiguredModelFallback(t *testing.T) {
	// A server that omits the model field: the configured name stands.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer srv.Close()
	c := NewAnthropicClient("k", "claude-haiku-4-5", 8)
	c.BaseURL = srv.URL
	_, model, err := c.Ask(context.Background(), "p")
	require.NoError(t, err)
	require.Equal(t, "claude-haiku-4-5", model)
}

func TestAnthropicContextCancellation(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)
	c := NewAnthropicClient("k", "m", 8)
	c.BaseURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.Ask(ctx, "p")
	require.Error(t, err)
	require.ErrorContains(t, err, "anthropic: ")
}
