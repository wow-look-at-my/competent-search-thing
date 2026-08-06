package preview

import (
	"context"
	"fmt"
	"strings"
)

// Provider connectivity probes: the engine behind the config editor's
// per-provider "Test" buttons. One minimal REAL request against the
// candidate (possibly unsaved) values, answering honest ok/error --
// the terse client errors carry the HTTP status and a capped provider
// message, never the key, never the raw body. NOTE the Kagi probe
// spends one real search credit (a limit-1 search is the cheapest
// honest test the API offers); the AI probe caps the answer at
// probeMaxOutputTokens.

// Probe bounds: defense in depth against a hostile frontend echo (the
// ValidatePickReport stance -- wire-abuse limits, not redaction).
const (
	probeMaxKeyBytes   = 4096
	probeMaxBaseBytes  = 2048
	probeMaxModelBytes = 256
	// probeMaxOutputTokens caps one probe answer -- deliberately tiny;
	// 16 is the smallest cap the strictest servers accept.
	probeMaxOutputTokens = 16
	// probePrompt is the one-shot probe input. The answer content is
	// irrelevant -- reachability and authentication are the test.
	probePrompt = "Reply with the single word: ok"
	// probeQuery is the Kagi probe's search query.
	probeQuery = "connectivity test"
)

// The two testable providers -- the whole set: the web search and the
// one user-configured AI endpoint.
const (
	ProviderKagi = "kagi"
	ProviderAI   = "ai"
)

// ProbeParams carries one probe's candidate values. Provider selects
// the endpoint (ProviderKagi or ProviderAI); the rest are that
// provider's candidate settings, already env-resolved by the caller
// (the app layer applies the same config-else-environment resolution
// the live dispatcher uses).
type ProbeParams struct {
	Provider string
	APIKey   string
	BaseURL  string
	Model    string
}

// ProbeResult is one probe's honest outcome. Message carries the
// success summary or the terse provider error (incl. "HTTP <code>"
// when the endpoint answered non-2xx) -- never key material.
type ProbeResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func probeFail(msg string) ProbeResult { return ProbeResult{Message: msg} }

// ProbeProvider runs one minimal real request against the candidate
// provider configuration. The caller bounds the call with ctx (the
// clients honor it); a cancelled or expired context surfaces as the
// client's terse error.
func ProbeProvider(ctx context.Context, p ProbeParams) ProbeResult {
	if len(p.APIKey) > probeMaxKeyBytes {
		return probeFail("test: API key too long")
	}
	if len(p.BaseURL) > probeMaxBaseBytes {
		return probeFail("test: base URL too long")
	}
	if len(p.Model) > probeMaxModelBytes {
		return probeFail("test: model name too long")
	}
	switch strings.ToLower(strings.TrimSpace(p.Provider)) {
	case ProviderKagi:
		return probeKagi(ctx, p)
	case ProviderAI:
		return probeAI(ctx, p)
	default:
		return probeFail(fmt.Sprintf("test: unknown provider %q", p.Provider))
	}
}

// probeKagi runs one limit-1 web search (SPENDS one API credit).
func probeKagi(ctx context.Context, p ProbeParams) ProbeResult {
	if p.APIKey == "" {
		return probeFail(errWebNoKey)
	}
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return probeFail(errWebBadBase)
	}
	client := NewKagiClient(p.APIKey, 1)
	client.BaseURL = base
	results, _, err := client.Search(ctx, probeQuery)
	if err != nil {
		return probeFail(err.Error())
	}
	noun := "results"
	if len(results) == 1 {
		noun = "result"
	}
	return ProbeResult{OK: true, Message: fmt.Sprintf("ok: search answered with %d %s (1 credit spent)", len(results), noun)}
}

// probeAI probes the configured chat-completions endpoint. The key is
// optional (keyless local servers are the common case); the base URL
// and model are not, exactly as the live wiring requires them.
func probeAI(ctx context.Context, p ProbeParams) ProbeResult {
	if p.BaseURL == "" {
		return probeFail(errAINoBase)
	}
	if p.Model == "" {
		return probeFail(errAINoModel)
	}
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return probeFail(errAIBadBase)
	}
	client := NewAIClient(p.APIKey, p.Model, probeMaxOutputTokens)
	client.BaseURL = base
	return probeAsk(ctx, client.Ask)
}

// probeAsk runs one tiny ask and shapes the outcome. Any answer --
// even a truncation-marker-only one -- proves reachability and
// authentication, which is what the button tests.
func probeAsk(ctx context.Context, ask askFunc) ProbeResult {
	_, model, err := ask(ctx, probePrompt)
	if err != nil {
		return probeFail(err.Error())
	}
	return ProbeResult{OK: true, Message: fmt.Sprintf("ok: model %s answered", model)}
}
