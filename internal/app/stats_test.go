package app

import (
	"bytes"
	"context"
	"log"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/sysstats"
)

// fakeStatsSource records the App's stats lifecycle calls.
type fakeStatsSource struct {
	mu       sync.Mutex
	startCtx context.Context
	starts   int
	visible  []bool
	snap     sysstats.Snapshot
}

func (f *fakeStatsSource) Start(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCtx = ctx
	f.starts++
}

func (f *fakeStatsSource) SetVisible(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visible = append(f.visible, v)
}

func (f *fakeStatsSource) Snapshot() sysstats.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeStatsSource) state() (starts int, ctx context.Context, visible []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.startCtx, append([]bool(nil), f.visible...)
}

func TestGetStatsWithoutSampler(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // newTestApp nils the seam
	require.Equal(t, sysstats.Snapshot{}, a.GetStats(), "zero snapshot before Startup")
	a.Startup(context.Background())
	require.Equal(t, sysstats.Snapshot{}, a.GetStats(), "zero snapshot with no sampler")
}

func TestGetStatsReadsSampler(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	fake := &fakeStatsSource{snap: sysstats.Snapshot{CPUPct: 12.5, CPUOK: true, MemTotal: 42, MemOK: true}}
	a.newStats = func() statsSource { return fake }
	a.Startup(context.Background())

	starts, ctx, _ := fake.state()
	require.Equal(t, 1, starts, "Start runs once")
	require.NotNil(t, ctx)
	require.NoError(t, ctx.Err(), "the stats context is live after Startup")
	require.Equal(t, fake.snap, a.GetStats())

	// A second Startup (context refresh) must not start a second
	// sampler.
	a.Startup(context.Background())
	starts, _, _ = fake.state()
	require.Equal(t, 1, starts)
}

func TestShowAndHideDriveStatsVisibility(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	fake := &fakeStatsSource{}
	a.newStats = func() statsSource { return fake }
	a.Startup(context.Background())
	a.DomReady(context.Background())

	a.toggle() // hidden -> summon (cursorOK false: centers, still shows)
	_, _, vis := fake.state()
	require.Equal(t, []bool{true}, vis, "the summon path wakes the sampler")

	a.Hide()
	_, _, vis = fake.state()
	require.Equal(t, []bool{true, false}, vis, "hiding idles the sampler")

	a.showIfHidden() // the IPC show path funnels through the same helper
	_, _, vis = fake.state()
	require.Equal(t, []bool{true, false, true}, vis)
}

func TestDomReadyDeferredShowDrivesStatsVisibility(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{ShowOnStartup: true})
	fake := &fakeStatsSource{}
	a.newStats = func() statsSource { return fake }
	a.Startup(context.Background())
	_, _, vis := fake.state()
	require.Empty(t, vis, "nothing shows before DomReady")

	a.DomReady(context.Background())
	_, _, vis = fake.state()
	require.Equal(t, []bool{true}, vis, "the deferred show wakes the sampler")
}

func TestStartupHonorsStatsDisabledConfig(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	dir := os.Getenv(config.EnvConfigDir) // newTestApp pointed this at a temp dir
	writeConfigJSON(t, dir, `{"stats":{"disabled":true}}`)
	a.newStats = a.buildStats // the real builder
	a.Startup(context.Background())

	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	require.Nil(t, st, "a disabled config must not build a sampler")
	require.Contains(t, buf.String(), "stats: disabled in config")
	require.Equal(t, sysstats.Snapshot{}, a.GetStats())
	a.Hide() // the nil-safe visibility path must not panic
}

func TestStartupBuildsRealSamplerWhenEnabled(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.newStats = a.buildStats // default config: enabled
	a.Startup(context.Background())

	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	require.NotNil(t, st, "the default config enables the sampler")
	// The sampler idles until the first show, so nothing samples here;
	// Shutdown (t.Cleanup) cancels its goroutines.
}

func TestEmitStats(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	snap := sysstats.Snapshot{CPUPct: 55, CPUOK: true, NetRxBps: 1000, NetOK: true}

	a.emitStats(snap) // pre-Startup: guarded no-op
	require.Empty(t, r.emitted(eventStatsUpdate))

	a.Startup(context.Background())
	a.emitStats(snap)
	evs := r.emitted(eventStatsUpdate)
	require.Len(t, evs, 1)
	require.Equal(t, []interface{}{snap}, evs[0].payload, "the event payload is the snapshot")
}

func TestShutdownCancelsStats(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	fake := &fakeStatsSource{}
	a.newStats = func() statsSource { return fake }
	a.Startup(context.Background())
	_, ctx, _ := fake.state()
	require.NoError(t, ctx.Err())

	a.Shutdown(context.Background())
	require.ErrorIs(t, ctx.Err(), context.Canceled, "Shutdown cancels the sampler's context")
	require.Equal(t, sysstats.Snapshot{}, a.GetStats(), "the sampler is detached after Shutdown")

	// Visibility flips after Shutdown are nil-safe no-ops.
	_, _, before := fake.state()
	a.Hide()
	_, _, after := fake.state()
	require.Equal(t, before, after)
}
