package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Anthropic Messages API client constants -- POST /v1/messages with
// the x-api-key and anthropic-version headers,
// {"model","max_tokens","messages":[{"role":"user","content":...}]}
// in, a content[] of "text" blocks out; stop_reason "max_tokens"
// marks a truncated answer and errors arrive as
// {"type":"error","error":{"type","message"}}. Coded against the
// Anthropic API reference (verified 2026-07-21).
const (
	anthropicDefaultBaseURL = "https://api.anthropic.com"
	anthropicMessagesPath   = "/v1/messages"
	// anthropicVersion is the required anthropic-version header value
	// (the API's stable version date, not a release number).
	anthropicVersion = "2023-06-01"
	// anthropicMaxBody caps how much of a response body is ever read.
	anthropicMaxBody = 4 << 20
)

// AnthropicClient asks the Anthropic Messages API for one-shot
// answers. Tests point BaseURL at an httptest server. The API key is
// confined to the x-api-key header -- it never appears in errors,
// logs, or payloads.
type AnthropicClient struct {
	// BaseURL is the API origin (default https://api.anthropic.com).
	BaseURL string
	// HTTPClient performs the requests (default http.DefaultClient;
	// callers bound requests with their context).
	HTTPClient *http.Client

	key             string
	model           string
	maxOutputTokens int
}

// NewAnthropicClient builds a client answering with model, capped at
// maxOutputTokens per answer.
func NewAnthropicClient(key, model string, maxOutputTokens int) *AnthropicClient {
	return &AnthropicClient{key: key, model: model, maxOutputTokens: maxOutputTokens}
}

// Model returns the configured model name.
func (c *AnthropicClient) Model() string { return c.model }

// anthropicResponse is the subset of the Messages API answer this
// client consumes.
type anthropicResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Ask sends prompt and returns the concatenated text answer plus the
// model that produced it (the server-resolved name when reported). An
// answer stopped by the token cap gets a trailing truncation marker
// line (marker-only answers are legal -- the openai.go stance).
func (c *AnthropicClient) Ask(ctx context.Context, prompt string) (string, string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	reqBody, err := json.Marshal(struct {
		Model     string    `json:"model"`
		MaxTokens int       `json:"max_tokens"`
		Messages  []message `json:"messages"`
	}{Model: c.model, MaxTokens: c.maxOutputTokens, Messages: []message{{Role: "user", Content: prompt}}})
	if err != nil {
		return "", "", fmt.Errorf("anthropic: %w", err)
	}
	base := c.BaseURL
	if base == "" {
		base = anthropicDefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+anthropicMessagesPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("anthropic: %w", err)
	}
	req.Header.Set("x-api-key", c.key)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, anthropicMaxBody))
	if err != nil {
		return "", "", fmt.Errorf("anthropic: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", anthropicHTTPError(resp.StatusCode, body)
	}
	var parsed anthropicResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("anthropic: malformed response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", "", fmt.Errorf("anthropic: %s", capString(parsed.Error.Message, providerErrMsgCap))
	}
	var b strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	answer := b.String()
	if parsed.StopReason == "max_tokens" {
		if answer != "" {
			answer += "\n"
		}
		answer += "[truncated by maxOutputTokens]"
	}
	if answer == "" {
		return "", "", fmt.Errorf("anthropic: empty answer (stop_reason %q)", parsed.StopReason)
	}
	model := parsed.Model
	if model == "" {
		model = c.model
	}
	return answer, model, nil
}

// anthropicHTTPError builds the terse non-2xx error: "anthropic: HTTP
// <code>" plus at most the short parsed error message from the API's
// {"type":"error","error":{"type","message"}} envelope -- never the
// raw body, never the key.
func anthropicHTTPError(code int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("anthropic: HTTP %d: %s", code, capString(envelope.Error.Message, providerErrMsgCap))
	}
	return fmt.Errorf("anthropic: HTTP %d", code)
}
