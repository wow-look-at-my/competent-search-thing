package preview

import "context"

// AI answer provider selection: Options.AIProvider picks which client
// answers FetchAI -- "openai" (the default, incl. ""), "anthropic",
// or "custom" (an OpenAI-compatible endpoint at Options.CustomBaseURL
// with no official fallback). Exactly one provider is wired per
// Dispatcher; unusable configurations leave aiFn nil with aiErr
// carrying the honest fetch-path message naming the config key (and
// environment fallback, where one exists) -- never a value.

// Fetch-path messages for an unavailable AI provider, per provider.
// They name config knobs and environment fallbacks but never quote
// values: keys are secret and a base URL may carry userinfo.
const (
	errAINoKey   = "openai: no API key (preview.openai.apiKey or OPENAI_API_KEY)"
	errAIBadBase = "openai: invalid baseUrl (preview.openai.baseUrl / OPENAI_BASE_URL)"

	errAnthropicNoKey   = "anthropic: no API key (preview.anthropic.apiKey or ANTHROPIC_API_KEY)"
	errAnthropicNoModel = "anthropic: no model (preview.anthropic.model)"
	errAnthropicBadBase = "anthropic: invalid baseUrl (preview.anthropic.baseUrl / ANTHROPIC_BASE_URL)"

	errCustomNoBase  = "custom: no base URL (preview.custom.baseUrl)"
	errCustomNoModel = "custom: no model (preview.custom.model)"
	errCustomBadBase = "custom: invalid baseUrl (preview.custom.baseUrl)"

	errAINoModel = "openai: no model (preview.openai.model)"
)

// AI provider selector values (config preview.aiProvider mirrors
// these; internal/config normalizes before they land here, and wireAI
// treats anything unrecognized -- incl. "" -- as OpenAI).
const (
	aiProviderOpenAI    = "openai"
	aiProviderAnthropic = "anthropic"
	aiProviderCustom    = "custom"
)

// askFunc is the one-shot answer seam every AI client satisfies:
// prompt in, (answer, resolvedModel) out.
type askFunc func(ctx context.Context, prompt string) (string, string, error)

// wireAI wires the configured AI answer provider into aiFn/aiErr --
// called once from New. The answer cache key is provider-qualified
// ("<provider>/<model>"), so switching providers can never serve one
// backend's cached answer as another's even when the model strings
// collide (a custom endpoint proxying "gpt-5-mini" is not OpenAI).
func (d *Dispatcher) wireAI(opt Options) {
	switch opt.AIProvider {
	case aiProviderAnthropic:
		d.wireAnthropic(opt)
	case aiProviderCustom:
		d.wireCustom(opt)
	default: // "openai" and the pre-selector "" both mean OpenAI
		d.wireOpenAI(opt)
	}
}

func (d *Dispatcher) wireOpenAI(opt Options) {
	if opt.OpenAIAPIKey == "" {
		d.aiErr = errAINoKey
		return
	}
	if opt.OpenAIModel == "" {
		d.aiErr = errAINoModel
		return
	}
	base, err := normalizeBaseURL(opt.OpenAIBaseURL)
	if err != nil {
		d.aiErr = errAIBadBase
		return
	}
	client := NewOpenAIClient(opt.OpenAIAPIKey, opt.OpenAIModel, opt.OpenAIMaxOutputTokens)
	client.BaseURL = base
	d.installAI(opt, aiProviderOpenAI, opt.OpenAIModel, client.Ask)
}

func (d *Dispatcher) wireAnthropic(opt Options) {
	if opt.AnthropicAPIKey == "" {
		d.aiErr = errAnthropicNoKey
		return
	}
	if opt.AnthropicModel == "" {
		d.aiErr = errAnthropicNoModel
		return
	}
	base, err := normalizeBaseURL(opt.AnthropicBaseURL)
	if err != nil {
		d.aiErr = errAnthropicBadBase
		return
	}
	client := NewAnthropicClient(opt.AnthropicAPIKey, opt.AnthropicModel, opt.AnthropicMaxOutputTokens)
	client.BaseURL = base
	d.installAI(opt, aiProviderAnthropic, opt.AnthropicModel, client.Ask)
}

// wireCustom wires the user-typed OpenAI-compatible endpoint: the
// base URL is REQUIRED (there is no official endpoint to fall back
// to) and the key is optional (local servers usually need none -- an
// empty key sends no Authorization header).
func (d *Dispatcher) wireCustom(opt Options) {
	if opt.CustomBaseURL == "" {
		d.aiErr = errCustomNoBase
		return
	}
	if opt.CustomModel == "" {
		d.aiErr = errCustomNoModel
		return
	}
	base, err := normalizeBaseURL(opt.CustomBaseURL)
	if err != nil {
		d.aiErr = errCustomBadBase
		return
	}
	client := NewOpenAIClient(opt.CustomAPIKey, opt.CustomModel, opt.CustomMaxOutputTokens)
	client.BaseURL = base
	client.Name = "custom" // errors name the custom section, not OpenAI
	d.installAI(opt, aiProviderCustom, opt.CustomModel, client.Ask)
}

// installAI installs one wired client as aiFn, in front of the
// persistent answer cache. cacheModel entries are keyed
// "<provider>/<configured model>" (see wireAI); cfgModel is the
// configured model name cache hits report (misses report the
// server-resolved name).
func (d *Dispatcher) installAI(opt Options, provider, cfgModel string, ask askFunc) {
	cacheModel := provider + "/" + cfgModel
	cache := NewAICache(opt.AICachePath)
	cache.Logf = opt.Logf // one-shot corrupt-file note on the lazy load
	d.aiErr = ""
	d.aiFn = func(ctx context.Context, query string) (*AIPreview, error) {
		// Cache lookups key on the CONFIGURED provider+model, so a
		// provider or model change in config never serves another
		// backend's answer.
		if answer, ok := cache.Get(cacheModel, query); ok {
			return &AIPreview{Query: query, Answer: answer, Model: cfgModel, Cached: true}, nil
		}
		answer, model, err := ask(ctx, query)
		if err != nil {
			return nil, err
		}
		if err := cache.Put(cacheModel, query, answer); err != nil {
			d.logf("preview: AI cache: %v (answer not persisted)", err)
		}
		return &AIPreview{Query: query, Answer: answer, Model: model, Cached: false}, nil
	}
}
