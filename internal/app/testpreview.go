package app

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
)

// previewTestTimeout bounds one Test-button probe end to end. Bound
// methods run on their own goroutines, so blocking here never stalls
// the UI thread; the frontend disables the button while the promise
// is in flight.
const previewTestTimeout = 15 * time.Second

// PreviewProviderTest carries the config editor's CANDIDATE values
// for one provider probe -- the working copy's current, possibly
// unsaved fields, so a key or endpoint can be tested BEFORE saving.
// The values are re-validated Go-side (preview.ProbeProvider bounds
// every field and normalizes the base URL; defense in depth -- the
// frontend merely echoes editor state).
type PreviewProviderTest struct {
	// Provider selects the endpoint: "kagi", "openai", "anthropic",
	// or "custom".
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
}

// TestPreviewProvider runs ONE minimal real request against the
// candidate provider configuration and answers honest ok/error --
// the terse message carries the HTTP status and provider error text,
// never key material. Empty candidate fields resolve through the SAME
// environment fallbacks the live dispatcher applies (KAGI_API_KEY,
// OPENAI_API_KEY / OPENAI_BASE_URL, ANTHROPIC_API_KEY /
// ANTHROPIC_BASE_URL; custom has none), so the probe exercises
// exactly the configuration a save would produce. NOTE the Kagi probe
// spends one real search credit; the AI probes cap the answer at a
// few tokens.
func (a *App) TestPreviewProvider(req PreviewProviderTest) preview.ProbeResult {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	key := req.APIKey
	base := req.BaseURL
	switch provider {
	case "kagi":
		if key == "" {
			key = a.plat.getenv(envKagiAPIKey)
		}
	case config.AIProviderOpenAI:
		if key == "" {
			key = a.plat.getenv(envOpenAIAPIKey)
		}
		if base == "" {
			base = a.plat.getenv(envOpenAIBaseURL)
		}
	case config.AIProviderAnthropic:
		if key == "" {
			key = a.plat.getenv(envAnthropicAPIKey)
		}
		if base == "" {
			base = a.plat.getenv(envAnthropicBaseURL)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewTestTimeout)
	defer cancel()
	res := preview.ProbeProvider(ctx, preview.ProbeParams{
		Provider: provider,
		APIKey:   key,
		BaseURL:  base,
		Model:    req.Model,
	})
	// The outcome message is log-safe by construction (terse client
	// errors, never keys or base URLs); the candidate values are not.
	log.Printf("preview: test %s: ok=%v (%s)", provider, res.OK, res.Message)
	return res
}
