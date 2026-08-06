package preview

import (
	"context"
	"net/url"
)

// AI answer provider wiring: ONE provider, entirely user-described
// (config preview.ai -- base URL, model, optional key). There is no
// built-in provider list and no default endpoint: nothing is sent
// anywhere until the user names an endpoint. An unusable
// configuration leaves aiFn nil with aiErr carrying the honest
// fetch-path message naming the missing config key -- never a value.

// Fetch-path messages for an unavailable AI provider. They name
// config knobs but never quote values: keys are secret and a base URL
// may carry userinfo.
const (
	errAINoBase  = "ai: no API base URL (preview.ai.baseUrl)"
	errAIBadBase = "ai: invalid baseUrl (preview.ai.baseUrl)"
	errAINoModel = "ai: no model (preview.ai.model)"
)

// askFunc is the one-shot answer seam the AI client satisfies:
// prompt in, (answer, resolvedModel) out.
type askFunc func(ctx context.Context, prompt string) (string, string, error)

// wireAI wires the configured AI endpoint into aiFn/aiErr -- called
// once from New. The key is OPTIONAL (local servers usually need
// none); the base URL and the model are not, because neither can be
// guessed for an arbitrary server.
func (d *Dispatcher) wireAI(opt Options) {
	if opt.AIBaseURL == "" {
		d.aiErr = errAINoBase
		return
	}
	if opt.AIModel == "" {
		d.aiErr = errAINoModel
		return
	}
	base, err := normalizeBaseURL(opt.AIBaseURL)
	if err != nil {
		d.aiErr = errAIBadBase
		return
	}
	client := NewAIClient(opt.AIAPIKey, opt.AIModel, opt.AIMaxOutputTokens)
	client.BaseURL = base
	d.installAI(opt, base, opt.AIModel, client.Ask)
}

// installAI installs the wired client as aiFn, in front of the
// persistent answer cache.
func (d *Dispatcher) installAI(opt Options, base, cfgModel string, ask askFunc) {
	// Cache entries are keyed by model AND endpoint host, so pointing
	// the same model name at a different server never serves the
	// other one's answers. The HOST only: a base URL may carry
	// userinfo, and this string is written to the cache file.
	cacheModel := cfgModel
	if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
		cacheModel = cfgModel + "@" + u.Hostname()
	}
	cache := NewAICache(opt.AICachePath)
	cache.Logf = opt.Logf // one-shot corrupt-file note on the lazy load
	d.aiErr = ""
	d.aiFn = func(ctx context.Context, query string) (*AIPreview, error) {
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
