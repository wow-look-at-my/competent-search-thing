package app

import (
	"context"
	"log"
	"path/filepath"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
)

// eventPreviewResult carries one preview payload for one selection
// generation; payload preview.Payload (see internal/preview). The
// frontend drops payloads whose gen is not the current one, mirroring
// eventPluginResults.
const eventPreviewResult = "preview:result"

// Environment fallbacks for the preview API keys: an empty config key
// defers to these (read through the getenv seam, so unit tests stay
// hermetic).
const (
	envKagiAPIKey      = "KAGI_API_KEY"
	envOpenAIAPIKey    = "OPENAI_API_KEY"
	envAnthropicAPIKey = "ANTHROPIC_API_KEY"
	// envOpenAIBaseURL / envAnthropicBaseURL are the SDK-conventional
	// endpoint overrides: an empty preview.<provider>.baseUrl defers
	// to the matching variable. The Kagi and custom base URLs
	// deliberately have NO env fallback (config only -- custom IS the
	// user-typed endpoint).
	envOpenAIBaseURL    = "OPENAI_BASE_URL"
	envAnthropicBaseURL = "ANTHROPIC_BASE_URL"
)

// PreviewConfigInfo is the GetPreviewConfig answer: whether the pane
// is on, whether the web-search/AI providers have credentials (config
// key or environment variable), which AI provider is selected, and
// the pixel width the left results column keeps while the pane is on
// (the flag-off bar width, config window.width). The keys themselves
// never cross to the frontend.
type PreviewConfigInfo struct {
	Enabled        bool `json:"enabled"`
	KagiConfigured bool `json:"kagiConfigured"`
	// AIProvider is the selected AI answer provider ("openai",
	// "anthropic", or "custom") -- the frontend's strip-button hint
	// names the right config keys with it.
	AIProvider string `json:"aiProvider"`
	// AIConfigured reports whether the SELECTED provider is usable:
	// a key (config or environment) for openai/anthropic, a base URL
	// plus model for custom.
	AIConfigured bool `json:"aiConfigured"`
	ResultsWidth int  `json:"resultsWidth"`
}

// aiCacheFileName is the persistent AI answer cache, next to
// config.json (delete the file to clear the cache).
const aiCacheFileName = "aicache.json"

// startPreview brings the preview layer up once, at Startup: nothing
// at all while the pane is disabled (every bound method degrades to a
// no-op on the nil dispatcher), otherwise a preview.Dispatcher whose
// parent context Shutdown cancels. The provider API keys resolve in
// buildPreviewDispatcher -- config value first, else the environment
// variable through the getenv seam -- exactly the resolution
// GetPreviewConfig reports, and the OpenAI base URL resolves the same
// way (preview.openai.baseUrl, else OPENAI_BASE_URL; the Kagi base is
// config-only). The resolved keys and base URLs flow only into the
// dispatcher options and are never logged (a base URL may carry
// userinfo); the dispatcher trims one trailing "/" and turns an
// invalid base into a terse fetch-path error instead of a broken
// client.
func (a *App) startPreview() {
	if !config.Enabled(a.previewConfig().Enabled) {
		return
	}
	a.buildPreviewDispatcher()
}

// previewConfig returns the live preview configuration (seeded from
// Options in New, swapped by applyPreview).
func (a *App) previewConfig() config.PreviewConfig {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	return a.previewCfg
}

// applyPreview is the config live-apply path for the preview section:
// store the new configuration, tear the old dispatcher down
// (shutdownPreview cancels its parent context, aborting an in-flight
// request; the bound methods are nil-dispatcher-safe throughout), and
// build a fresh one when the pane is enabled. The frontend refetches
// GetPreviewConfig on config:changed and mounts/unmounts the pane;
// the window-size half of a preview flip rides the shared window-size
// group.
func (a *App) applyPreview(next *config.Config) error {
	a.previewMu.Lock()
	a.previewCfg = next.Preview
	a.previewMu.Unlock()
	a.shutdownPreview()
	if config.Enabled(next.Preview.Enabled) {
		a.buildPreviewDispatcher()
	}
	return nil
}

