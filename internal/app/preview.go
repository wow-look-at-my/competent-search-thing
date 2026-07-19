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
	envKagiAPIKey   = "KAGI_API_KEY"
	envOpenAIAPIKey = "OPENAI_API_KEY"
	// envOpenAIBaseURL is the SDK-conventional endpoint override: an
	// empty preview.openai.baseUrl defers to it, e.g. for an
	// OpenAI-compatible server. The Kagi base URL deliberately has NO
	// env fallback (config only).
	envOpenAIBaseURL = "OPENAI_BASE_URL"
)

// PreviewConfigInfo is the GetPreviewConfig answer: whether the pane
// is on, whether the web-search/AI providers have credentials (config
// key or environment variable), and the pixel width the left results
// column keeps while the pane is on (the flag-off bar width, config
// window.width). The keys themselves never cross to the frontend.
type PreviewConfigInfo struct {
	Enabled          bool `json:"enabled"`
	KagiConfigured   bool `json:"kagiConfigured"`
	OpenAIConfigured bool `json:"openaiConfigured"`
	ResultsWidth     int  `json:"resultsWidth"`
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
	if !a.previewConfig().Enabled {
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
	if next.Preview.Enabled {
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
	cachePath := ""
	if dir, err := config.Dir(); err == nil {
		cachePath = filepath.Join(dir, aiCacheFileName)
	} else {
		log.Printf("preview: %v (AI answer cache not persisted)", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := preview.New(ctx, preview.Options{
		TextMaxKB:             p.TextMaxKB,
		ImageMaxEdge:          p.ImageMaxEdge,
		DirMaxEntries:         p.DirMaxEntries,
		Emit:                  a.previewEmit,
		KagiAPIKey:            kagiKey,
		KagiBaseURL:           p.Kagi.BaseURL,
		KagiMaxResults:        p.Kagi.MaxResults,
		OpenAIAPIKey:          openaiKey,
		OpenAIBaseURL:         openaiBase,
		OpenAIModel:           p.OpenAI.Model,
		OpenAIMaxOutputTokens: p.OpenAI.MaxOutputTokens,
		AICachePath:           cachePath,
		Logf:                  log.Printf,
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
		Enabled:          p.Enabled,
		KagiConfigured:   p.Kagi.APIKey != "" || a.plat.getenv(envKagiAPIKey) != "",
		OpenAIConfigured: p.OpenAI.APIKey != "" || a.plat.getenv(envOpenAIAPIKey) != "",
		ResultsWidth:     a.resultsWidth(),
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
