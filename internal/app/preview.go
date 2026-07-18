package app

import (
	"context"

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
)

// PreviewConfigInfo is the GetPreviewConfig answer: whether the pane
// is on, and whether the web-search/AI providers have credentials
// (config key or environment variable). The keys themselves never
// cross to the frontend.
type PreviewConfigInfo struct {
	Enabled          bool `json:"enabled"`
	KagiConfigured   bool `json:"kagiConfigured"`
	OpenAIConfigured bool `json:"openaiConfigured"`
}

// startPreview brings the preview layer up once, at Startup: nothing
// at all while the pane is disabled (every bound method degrades to a
// no-op on the nil dispatcher), otherwise a preview.Dispatcher whose
// parent context Shutdown cancels.
func (a *App) startPreview() {
	if !a.opt.Preview.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := preview.New(ctx, preview.Options{
		TextMaxKB:     a.opt.Preview.TextMaxKB,
		ImageMaxEdge:  a.opt.Preview.ImageMaxEdge,
		DirMaxEntries: a.opt.Preview.DirMaxEntries,
		Emit:          a.previewEmit,
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
// never exposed (or logged).
func (a *App) GetPreviewConfig() PreviewConfigInfo {
	p := a.opt.Preview
	return PreviewConfigInfo{
		Enabled:          p.Enabled,
		KagiConfigured:   p.Kagi.APIKey != "" || a.plat.getenv(envKagiAPIKey) != "",
		OpenAIConfigured: p.OpenAI.APIKey != "" || a.plat.getenv(envOpenAIAPIKey) != "",
	}
}

// FetchWebPreview is the explicit web-search preview trigger. Phase-3
// stub: it answers with an error payload so the bound surface and the
// frontend contract are final now.
func (a *App) FetchWebPreview(query string, gen int) {
	a.previewGen.Store(int64(gen))
	a.previewEmit(preview.Payload{
		Gen:  gen,
		Kind: preview.KindError,
		Err:  "web search lands later in this PR",
	})
}

// FetchAIPreview is the explicit AI answer preview trigger. Phase-3
// stub, like FetchWebPreview.
func (a *App) FetchAIPreview(query string, gen int) {
	a.previewGen.Store(int64(gen))
	a.previewEmit(preview.Payload{
		Gen:  gen,
		Kind: preview.KindError,
		Err:  "AI answers land later in this PR",
	})
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
