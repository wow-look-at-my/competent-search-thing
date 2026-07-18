package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/preview"
)

// previewTestOptions enables the pane with small caps.
func previewTestOptions() Options {
	return Options{Preview: config.PreviewConfig{
		Enabled:       true,
		WindowWidth:   1600,
		WindowHeight:  800,
		TextMaxKB:     4,
		ImageMaxEdge:  100,
		DirMaxEntries: 10,
	}}
}

func TestPreviewDisabledByDefault(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	require.Nil(t, a.previewDispatcher(), "no dispatcher while the pane is off")

	// Every bound method is a safe no-op.
	a.QueryPreview(preview.Target{Kind: preview.TargetFile, Path: "/tmp/x"}, 1)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, r.emitted(eventPreviewResult))
	require.Equal(t, int64(1), a.previewGen.Load(), "the generation still advances")

	info := a.GetPreviewConfig()
	require.False(t, info.Enabled)
	require.False(t, info.KagiConfigured)
	require.False(t, info.OpenAIConfigured)
}

func TestPreviewEnabledEmitsMetaThenRich(t *testing.T) {
	a, r := newTestApp(t, nil, previewTestOptions())
	a.Startup(context.Background())
	require.NotNil(t, a.previewDispatcher())

	dir := t.TempDir()
	path := filepath.Join(dir, "hello.md")
	require.NoError(t, os.WriteFile(path, []byte("# hi\n"), 0o644))

	a.QueryPreview(preview.Target{Kind: preview.TargetFile, Path: path}, 1)
	require.Eventually(t, func() bool {
		return len(r.emitted(eventPreviewResult)) >= 2
	}, 5*time.Second, 10*time.Millisecond, "meta then rich payloads arrive")

	events := r.emitted(eventPreviewResult)
	first := events[0].payload[0].(preview.Payload)
	require.Equal(t, preview.KindMeta, first.Kind)
	require.Equal(t, 1, first.Gen)
	second := events[1].payload[0].(preview.Payload)
	require.Equal(t, preview.KindText, second.Kind)
	require.Equal(t, "# hi\n", second.Text.Content)
	require.Equal(t, "markdown", second.Text.Lang)
}

func TestPreviewEmitDropsStaleGenerations(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.previewGen.Store(5)
	a.previewEmit(preview.Payload{Gen: 4, Kind: preview.KindMeta})
	require.Empty(t, r.emitted(eventPreviewResult), "stale generation never reaches the frontend")
	a.previewEmit(preview.Payload{Gen: 5, Kind: preview.KindMeta})
	require.Len(t, r.emitted(eventPreviewResult), 1, "current generation still emits")
}

func TestPreviewEmitBeforeStartupIsSafe(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	// No Startup: the runtime ctx is nil and emitEvent must no-op.
	a.previewEmit(preview.Payload{Gen: 0, Kind: preview.KindMeta})
}

func TestQueryPreviewSupersedes(t *testing.T) {
	a, r := newTestApp(t, nil, previewTestOptions())
	a.Startup(context.Background())
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o644))

	a.QueryPreview(preview.Target{Kind: preview.TargetFile, Path: path}, 1)
	a.QueryPreview(preview.Target{Kind: preview.TargetNone}, 2)
	require.Equal(t, int64(2), a.previewGen.Load())
	// Anything gen 1 managed to emit before the cancel is gated out;
	// nothing for gen 2 ever arrives (cancel-only). Give the request
	// goroutine a moment, then assert only gen-2-tagged silence.
	time.Sleep(100 * time.Millisecond)
	for _, e := range r.emitted(eventPreviewResult) {
		p := e.payload[0].(preview.Payload)
		require.NotEqual(t, 2, p.Gen, "a cancel-only target emits nothing")
	}
}

func TestGetPreviewConfigConfiguredDetection(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Preview: config.PreviewConfig{
		Enabled: true,
		Kagi:    config.PreviewKagiConfig{APIKey: "kagi-secret"},
	}})
	info := a.GetPreviewConfig()
	require.True(t, info.Enabled)
	require.True(t, info.KagiConfigured, "a config key counts as configured")
	require.False(t, info.OpenAIConfigured)

	// Environment variables count too (newTestApp pins getenv to "").
	b, _ := newTestApp(t, nil, Options{})
	b.plat.getenv = func(key string) string {
		if key == envOpenAIAPIKey {
			return "sk-from-env"
		}
		return ""
	}
	info = b.GetPreviewConfig()
	require.False(t, info.KagiConfigured)
	require.True(t, info.OpenAIConfigured, "the env fallback counts as configured")
}

func TestFetchPreviewDisabledIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())

	a.FetchWebPreview("some query", 3)
	a.FetchAIPreview("some query", 4)
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, r.emitted(eventPreviewResult), "no dispatcher, no payloads")
	require.Equal(t, int64(4), a.previewGen.Load(), "the generation still advances")
}

