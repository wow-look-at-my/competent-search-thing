package preview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newOpenAITestClient wires a client at srv.
func newOpenAITestClient(srv *httptest.Server) *OpenAIClient {
	c := NewOpenAIClient("sk-secret-key", "gpt-5-mini", 1024)
	c.BaseURL = srv.URL
	c.HTTPClient = srv.Client()
	return c
}

func TestOpenAIAskRequestAndConcat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/responses", r.URL.Path)
		require.Equal(t, "Bearer sk-secret-key", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		require.Equal(t, "gpt-5-mini", req["model"])
		require.Equal(t, "what is a searchbar?", req["input"])
		require.Equal(t, float64(1024), req["max_output_tokens"])

		// Reasoning items and non-output_text parts are skipped; the
		// message items' output_text parts concatenate in order.
		_, _ = w.Write([]byte(`{
			"status": "completed",
			"model": "gpt-5-mini-2026-01-01",
			"output": [
				{"type": "reasoning", "content": []},
				{"type": "message", "content": [
					{"type": "output_text", "text": "A searchbar"},
					{"type": "refusal", "text": "never seen"}
				]},
				{"type": "message", "content": [{"type": "output_text", "text": " finds things."}]}
			]
		}`))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	answer, model, err := c.Ask(context.Background(), "what is a searchbar?")
	require.NoError(t, err)
	require.Equal(t, "A searchbar finds things.", answer)
	require.Equal(t, "gpt-5-mini-2026-01-01", model, "the server-resolved model name wins")
}

func TestOpenAIIncompleteAppendsMarker(t *testing.T) {
	body := `{
		"status": "incomplete",
		"incomplete_details": {"reason": "max_output_tokens"},
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "Partial answer"}]}]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	answer, model, err := c.Ask(context.Background(), "q")
	require.NoError(t, err)
	require.Equal(t, "Partial answer\n[truncated by max_output_tokens]", answer)
	require.Equal(t, "gpt-5-mini", model, "no server model falls back to the configured one")

	// A content-filter truncation names its reason instead.
	body = `{
		"status": "incomplete",
		"incomplete_details": {"reason": "content_filter"},
		"output": [{"type": "message", "content": [{"type": "output_text", "text": "Cut"}]}]
	}`
	answer, _, err = c.Ask(context.Background(), "q2")
	require.NoError(t, err)
	require.Equal(t, "Cut\n[truncated: content_filter]", answer)

	// All tokens spent on reasoning: the marker IS the answer (a real
	// gpt-5 + small max_output_tokens outcome), not an error.
	body = `{"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}, "output": []}`
	answer, _, err = c.Ask(context.Background(), "q3")
	require.NoError(t, err)
	require.Equal(t, "[truncated by max_output_tokens]", answer)
}

func TestOpenAIOutputTextFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The raw REST answer is documented not to carry output_text;
		// tolerate a server/proxy that inlines it anyway.
		_, _ = w.Write([]byte(`{"status": "completed", "output": [], "output_text": "inline convenience"}`))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	answer, _, err := c.Ask(context.Background(), "q")
	require.NoError(t, err)
	require.Equal(t, "inline convenience", answer)
}

func TestOpenAIHTTPErrorsAreTerse(t *testing.T) {
	status := 429
	body := `{"error": {"message": "Rate limit reached for gpt-5-mini", "type": "requests"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	_, _, err := c.Ask(context.Background(), "q")
	require.EqualError(t, err, "openai: HTTP 429: Rate limit reached for gpt-5-mini")
	require.NotContains(t, err.Error(), "sk-secret-key", "the key never leaks into errors")

	// A huge message is capped; a non-JSON body yields the bare line.
	status, body = 400, `{"error": {"message": "`+strings.Repeat("y", 4000)+`"}}`
	_, _, err = c.Ask(context.Background(), "q")
	require.Error(t, err)
	require.Less(t, len(err.Error()), 300)

	status, body = 502, "<html>bad gateway</html>"
	_, _, err = c.Ask(context.Background(), "q")
	require.EqualError(t, err, "openai: HTTP 502")
}

func TestOpenAI200WithErrorObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status": "failed", "error": {"message": "model exploded"}, "output": []}`))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	_, _, err := c.Ask(context.Background(), "q")
	require.EqualError(t, err, "openai: model exploded")
}

func TestOpenAIMalformedAndEmpty(t *testing.T) {
	body := "not json"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newOpenAITestClient(srv)

	_, _, err := c.Ask(context.Background(), "q")
	require.ErrorContains(t, err, "openai: malformed response")

	body = `{"status": "completed", "output": []}`
	_, _, err = c.Ask(context.Background(), "q")
	require.ErrorContains(t, err, "openai: empty answer")
	require.ErrorContains(t, err, "completed")
}

func TestOpenAIModelAccessor(t *testing.T) {
	c := NewOpenAIClient("k", "some-model", 5)
	require.Equal(t, "some-model", c.Model())
}