// buildPreviewDispatcher builds and installs one preview.Dispatcher
// from the live preview configuration (see startPreview for the key
// and base-URL resolution contract).
func (a *App) buildPreviewDispatcher() {
	p := a.previewConfig()
	kagiKey := p.Kagi.APIKey
	if kagiKey == "" {
		kagiKey = a.plat.getenv(envKagiAPIKey)
	}
	openaiKey := p.OpenAI.APIKey
	if openaiKey == "" {
		openaiKey = a.plat.getenv(envOpenAIAPIKey)
	}
	openaiBase := p.OpenAI.BaseURL
	if openaiBase == "" {
		openaiBase = a.plat.getenv(envOpenAIBaseURL)
	}
	anthropicKey := p.Anthropic.APIKey
	if anthropicKey == "" {
		anthropicKey = a.plat.getenv(envAnthropicAPIKey)
	}
	anthropicBase := p.Anthropic.BaseURL
	if anthropicBase == "" {
		anthropicBase = a.plat.getenv(envAnthropicBaseURL)
	}
	cachePath := ""
	if dir, err := config.Dir(); err == nil {
		cachePath = filepath.Join(dir, aiCacheFileName)
	} else {
		log.Printf("preview: %v (AI answer cache not persisted)", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := preview.New(ctx, preview.Options{
		TextMaxKB:                p.TextMaxKB,
		ImageMaxEdge:             p.ImageMaxEdge,
		DirMaxEntries:            p.DirMaxEntries,
		Emit:                     a.previewEmit,
		KagiAPIKey:               kagiKey,
		KagiBaseURL:              p.Kagi.BaseURL,
		KagiMaxResults:           p.Kagi.MaxResults,
		AIProvider:               p.AIProvider,
		OpenAIAPIKey:             openaiKey,
		OpenAIBaseURL:            openaiBase,
		OpenAIModel:              p.OpenAI.Model,
		OpenAIMaxOutputTokens:    p.OpenAI.MaxOutputTokens,
		AnthropicAPIKey:          anthropicKey,
		AnthropicBaseURL:         anthropicBase,
		AnthropicModel:           p.Anthropic.Model,
		AnthropicMaxOutputTokens: p.Anthropic.MaxOutputTokens,
		CustomAPIKey:             p.Custom.APIKey,
		CustomBaseURL:            p.Custom.BaseURL,
		CustomModel:              p.Custom.Model,
		CustomMaxOutputTokens:    p.Custom.MaxOutputTokens,
		AICachePath:              cachePath,
		Logf:                     log.Printf,
	})
	a.previewMu.Lock()
	a.previewDisp = d
	a.previewCancel = cancel
	a.previewMu.Unlock()
}

// previewEmit forwards one dispatcher payload to the frontend unless
// a newer generation superseded it -- the same gate QueryPlugins'
// emit closure applies. It runs on dispatcher request goroutines;
// emitEvent is already nil-ctx-safe and goroutine-safe.
func (a *App) previewEmit(p preview.Payload) {
	if a.previewGen.Load() != int64(p.Gen) {
		return
	}
	a.emitEvent(eventPreviewResult, p)
}

// previewDispatcher returns the dispatcher, nil while the pane is
// disabled (or before Startup).
func (a *App) previewDispatcher() *preview.Dispatcher {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	return a.previewDisp
}

// QueryPreview asks for target to be previewed under generation gen
// (the frontend's selection sequence number). The answer arrives
// asynchronously as eventPreviewResult payloads carrying gen; a new
// call cancels the in-flight request, and a TargetNone target cancels
// without starting a new one. A nil dispatcher (pane disabled) is a
// no-op, so the bound surface is always safe to call.
func (a *App) QueryPreview(target preview.Target, gen int) {
	a.previewGen.Store(int64(gen))
	d := a.previewDispatcher()
	if d == nil {
		return
	}
	d.Preview(target, gen)
}

// GetPreviewConfig reports the preview pane's frontend-relevant
// configuration. "Configured" means a non-empty API key in the config
// or the matching environment variable; the key values themselves are
// never exposed (or logged). ResultsWidth is the live window.width
// (seeded from Options.ResultsWidth, swapped by the window-size
// applier), falling back to the flag-off default so a zero value never
// produces a collapsed column. The whole answer follows the LIVE
// configuration -- the frontend refetches it on config:changed.
func (a *App) GetPreviewConfig() PreviewConfigInfo {
	p := a.previewConfig()
	return PreviewConfigInfo{
		Enabled:        config.Enabled(p.Enabled),
		KagiConfigured: p.Kagi.APIKey != "" || a.plat.getenv(envKagiAPIKey) != "",
		AIProvider:     aiProviderName(p),
		AIConfigured:   a.aiConfigured(p),
		ResultsWidth:   a.resultsWidth(),
	}
}

// aiProviderName is the effective provider selector -- the config
// value with the pre-selector zero value reading as the default
// (Normalize repairs persisted configs, but Options-seeded test/boot
// state may carry the raw zero).
func aiProviderName(p config.PreviewConfig) string {
	if p.AIProvider == "" {
		return config.DefaultPreviewAIProvider
	}
	return p.AIProvider
}

// aiConfigured mirrors the dispatcher wiring's usability rule for the
// SELECTED provider: openai/anthropic need a key (config or
// environment), custom needs its base URL plus a model (the key is
// optional there). An invalid base URL still fails at fetch time with
// the terse message -- this is the strip button's cheap availability
// signal, not a validator.
func (a *App) aiConfigured(p config.PreviewConfig) bool {
	switch aiProviderName(p) {
	case config.AIProviderAnthropic:
		return p.Anthropic.APIKey != "" || a.plat.getenv(envAnthropicAPIKey) != ""
	case config.AIProviderCustom:
		return p.Custom.BaseURL != "" && p.Custom.Model != ""
	default:
		return p.OpenAI.APIKey != "" || a.plat.getenv(envOpenAIAPIKey) != ""
	}
}

// FetchWebPreview is the explicit web-search preview trigger -- the
// frontend's Ctrl+K button path, its ONLY call site. The dispatcher
// answers asynchronously with exactly one eventPreviewResult payload
// (kind "web" or "error") under gen; the fetch shares Preview's
// cancel/generation space, so it supersedes an in-flight file preview
// and vice versa. Nil dispatcher (pane disabled) = safe no-op.
func (a *App) FetchWebPreview(query string, gen int) {
	a.previewGen.Store(int64(gen))
	d := a.previewDispatcher()
	if d == nil {
		return
	}
	d.FetchWeb(query, gen)
}

// FetchAIPreview is the explicit AI answer preview trigger (Ctrl+I),
// with the same contract as FetchWebPreview.
func (a *App) FetchAIPreview(query string, gen int) {
	a.previewGen.Store(int64(gen))
	d := a.previewDispatcher()
	if d == nil {
		return
	}
	d.FetchAI(query, gen)
}

// shutdownPreview cancels the dispatcher's parent context (aborting
// an in-flight request; every later Preview call no-ops) and drops
// the handles. Idempotent and safe before startPreview.
func (a *App) shutdownPreview() {
	a.previewMu.Lock()
	cancel := a.previewCancel
	a.previewCancel = nil
	a.previewDisp = nil
	a.previewMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
