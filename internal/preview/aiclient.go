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

// The ONE AI answer client: an OpenAI-compatible chat-completions
// endpoint the user names in full (preview.ai.baseUrl). There is no
// built-in provider and no default endpoint -- with nothing
// configured the answer preview is simply unavailable, and no query
// is ever sent anywhere the user did not point it.
//
// Chat completions (POST {base}/chat/completions) is deliberately the
// wire shape: it is what OpenAI, Anthropic's compatibility endpoint,
// OpenRouter, Ollama, llama.cpp, LM Studio, vLLM and everything else
// in that ecosystem implement. baseUrl is the API base INCLUDING the
// version segment, the convention every SDK's base_url uses
// (https://api.openai.com/v1, http://localhost:11434/v1,
// https://api.anthropic.com/v1, https://openrouter.ai/api/v1).
const (
	chatCompletionsPath = "/chat/completions"
	// aiMaxBody caps how much of a response body is ever read.
	aiMaxBody = 4 << 20
)

// AIClient asks a chat-completions endpoint for one-shot answers.
// Tests point BaseURL at an httptest server. The API key is confined
// to the Authorization header -- it never appears in errors, logs or
// payloads -- and an EMPTY key sends no Authorization header at all,
// which is what local servers expect.
type AIClient struct {
	// BaseURL is the API base, version segment included. Required:
	// there is no default endpoint.
	BaseURL string
	// HTTPClient performs the requests (default http.DefaultClient;
	// callers bound requests with their context).
	HTTPClient *http.Client

	key             string
	model           string
	maxOutputTokens int
}

// NewAIClient builds a client answering with model, capped at
// maxOutputTokens per answer.
func NewAIClient(key, model string, maxOutputTokens int) *AIClient {
	return &AIClient{key: key, model: model, maxOutputTokens: maxOutputTokens}
}

// Model returns the configured model name.
func (c *AIClient) Model() string { return c.model }

// chatResponse is the subset of a chat-completions answer this client
// consumes. Servers vary in what else they send; unknown fields are
// ignored (the tolerance every client in this repo applies).
type chatResponse struct {
	Model string `json:"model"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Ask sends prompt and returns the answer text plus the model that
// produced it (the server-resolved name when reported). An answer cut
// short by the token cap gets a trailing truncation marker line.
func (c *AIClient) Ask(ctx context.Context, prompt string) (string, string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	reqBody, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
	}{Model: c.model, Messages: []message{{Role: "user", Content: prompt}}, MaxTokens: c.maxOutputTokens})
	if err != nil {
		return "", "", fmt.Errorf("ai: %w", err)
	}
	if c.BaseURL == "" {
		return "", "", fmt.Errorf("%s", errAINoBase)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+chatCompletionsPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", fmt.Errorf("ai: %w", err)
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
		return "", "", fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, aiMaxBody))
	if err != nil {
		return "", "", fmt.Errorf("ai: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", aiHTTPError(resp.StatusCode, body)
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("ai: malformed response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", "", fmt.Errorf("ai: %s", capString(parsed.Error.Message, providerErrMsgCap))
	}
	var b strings.Builder
	truncated := false
	for _, ch := range parsed.Choices {
		b.WriteString(ch.Message.Content)
		if ch.FinishReason == "length" {
			truncated = true
		}
	}
	answer := b.String()
	if truncated {
		// The same honest marker the token-capped answer always
		// carried: a cut-off answer must never read as a complete one.
		if answer != "" {
			answer += "\n"
		}
		answer += "[truncated by maxOutputTokens]"
	}
	if answer == "" {
		return "", "", fmt.Errorf("ai: empty answer")
	}
	model := parsed.Model
	if model == "" {
		model = c.model
	}
	return answer, model, nil
}

// aiHTTPError builds the terse non-2xx error: "ai: HTTP <code>" plus
// at most the short parsed {"error":{"message":...}} -- never the raw
// body, never the key. Servers that answer a bare string message
// (some local ones do) are read too.
func aiHTTPError(code int, body []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.Error, &obj); err == nil && obj.Message != "" {
			return fmt.Errorf("ai: HTTP %d: %s", code, capString(obj.Message, providerErrMsgCap))
		}
		var msg string
		if err := json.Unmarshal(envelope.Error, &msg); err == nil && msg != "" {
			return fmt.Errorf("ai: HTTP %d: %s", code, capString(msg, providerErrMsgCap))
		}
	}
	return fmt.Errorf("ai: HTTP %d", code)
}
