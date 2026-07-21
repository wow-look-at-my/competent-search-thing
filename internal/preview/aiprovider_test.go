package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// newAITestServer answers both AI wire shapes: /v1/responses (OpenAI
// compatible) and /v1/messages (Anthropic), counting hits and
// recording the last Authorization header value seen.
func newAITestServer(t *testing.T, answer string) (*httptest.Server, *atomic.Int64, *atomic.Value) {
	t.Helper()
	var hits atomic.Int64
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"status":"completed","model":"resolved-model",
				"output":[{"type":"message","content":[{"type":"output_text","text":"` + answer + `"}]}]}`))
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"model":"resolved-model","stop_reason":"end_turn",
				"content":[{"type":"text","text":"` + answer + `"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &auth
}

func TestWireAnthropicProviderEndToEnd(t *testing.T) {
	srv, hits, _ := newAITestServer(t, "claude says hi")
	ch := make(chan Payload, 8)
	d := New(context.Background(), Options{
		Emit:                     func(p Payload) { ch <- p },
		AIProvider:               "anthropic",
		AnthropicAPIKey:          "sk-ant",
		AnthropicBaseURL:         srv.URL,
		AnthropicModel:           "claude-haiku-4-5",
		AnthropicMaxOutputTokens: 16,
		AICachePath:              filepath.Join(t.TempDir(), "aicache.json"),
	})
	require.True(t, d.AIConfigured())

	d.FetchAI("q", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindAI, p.Kind)
	require.Equal(t, "claude says hi", p.AI.Answer)
	require.Equal(t, "resolved-model", p.AI.Model)
	require.False(t, p.AI.Cached)
	require.Equal(t, int64(1), hits.Load())

	// The second identical query is a cache hit reporting the
	// CONFIGURED model, with zero network.
	d.FetchAI("q", 2)
	p = waitPayload(t, ch)
	require.True(t, p.AI.Cached)
	require.Equal(t, "claude-haiku-4-5", p.AI.Model)
	require.Equal(t, int64(1), hits.Load())
}

func TestWireCustomProviderKeylessEndToEnd(t *testing.T) {
	srv, hits, auth := newAITestServer(t, "local answer")
	ch := make(chan Payload, 8)
	d := New(context.Background(), Options{
		Emit:                  func(p Payload) { ch <- p },
		AIProvider:            "custom",
		CustomBaseURL:         srv.URL,
		CustomModel:           "llama3",
		CustomMaxOutputTokens: 16,
	})
	require.True(t, d.AIConfigured(), "a keyless custom endpoint is usable")

	d.FetchAI("q", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindAI, p.Kind)
	require.Equal(t, "local answer", p.AI.Answer)
	require.Equal(t, int64(1), hits.Load())
	require.Equal(t, "", auth.Load(), "an empty key sends NO Authorization header")
}

func TestWireCustomProviderErrorsNameCustom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	ch := make(chan Payload, 4)
	d := New(context.Background(), Options{
		Emit:          func(p Payload) { ch <- p },
		AIProvider:    "custom",
		CustomBaseURL: srv.URL,
		CustomModel:   "llama3",
	})
	d.FetchAI("q", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, "custom: HTTP 404", p.Err,
		"custom endpoint errors name the custom section, never OpenAI")
}

// TestAIProviderUnusableConfigurations pins every provider's honest
// fetch-path message: which knob is missing or invalid, named without
// quoting any value.
func TestAIProviderUnusableConfigurations(t *testing.T) {
	cases := []struct {
		name string
		opt  Options
		want string
	}{
		{"openai no key", Options{AIProvider: "openai"}, errAINoKey},
		{"openai no model", Options{AIProvider: "openai", OpenAIAPIKey: "k"}, errAINoModel},
		{"openai bad base", Options{AIProvider: "openai", OpenAIAPIKey: "k", OpenAIModel: "m", OpenAIBaseURL: "ftp://x"}, errAIBadBase},
		{"anthropic no key", Options{AIProvider: "anthropic"}, errAnthropicNoKey},
		{"anthropic no model", Options{AIProvider: "anthropic", AnthropicAPIKey: "k"}, errAnthropicNoModel},
		{"anthropic bad base", Options{AIProvider: "anthropic", AnthropicAPIKey: "k", AnthropicModel: "m", AnthropicBaseURL: "not a url"}, errAnthropicBadBase},
		{"custom no base", Options{AIProvider: "custom", CustomModel: "m"}, errCustomNoBase},
		{"custom no model", Options{AIProvider: "custom", CustomBaseURL: "http://h.example"}, errCustomNoModel},
		{"custom bad base", Options{AIProvider: "custom", CustomBaseURL: "no-scheme.example", CustomModel: "m"}, errCustomBadBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan Payload, 4)
			tc.opt.Emit = func(p Payload) { ch <- p }
			d := New(context.Background(), tc.opt)
			require.False(t, d.AIConfigured())
			d.FetchAI("q", 1)
			p := waitPayload(t, ch)
			require.Equal(t, KindError, p.Kind)
			require.Equal(t, tc.want, p.Err)
		})
	}
}

// TestAICacheKeysAreProviderQualified: two providers configured with
// the SAME model string over the SAME cache file never serve each
// other's answers -- the cache key carries the provider.
func TestAICacheKeysAreProviderQualified(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "aicache.json")

	srvA, hitsA, _ := newAITestServer(t, "openai answer")
	chA := make(chan Payload, 4)
	dA := New(context.Background(), Options{
		Emit:          func(p Payload) { chA <- p },
		AIProvider:    "openai",
		OpenAIAPIKey:  "k",
		OpenAIBaseURL: srvA.URL,
		OpenAIModel:   "shared-model",
		AICachePath:   cachePath,
	})
	dA.FetchAI("q", 1)
	require.Equal(t, "openai answer", waitPayload(t, chA).AI.Answer)
	require.Equal(t, int64(1), hitsA.Load())

	// Same model string, same cache file, DIFFERENT provider: a fresh
	// dial, not a cross-backend cache hit.
	srvB, hitsB, _ := newAITestServer(t, "custom answer")
	chB := make(chan Payload, 4)
	dB := New(context.Background(), Options{
		Emit:          func(p Payload) { chB <- p },
		AIProvider:    "custom",
		CustomBaseURL: srvB.URL,
		CustomModel:   "shared-model",
		AICachePath:   cachePath,
	})
	dB.FetchAI("q", 1)
	p := waitPayload(t, chB)
	require.Equal(t, "custom answer", p.AI.Answer)
	require.False(t, p.AI.Cached)
	require.Equal(t, int64(1), hitsB.Load(), "the custom provider never reads OpenAI's cached answer")
}

// TestWireAIEmptyProviderMeansOpenAI: the pre-selector zero value
// keeps the historical wiring byte-identical.
func TestWireAIEmptyProviderMeansOpenAI(t *testing.T) {
	d := New(context.Background(), Options{OpenAIAPIKey: "k", OpenAIModel: "m"})
	require.True(t, d.AIConfigured())
	d = New(context.Background(), Options{AIProvider: "", AnthropicAPIKey: "k", AnthropicModel: "m"})
	require.False(t, d.AIConfigured(), "an unselected anthropic section wires nothing")
}
