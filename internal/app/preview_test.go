package app

import (
	"context"
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

func TestFetchPreviewStubsEmitErrorPayloads(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())

	a.FetchWebPreview("some query", 3)
	events := r.emitted(eventPreviewResult)
	require.Len(t, events, 1)
	p := events[0].payload[0].(preview.Payload)
	require.Equal(t, 3, p.Gen)
	require.Equal(t, preview.KindError, p.Kind)
	require.Contains(t, p.Err, "web search lands later")

	a.FetchAIPreview("some query", 4)
	events = r.emitted(eventPreviewResult)
	require.Len(t, events, 2)
	p = events[1].payload[0].(preview.Payload)
	require.Equal(t, 4, p.Gen)
	require.Equal(t, preview.KindError, p.Kind)
	require.Contains(t, p.Err, "AI answers land later")
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
	require.Equal(t, WindowWidth, a.winW)
	require.Equal(t, WindowHeight, a.winH)

	b, _ := newTestApp(t, nil, Options{WindowW: 1600, WindowH: 800})
	require.Equal(t, 1600, b.winW)
	require.Equal(t, 800, b.winH)

	// A partial override (one dimension) keeps the safe defaults.
	c, _ := newTestApp(t, nil, Options{WindowW: 1600})
	require.Equal(t, WindowWidth, c.winW)
	require.Equal(t, WindowHeight, c.winH)
}

func TestShowPositionsWithConfiguredWindowSize(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowW: 1600, WindowH: 800})
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