func TestFetchPreviewWithoutKeysEmitsNamedErrors(t *testing.T) {
	// newTestApp pins getenv to "" and the options carry no API keys,
	// so both providers are unconfigured. The no-key answer is
	// synchronous: no goroutine, no network.
	a, r := newTestApp(t, nil, previewTestOptions())
	a.Startup(context.Background())
	d := a.previewDispatcher()
	require.NotNil(t, d)
	require.False(t, d.WebConfigured())
	require.False(t, d.AIConfigured())

	a.FetchWebPreview("some query", 3)
	events := r.emitted(eventPreviewResult)
	require.Len(t, events, 1)
	p := events[0].payload[0].(preview.Payload)
	require.Equal(t, 3, p.Gen)
	require.Equal(t, preview.KindError, p.Kind)
	require.Equal(t, "kagi: no API key (preview.kagi.apiKey or KAGI_API_KEY)", p.Err)

	a.FetchAIPreview("some query", 4)
	events = r.emitted(eventPreviewResult)
	require.Len(t, events, 2)
	p = events[1].payload[0].(preview.Payload)
	require.Equal(t, 4, p.Gen)
	require.Equal(t, preview.KindError, p.Kind)
	require.Equal(t, "openai: no API key (preview.openai.apiKey or OPENAI_API_KEY)", p.Err)
}

func TestFetchPreviewBlankQueryEmitsError(t *testing.T) {
	a, r := newTestApp(t, nil, previewTestOptions())
	a.Startup(context.Background())

	a.FetchWebPreview("   ", 1)
	events := r.emitted(eventPreviewResult)
	require.Len(t, events, 1)
	p := events[0].payload[0].(preview.Payload)
	require.Equal(t, preview.KindError, p.Kind)
	require.Equal(t, "empty query", p.Err)
}

func TestStartPreviewResolvesKeysLikeGetPreviewConfig(t *testing.T) {
	// Environment fallbacks resolve through the getenv seam.
	opt := previewTestOptions()
	a, _ := newTestApp(t, nil, opt)
	a.plat.getenv = func(key string) string {
		switch key {
		case envKagiAPIKey:
			return "kagi-from-env"
		case envOpenAIAPIKey:
			return "openai-from-env"
		}
		return ""
	}
	a.Startup(context.Background())
	d := a.previewDispatcher()
	require.NotNil(t, d)
	require.True(t, d.WebConfigured(), "the KAGI_API_KEY fallback configures the provider")
	require.True(t, d.AIConfigured(), "the OPENAI_API_KEY fallback configures the provider")
	info := a.GetPreviewConfig()
	require.True(t, info.KagiConfigured, "GetPreviewConfig agrees with the dispatcher")
	require.True(t, info.OpenAIConfigured)

	// Config keys win without any environment.
	opt2 := previewTestOptions()
	opt2.Preview.Kagi.APIKey = "kagi-from-config"
	b, _ := newTestApp(t, nil, opt2)
	b.Startup(context.Background())
	d = b.previewDispatcher()
	require.True(t, d.WebConfigured())
	require.False(t, d.AIConfigured())
	info = b.GetPreviewConfig()
	require.True(t, info.KagiConfigured)
	require.False(t, info.OpenAIConfigured)
}

func TestShutdownCancelsPreview(t *testing.T) {
	a, r := newTestApp(t, nil, previewTestOptions())
	a.Startup(context.Background())
	d := a.previewDispatcher()
	require.NotNil(t, d)

	a.Shutdown(context.Background())
	require.Nil(t, a.previewDispatcher(), "shutdown drops the dispatcher")

	// The old dispatcher's parent context is cancelled: a straggler
	// request emits nothing, even with a matching generation.
	a.previewGen.Store(9)
	d.Preview(preview.Target{Kind: preview.TargetPlugin, Title: "t"}, 9)
	time.Sleep(100 * time.Millisecond)
	for _, e := range r.emitted(eventPreviewResult) {
		require.NotEqual(t, 9, e.payload[0].(preview.Payload).Gen)
	}

	a.Shutdown(context.Background()) // idempotent
}

func TestWindowSizeDefaultsAndOverride(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	w, h := a.windowSize()
	require.Equal(t, config.DefaultWindowWidth, w)
	require.Equal(t, config.DefaultWindowHeight, h)

	b, _ := newTestApp(t, nil, Options{WindowWidth: 1600, WindowHeight: 800})
	w, h = b.windowSize()
	require.Equal(t, 1600, w)
	require.Equal(t, 800, h)

	// A partial override falls back to the config default per axis
	// (see windowSize), so the unset dimension stays safe.
	c, _ := newTestApp(t, nil, Options{WindowWidth: 1600})
	w, h = c.windowSize()
	require.Equal(t, 1600, w)
	require.Equal(t, config.DefaultWindowHeight, h)
}

func TestShowPositionsWithConfiguredWindowSize(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 1600, WindowHeight: 800})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540 // cursor on the primary
	r.displays = testDisplays()
	r.winX, r.winY = 100, 100 // window already on the primary
	a.Startup(context.Background())

	a.showOnCursorDisplay()

	// BarPosition with the widened window: x = (1920-1600)/2 = 160,
	// y = 1080/3 - 800/3 = 360 - 266 = 94 (integer division).
	wantX, wantY := platform.BarPosition(r.displays[0], 1600, 800)
	require.Equal(t, []int{wantX}, r.setPosX)
	require.Equal(t, []int{wantY}, r.setPosY)
}

