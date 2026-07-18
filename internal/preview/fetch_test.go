package preview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchWebEmitsSingleWebPayload(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.webFn = func(_ context.Context, query string) (*WebPreview, error) {
		return &WebPreview{
			Query:   query,
			Results: []WebResult{{Title: "T", URL: "https://t.example", Snippet: "s"}},
			Cached:  true,
		}, nil
	}
	d.FetchWeb("some query", 7)
	p := waitPayload(t, ch)
	require.Equal(t, 7, p.Gen)
	require.Equal(t, KindWeb, p.Kind)
	require.Equal(t, "some query", p.Title)
	require.NotNil(t, p.Web)
	require.Equal(t, "some query", p.Web.Query)
	require.True(t, p.Web.Cached, "the provider's cached flag rides the payload")
	require.Len(t, p.Web.Results, 1)
	require.GreaterOrEqual(t, p.DurMS, int64(0))
	assertNoPayload(t, ch) // exactly one payload per accepted fetch
}

func TestFetchAIEmitsSingleAIPayload(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.aiFn = func(_ context.Context, query string) (*AIPreview, error) {
		return &AIPreview{Query: query, Answer: "42", Model: "m", Cached: false}, nil
	}
	d.FetchAI("meaning of life", 3)
	p := waitPayload(t, ch)
	require.Equal(t, 3, p.Gen)
	require.Equal(t, KindAI, p.Kind)
	require.NotNil(t, p.AI)
	require.Equal(t, "42", p.AI.Answer)
	require.False(t, p.AI.Cached)
	assertNoPayload(t, ch)
}

func TestFetchBlankQueryEmitsError(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.webFn = func(context.Context, string) (*WebPreview, error) {
		t.Error("a blank query must never reach the provider")
		return nil, nil
	}
	d.FetchWeb("   ", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, "empty query", p.Err)

	d.FetchAI("", 2)
	p = waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, "empty query", p.Err)
}

func TestFetchWithoutKeysNamesTheConfigKeys(t *testing.T) {
	d, ch := newTestDispatcher(t) // no keys: webFn/aiFn stay nil
	require.False(t, d.WebConfigured())
	require.False(t, d.AIConfigured())

	d.FetchWeb("q", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, "kagi: no API key (preview.kagi.apiKey or KAGI_API_KEY)", p.Err)

	d.FetchAI("q", 2)
	p = waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, "openai: no API key (preview.openai.apiKey or OPENAI_API_KEY)", p.Err)
}

func TestFetchProviderErrorEmitsErrorPayload(t *testing.T) {
	d, ch := newTestDispatcher(t)
	d.webFn = func(context.Context, string) (*WebPreview, error) {
		return nil, errors.New("kagi: HTTP 500")
	}
	d.FetchWeb("q", 4)
	p := waitPayload(t, ch)
	require.Equal(t, KindError, p.Kind)
	require.Equal(t, 4, p.Gen)
	require.Equal(t, "kagi: HTTP 500", p.Err)
}

