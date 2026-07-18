package app

import (
	"context"
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/sysstats"
)

// eventStatsUpdate carries one published system-stats sample; payload
// sysstats.Snapshot (json tags cpuPct/cpuOk/gpuPct/gpuOk/memUsed/
// memTotal/memOk/swapUsed/swapTotal/swapOk/netRxBps/netTxBps/netOk).
const eventStatsUpdate = "stats:update"

// statsSource is the slice of *sysstats.Sampler the App consumes,
// split out so tests can inject recording fakes (the real Start spawns
// the sampling goroutines).
type statsSource interface {
	Start(ctx context.Context)
	SetVisible(v bool)
	Snapshot() sysstats.Snapshot
}

// startStats brings the system-stats sampler up once, at Startup: the
// newStats seam yields the sampler (nil = disabled in config, or a
// test), and Start runs its goroutines under a dedicated context
// cancelled in Shutdown. The sampler is idle until the bar first
// becomes visible, so starting it here costs nothing.
func (a *App) startStats() {
	if a.newStats == nil {
		return
	}
	st := a.newStats()
	if st == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.stats = st
	a.statsCancel = cancel
	a.mu.Unlock()
	st.Start(ctx)
}

// buildStats is the production value behind the newStats seam: a fresh
// config read (the standalone-read pattern translucent.go uses; the
// App deliberately carries no Config), the stats.disabled kill switch,
// and a sampler whose published samples are relayed to the frontend
// as eventStatsUpdate. sysstats.New logs the one-line source summary
// itself.
func (a *App) buildStats() statsSource {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("stats: config: %v (continuing with defaults)", err)
	}
	if cfg.Stats.Disabled {
		log.Printf("stats: disabled in config")
		return nil
	}
	return sysstats.New(sysstats.Options{
		OnUpdate: a.emitStats,
		Logf:     log.Printf,
	})
}

// emitStats relays one published stats sample to the frontend. It runs
// on the sampler goroutine and is guarded like every other emit, so a
// pre-Startup call no-ops.
func (a *App) emitStats(snap sysstats.Snapshot) {
	a.emitEvent(eventStatsUpdate, snap)
}

// statsVisible forwards bar visibility to the sampler -- a flag flip
// plus (on show) a non-blocking kick inside the sampler, never IO.
// Nil-safe: stats disabled, not started yet, or already shut down all
// no-op.
func (a *App) statsVisible(v bool) {
	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	if st != nil {
		st.SetVisible(v)
	}
}

// GetStats returns the sampler's cached snapshot for the frontend:
// instant, a mutex-guarded copy, never any IO on this path. With the
// sampler disabled or not started it is the zero Snapshot -- every OK
// flag false, which the frontend renders as placeholders.
func (a *App) GetStats() sysstats.Snapshot {
	a.mu.Lock()
	st := a.stats
	a.mu.Unlock()
	if st == nil {
		return sysstats.Snapshot{}
	}
	return st.Snapshot()
}
