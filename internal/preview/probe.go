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
// honest test the API offers); the AI probes cap the answer at
// probeMaxOutputTokens.

// Probe bounds: defense in depth against a hostile frontend echo (the
// ValidatePickReport stance -- wire-abuse limits, not redaction).
const (
	probeMaxKeyBytes   = 4096
	probeMaxBaseBytes  = 2048
	probeMaxModelBytes = 256
	// probeMaxOutputTokens caps one probe answer -- deliberately tiny
	// (the OpenAI Responses API rejects values below 16; Anthropic
	// accepts any positive cap, and 16 is equally cheap there).
	probeMaxOutputTokens = 16
	// probePrompt is the one-shot probe input. The answer content is
	// irrelevant -- reachability and authentication are the test.
	probePrompt = "Reply with the single word: ok"
	// probeQuery is the Kagi probe's search query.
	probeQuery = "connectivity test"
)

// ProbeParams carries one probe's candidate values. Provider selects
// the endpoint ("kagi", "openai", "anthropic", or "custom"); the rest
// are that provider's candidate settings, already env-resolved by the
// caller (the app layer applies the same config-else-environment
// resolution the live dispatcher uses).
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
	case "kagi":
		return probeKagi(ctx, p)
	case aiProviderOpenAI:
		return probeOpenAICompat(ctx, p, "openai", errAINoKey, errAINoModel, errAIBadBase, true)
	case aiProviderAnthropic:
		return probeAnthropic(ctx, p)
	case aiProviderCustom:
		return probeCustom(ctx, p)
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

// probeOpenAICompat probes an OpenAI-Responses-API endpoint --
// the OpenAI provider itself and (via probeCustom) any compatible
// server. keyRequired=false skips the key check (custom endpoints may
// be keyless).
func probeOpenAICompat(ctx context.Context, p ProbeParams, label, noKey, noModel, badBase string, keyRequired bool) ProbeResult {
	if keyRequired && p.APIKey == "" {
		return probeFail(noKey)
	}
	if p.Model == "" {
		return probeFail(noModel)
	}
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return probeFail(badBase)
	}
	client := NewOpenAIClient(p.APIKey, p.Model, probeMaxOutputTokens)
	client.BaseURL = base
	client.Name = label
	return probeAsk(ctx, client.Ask)
}

func probeCustom(ctx context.Context, p ProbeParams) ProbeResult {
	if p.BaseURL == "" {
		return probeFail(errCustomNoBase)
	}
	return probeOpenAICompat(ctx, p, "custom", "", errCustomNoModel, errCustomBadBase, false)
}

func probeAnthropic(ctx context.Context, p ProbeParams) ProbeResult {
	if p.APIKey == "" {
		return probeFail(errAnthropicNoKey)
	}
	if p.Model == "" {
		return probeFail(errAnthropicNoModel)
	}
	base, err := normalizeBaseURL(p.BaseURL)
	if err != nil {
		return probeFail(errAnthropicBadBase)
	}
	client := NewAnthropicClient(p.APIKey, p.Model, probeMaxOutputTokens)
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
