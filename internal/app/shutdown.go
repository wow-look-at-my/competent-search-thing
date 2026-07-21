package app

// The teardown half of the App lifecycle, moved verbatim from app.go
// for the file-length cap (the search.go precedent). The construction
// half (New / Startup / DomReady / buildIndex) stays in app.go.

import (
	"context"
	"log"
	"os"
)

// Shutdown is wired to the Wails OnShutdown hook. It closes the
// single-instance IPC server first (no new summons during teardown;
// closing also unlinks the socket) and the companion-extension bridge
// beside it (the other owned listener), releases the global hotkey
// (stopping the native listener, aborting a still-running
// portal/gsettings backend chain, and closing an active portal
// shortcut), closes the tray icon (aborting a Start still waiting on
// the bus; closing the tray's connection unregisters the icon),
// cancels the system-stats sampler's goroutines,
// cancels the in-flight plugin generation, closes the registry, and
// cancels the firefox context (aborting a frequent-sites history
// refresh mid-copy), cancels the preview dispatcher's parent context
// (aborting an in-flight preview request; see preview.go), cancels a
// still-running initial build (its walk aborts
// and logs "index: initial build cancelled"), and stops the rescanner
// first (it may be mid-rescan and calls back into the watcher to
// resync watches), then the sweeper (its passes reconcile through the
// watcher too, so it must stop before it), then the watcher, then the
// theme hot-reload watcher. Every step is bounded: an in-flight
// rescan, sweep pass, or watch resync is cancelled, never waited out,
// so quit stays fast even mid-walk on a huge index. Safe to call at
// any point, even before the watch layer came up; the shuttingDown
// flag keeps a racing startWatch from starting it afterwards. The
// very last step clears the TTY progress line and restores the
// standard logger to stderr (non-TTY printers never touched it).
func (a *App) Shutdown(_ context.Context) {
	if a.opt.IPC != nil {
		if err := a.opt.IPC.Close(); err != nil {
			log.Printf("ipc: close: %v", err)
		}
	}

	// The companion-extension bridge is the other listener this
	// process owns; close it beside the IPC server (unlinks its
	// socket; the host relay simply retries until the next launch).
	a.shutdownFfext()

	a.mu.Lock()
	th := a.trayH
	a.trayH = nil
	trayCancel := a.trayCancel
	a.trayCancel = nil
	statsCancel := a.statsCancel
	a.statsCancel = nil
	a.stats = nil
	svcCancel := a.svcCancel
	a.svcCancel = nil
	launchCancel := a.launchCancel
	a.launchCancel = nil
	if launchCancel == nil && a.launchCtx == nil {
		// Nothing was ever launched: park a pre-cancelled context so a
		// post-shutdown launch cannot arm a watcher that outlives us.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		a.launchCtx = ctx
	}
	a.mu.Unlock()
	if launchCancel != nil {
		launchCancel()
	}
	// The hotkey teardown (native stop, chain cancel, portal close)
	// is shared with the config live-apply's re-registration path.
	a.teardownHotkey()
	if trayCancel != nil {
		trayCancel()
	}
	if th != nil {
		if err := th.Close(); err != nil {
			log.Printf("tray: close: %v", err)
		}
	}
	if statsCancel != nil {
		statsCancel()
	}
	// An in-flight login-service registration aborts its bounded
	// service-manager execs (see service.go in this package).
	if svcCancel != nil {
		svcCancel()
	}

	a.pluginMu.Lock()
	cancel := a.pluginCancel
	a.pluginCancel = nil
	reg := a.registry
	a.registry = nil
	ffCancel := a.firefoxCancel
	a.firefoxCancel = nil
	a.pluginMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if reg != nil {
		reg.Close()
	}
	if ffCancel != nil {
		ffCancel()
	}

	a.shutdownPreview()

	a.watchMu.Lock()
	a.shuttingDown = true
	buildCancel := a.buildCancel
	a.buildCancel = nil
	w, r, sw, tw := a.watcher, a.rescanner, a.sweeper, a.themeW
	a.watcher, a.rescanner, a.sweeper, a.themeW = nil, nil, nil, nil
	ew := a.earlyWatcher
	a.earlyWatcher = nil
	a.watchMu.Unlock()
	if buildCancel != nil {
		buildCancel()
	}
	if r != nil {
		r.Stop()
	}
	if sw != nil {
		sw.Stop()
	}
	if w != nil {
		w.Stop()
	}
	if ew != nil {
		// A pre-build watcher whose build is still walking (the cancel
		// above aborts it); stopping here is idempotent with the
		// build goroutine's own cleanup.
		ew.Stop()
	}
	if tw != nil {
		tw.stop()
	}

	// Drain the frecency layer's short-lived goroutines (one state
	// load, in-flight open recordings, a cwd derivation) so an open
	// recorded moments before quit still hits the disk. Each is a
	// single bounded file operation or /proc walk -- no lock is held
	// here and none of them can block indefinitely.
	a.frecWG.Wait()

	// Same for the priors layer's table rebuilds (bounded local file
	// reads); the closed flag inside keeps a finishing rebuild from
	// re-arming behind the drain.
	a.shutdownPriors()

	// Same for the telemetry appends (telemetry.go): each is one
	// bounded file append; a pick recorded moments before quit still
	// lands in the log.
	a.telWG.Wait()

	// And for the arbiter's training runs (arbiter.go; bounded local
	// file reads plus in-memory SGD): the closed flag inside keeps a
	// finishing run from re-arming behind the drain. After telWG, so
	// a retrain kicked by the very last pick append is drained too.
	a.shutdownArbiter()

	// Restore the standard logger LAST: in TTY mode installProgressLog
	// pointed it at the printer, and keeping that interception through
	// the teardown above let every log line up to here interleave
	// cleanly with a still-rendered progress row. Done clears any
	// in-place line first, so nothing written after us collides with a
	// leftover row. Non-TTY printers never touched the logger and are
	// left alone (unit tests capture log output; Shutdown must not
	// clobber their buffers).
	a.mu.Lock()
	pr := a.progress
	a.mu.Unlock()
	if pr != nil && pr.TTY() {
		pr.Done()
		log.SetOutput(os.Stderr)
	}
}