// lastPreviewPayload waits until at least n preview:result events
// arrived and returns the newest one's payload.
func lastPreviewPayload(t *testing.T, r *seamRecorder, n int) preview.Payload {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(r.emitted(eventPreviewResult)) >= n
	}, 5*time.Second, 10*time.Millisecond, "preview payload arrives")
	events := r.emitted(eventPreviewResult)
	return events[len(events)-1].payload[0].(preview.Payload)
}

func TestStartPreviewResolvesOpenAIBaseURL(t *testing.T) {
	// Keep the AI answer cache out of the real config dir.
	t.Setenv(config.EnvConfigDir, t.TempDir())
	answerWith := func(text string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"completed","model":"m","output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}`))
		}))
	}
	cfgSrv := answerWith("from-config")
	defer cfgSrv.Close()
	envSrv := answerWith("from-env")
	defer envSrv.Close()

	baseOpts := func() Options {
		o := previewTestOptions()
		o.Preview.OpenAI.APIKey = "sk-test"
		o.Preview.OpenAI.Model = "m"
		o.Preview.OpenAI.MaxOutputTokens = 16
		return o
	}
	envGetenv := func(key string) string {
		if key == envOpenAIBaseURL {
			return envSrv.URL
		}
		return ""
	}

	// The config value wins over the environment (which answers
	// differently and must not be dialed).
	opt := baseOpts()
	opt.Preview.OpenAI.BaseURL = cfgSrv.URL
	a, ra := newTestApp(t, nil, opt)
	a.plat.getenv = envGetenv
	a.Startup(context.Background())
	a.FetchAIPreview("q config", 1)
	p := lastPreviewPayload(t, ra, 1)
	require.Equal(t, preview.KindAI, p.Kind)
	require.Equal(t, "from-config", p.AI.Answer)

	// An empty config value falls back to OPENAI_BASE_URL through the
	// getenv seam.
	b, rb := newTestApp(t, nil, baseOpts())
	b.plat.getenv = envGetenv
	b.Startup(context.Background())
	b.FetchAIPreview("q env", 1)
	p = lastPreviewPayload(t, rb, 1)
	require.Equal(t, preview.KindAI, p.Kind)
	require.Equal(t, "from-env", p.AI.Answer)
}

func TestStartPreviewPassesKagiBaseURL(t *testing.T) {
	// The Kagi base is config-only (no environment fallback); the
	// configured value reaches the real client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"search":[{"url":"https://hit.example","title":"Hit","snippet":"s"}]}}`))
	}))
	defer srv.Close()

	opt := previewTestOptions()
	opt.Preview.Kagi.APIKey = "k"
	opt.Preview.Kagi.BaseURL = srv.URL
	a, r := newTestApp(t, nil, opt)
	a.Startup(context.Background())
	a.FetchWebPreview("some query", 1)
	p := lastPreviewPayload(t, r, 1)
	require.Equal(t, preview.KindWeb, p.Kind)
	require.Len(t, p.Web.Results, 1)
	require.Equal(t, "https://hit.example", p.Web.Results[0].URL)
}

func TestStartPreviewInvalidBaseURLKeepsTerseFetchError(t *testing.T) {
	opt := previewTestOptions()
	opt.Preview.Kagi.APIKey = "k"
	opt.Preview.Kagi.BaseURL = "kagi.example" // no scheme
	opt.Preview.OpenAI.APIKey = "o"
	opt.Preview.OpenAI.BaseURL = "gopher://answers.example"
	a, r := newTestApp(t, nil, opt)
	a.Startup(context.Background())
	d := a.previewDispatcher()
	require.NotNil(t, d)
	require.False(t, d.WebConfigured(), "an invalid base never installs a client")
	require.False(t, d.AIConfigured(), "an invalid base never installs a client")

	// The keys are present, so the frontend buttons stay enabled --
	// clicking explains the actual problem, naming the knob but never
	// the value.
	info := a.GetPreviewConfig()
	require.True(t, info.KagiConfigured)
	require.True(t, info.OpenAIConfigured)

	a.FetchWebPreview("q", 1)
	events := r.emitted(eventPreviewResult)
	require.Len(t, events, 1, "the invalid-base answer is synchronous")
	p := events[0].payload[0].(preview.Payload)
	require.Equal(t, preview.KindError, p.Kind)
	require.Equal(t, "kagi: invalid baseUrl (preview.kagi.baseUrl)", p.Err)
	require.NotContains(t, p.Err, "kagi.example")

	a.FetchAIPreview("q", 2)
	events = r.emitted(eventPreviewResult)
	require.Len(t, events, 2)
	p = events[1].payload[0].(preview.Payload)
	require.Equal(t, preview.KindError, p.Kind)
	require.Equal(t, "openai: invalid baseUrl (preview.openai.baseUrl / OPENAI_BASE_URL)", p.Err)
	require.NotContains(t, p.Err, "answers.example")
}
