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

// OpenAI Responses API client constants -- POST /v1/responses with a
// Bearer key, {"model","input","max_output_tokens"} in, an output[]
// of "message" items whose "output_text" content parts carry the
// text; verified against the OpenAI API reference (openai-openapi)
// 2026-07-18.
const (
	openAIDefaultBaseURL = "https://api.openai.com"
	openAIResponsesPath  = "/v1/responses"
	// openAIMaxBody caps how much of a response body is ever read.
	openAIMaxBody = 4 << 20
)

// OpenAIClient asks the OpenAI Responses API for one-shot answers.
// Tests point BaseURL at an httptest server. The API key is confined
// to the Authorization header -- it never appears in errors, logs, or
// payloads.
type OpenAIClient struct {
	// BaseURL is the API origin (default https://api.openai.com).
	BaseURL string
	// HTTPClient performs the requests (default http.DefaultClient;
	// callers bound requests with their context).
	HTTPClient *http.Client
	// Name labels this client's error messages (default "openai").
	// The custom provider sets "custom" so a misconfigured local
	// endpoint's errors name the section that configures it instead
	// of blaming OpenAI.
	Name string

	key             string
	model           string
	maxOutputTokens int
}

// NewOpenAIClient builds a client answering with model, capped at
// maxOutputTokens per answer.
func NewOpenAIClient(key, model string, maxOutputTokens int) *OpenAIClient {
	return &OpenAIClient{key: key, model: model, maxOutputTokens: maxOutputTokens}
}

// Model returns the configured model name.
func (c *OpenAIClient) Model() string { return c.model }

// label is the error-message prefix (Name, default "openai").
func (c *OpenAIClient) label() string {
	if c.Name != "" {
		return c.Name
	}
	return "openai"
}

// openAIResponse is the subset of the Responses API answer this
// client consumes.
type openAIResponse struct {
	Status string `json:"status"`
	Model  string `json:"model"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	// OutputText is the SDKs' aggregated-text convenience property.
	// The raw REST answer is documented NOT to carry it; it is read
	// only as a fallback in case a proxy or future server inlines it.
	OutputText string `json:"output_text"`
}

// Ask sends prompt and returns the concatenated text answer plus the
// model that produced it (the server-resolved name when reported). An
// "incomplete" answer gets a trailing truncation marker line.
func (c *OpenAIClient) Ask(ctx context.Context, prompt string) (string, string, error) {
	reqBody, err := json.Marshal(struct {
		Model           string `json:"model"`
		Input           string `json:"input"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}{Model: c.model, Input: prompt, MaxOutputTokens: c.maxOutputTokens})
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", c.label(), err)
	}
	base := c.BaseURL
	if base == "" {
		base = openAIDefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+openAIResponsesPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", c.label(), err)
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", c.label(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, openAIMaxBody))
	if err != nil {
		return "", "", fmt.Errorf("%s: reading response: %w", c.label(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", openAIHTTPError(c.label(), resp.StatusCode, body)
	}
	var parsed openAIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("%s: malformed response: %w", c.label(), err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", "", fmt.Errorf("%s: %s", c.label(), capString(parsed.Error.Message, providerErrMsgCap))
	}
	var b strings.Builder
	for _, item := range parsed.Output {
		if item.Type != "message" {
			continue // reasoning summaries, tool calls, ...
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				b.WriteString(part.Text)
			}
		}
	}
	answer := b.String()
	if answer == "" {
		answer = parsed.OutputText
	}
	if parsed.Status == "incomplete" {
		marker := "[truncated by max_output_tokens]"
		if parsed.IncompleteDetails != nil && parsed.IncompleteDetails.Reason == "content_filter" {
			marker = "[truncated: content_filter]"
		}
		if answer != "" {
			answer += "\n"
		}
		answer += marker
	}
	if answer == "" {
		return "", "", fmt.Errorf("%s: empty answer (status %q)", c.label(), parsed.Status)
	}
	model := parsed.Model
	if model == "" {
		model = c.model
	}
	return answer, model, nil
}

// openAIHTTPError builds the terse non-2xx error: "<label>: HTTP
// <code>" plus at most the short parsed {"error":{"message":...}} --
// never the raw body, never the key.
func openAIHTTPError(label string, code int, body []byte) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("%s: HTTP %d: %s", label, code, capString(envelope.Error.Message, providerErrMsgCap))
	}
	return fmt.Errorf("%s: HTTP %d", label, code)
}