func TestFetchSupersedesFilePreviewAndViceVersa(t *testing.T) {
	d, ch := newTestDispatcher(t)
	entered := make(chan struct{}, 1)
	d.textFn = func(ctx context.Context, _ string, _ int) (*TextPreview, error) {
		entered <- struct{}{}
		<-ctx.Done() // block until superseded
		return nil, ctx.Err()
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	// A fetch supersedes an in-flight file preview: the file's rich
	// payload never lands, the web payload does.
	d.Preview(Target{Kind: TargetFile, Path: path}, 1)
	require.Equal(t, KindMeta, waitPayload(t, ch).Kind)
	<-entered
	d.webFn = func(_ context.Context, query string) (*WebPreview, error) {
		return &WebPreview{Query: query}, nil
	}
	d.FetchWeb("q", 2)
	p := waitPayload(t, ch)
	require.Equal(t, KindWeb, p.Kind)
	require.Equal(t, 2, p.Gen)
	assertNoPayload(t, ch) // gen 1's cancelled preview emits nothing more

	// And a preview supersedes an in-flight fetch: the blocked web
	// provider's answer is suppressed.
	released := make(chan struct{})
	d.webFn = func(ctx context.Context, _ string) (*WebPreview, error) {
		entered <- struct{}{}
		<-released
		return &WebPreview{Query: "stale"}, nil
	}
	d.FetchWeb("slow", 3)
	<-entered
	d.Preview(Target{Kind: TargetPlugin, Title: "row"}, 4)
	p = waitPayload(t, ch)
	require.Equal(t, KindMeta, p.Kind)
	require.Equal(t, 4, p.Gen)
	close(released)
	assertNoPayload(t, ch) // the superseded fetch emits nothing
}

func TestFetchSupersededByNoneCancel(t *testing.T) {
	d, ch := newTestDispatcher(t)
	entered := make(chan struct{}, 1)
	d.aiFn = func(ctx context.Context, _ string) (*AIPreview, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.FetchAI("q", 1)
	<-entered
	d.Preview(Target{Kind: TargetNone}, 2) // the frontend's deselect cancel
	assertNoPayload(t, ch)                 // a cancelled fetch emits nothing, not even its error
}

func TestFetchErrMsgSpellsOutTimeouts(t *testing.T) {
	require.Equal(t, "web search timed out after 10s",
		fetchErrMsg(context.DeadlineExceeded, "web search", webTimeout))
	require.Equal(t, "AI answer timed out after 1m30s",
		fetchErrMsg(context.DeadlineExceeded, "AI answer", aiTimeout))
	require.Equal(t, "boom", fetchErrMsg(errors.New("boom"), "web search", webTimeout))
}

func TestNewWiresProvidersFromKeys(t *testing.T) {
	d := New(context.Background(), Options{})
	require.False(t, d.WebConfigured())
	require.False(t, d.AIConfigured())

	d = New(context.Background(), Options{KagiAPIKey: "k"})
	require.True(t, d.WebConfigured())
	require.False(t, d.AIConfigured())

	d = New(context.Background(), Options{OpenAIAPIKey: "o", OpenAIModel: "m", OpenAIMaxOutputTokens: 8})
	require.False(t, d.WebConfigured())
	require.True(t, d.AIConfigured())
}

// TestFetchAIEndToEndWithCache drives the aiFn wiring shape New
// builds -- OpenAI client + persistent cache -- against an httptest
// server (rebuilt here because Options deliberately carries no
// BaseURL knob; production always talks to the real endpoint): the
// first fetch dials and persists, the second is served from the cache
// file with zero network and Cached=true.
func TestFetchAIEndToEndWithCache(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status":"completed","model":"m-resolved",
			"output":[{"type":"message","content":[{"type":"output_text","text":"cached answer"}]}]}`))
	}))
	defer srv.Close()

	cachePath := filepath.Join(t.TempDir(), "aicache.json")
	openai := NewOpenAIClient("sk-test", "m", 16)
	openai.BaseURL = srv.URL
	openai.HTTPClient = srv.Client()
	cache := NewAICache(cachePath)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := make(chan Payload, 8)
	d := New(ctx, Options{Emit: func(p Payload) { ch <- p }})
	d.aiFn = func(fctx context.Context, query string) (*AIPreview, error) {
		if answer, ok := cache.Get(openai.Model(), query); ok {
			return &AIPreview{Query: query, Answer: answer, Model: openai.Model(), Cached: true}, nil
		}
		answer, model, err := openai.Ask(fctx, query)
		if err != nil {
			return nil, err
		}
		_ = cache.Put(openai.Model(), query, answer)
		return &AIPreview{Query: query, Answer: answer, Model: model, Cached: false}, nil
	}

	d.FetchAI("q", 1)
	p := waitPayload(t, ch)
	require.Equal(t, KindAI, p.Kind)
	require.False(t, p.AI.Cached)
	require.Equal(t, "cached answer", p.AI.Answer)
	require.Equal(t, "m-resolved", p.AI.Model)
	require.Equal(t, int64(1), hits.Load())

	// Second fetch: served from the persistent cache, zero network.
	d.FetchAI("q", 2)
	p = waitPayload(t, ch)
	require.Equal(t, KindAI, p.Kind)
	require.True(t, p.AI.Cached)
	require.Equal(t, "cached answer", p.AI.Answer)
	require.Equal(t, "m", p.AI.Model, "cache hits report the configured model")
	require.Equal(t, int64(1), hits.Load(), "the cache hit never dialed")

	// A FRESH cache over the same file (a new app run) still hits.
	cache2 := NewAICache(cachePath)
	answer, ok := cache2.Get("m", "q")
	require.True(t, ok)
	require.Equal(t, "cached answer", answer)
}
