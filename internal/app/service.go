package app

// The automatic login-service registration hook (the 2026-07-21
// "obviously it must be fully automatic" ruling): every GUI startup
// runs internal/service's Ensure decision matrix on its own goroutine
// -- never blocking first render -- so a plain install (deb, brew,
// raw binary) starts with the desktop from the next login on, with
// zero manual steps. Ensure never starts anything (the app is already
// running, possibly AS the service instance), yields to foreign
// owners (brew services, the deb-shipped unit), respects the
// `service uninstall` opt-out marker, and self-heals a stale binary
// path after upgrades (the gsettings keybinding precedent). The
// COMPETENT_SEARCH_NO_SERVICE env knob plus the natural
// no-user-bus/no-GUI-domain degrade keep CI runners and headless
// sessions clean.

import (
	"context"
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/service"
)

// EnvNoService disables the automatic login-service registration for
// this process when set to any non-empty value (CI runners, headless
// scripting). Deliberately an env knob, not a config.json field: the
// persistent per-user opt-out is `service uninstall`'s marker file.
const EnvNoService = "COMPETENT_SEARCH_NO_SERVICE"

// serviceRegistrar is the slice of *service.Manager the auto-hook
// consumes, split out so tests inject recording fakes (the trayHandle
// pattern; the real Ensure stats the disk and execs
// launchctl/systemctl).
type serviceRegistrar interface {
	Ensure(ctx context.Context) (service.EnsureResult, error)
}

// startService runs the automatic registration once, at Startup:
// env-gated, linux/darwin only, built through the newService seam,
// and asynchronous -- Ensure's worst case is a few 10s-bounded
// service-manager execs, none of which may delay the first render.
func (a *App) startService() {
	if a.plat.getenv(EnvNoService) != "" {
		log.Printf("service: auto-registration disabled (%s is set)", EnvNoService)
		return
	}
	if a.plat.goos != "linux" && a.plat.goos != "darwin" {
		return
	}
	if a.newService == nil {
		return
	}
	reg := a.newService()
	if reg == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.svcCancel = cancel
	a.mu.Unlock()
	go func() {
		res, err := reg.Ensure(ctx)
		logServiceOutcome(res, err)
	}()
}

// buildService is the production value behind the newService seam.
func (a *App) buildService() serviceRegistrar {
	m, err := service.NewManager()
	if err != nil {
		log.Printf("service: %v (skipping login-service registration)", err)
		return nil
	}
	return m
}

// logServiceOutcome turns Ensure's verdict into at most ONE honest
// log line: loud when something changed (registered, repaired), one
// informative line when yielding or degraded, silent when converged.
func logServiceOutcome(res service.EnsureResult, err error) {
	if err != nil {
		log.Printf("service: login-service registration: %v (running on)", err)
		return
	}
	switch res.Action {
	case service.EnsureRegistered:
		log.Printf("service: registered as a login service (%s; starts at your next login) -- disable with 'competent-search-thing service uninstall'", res.ServicePath)
	case service.EnsureRepaired:
		old := res.PreviousExe
		if old == "" {
			old = "(unparsed)"
		}
		msg := "service: repaired the login service command: " + old + " -> " + res.Exe + " (" + res.ServicePath + ")"
		if res.Note != "" {
			msg += " -- " + res.Note
		}
		log.Printf("%s", msg)
	case service.EnsureYielded:
		msg := "service: " + res.Owner + " owns login startup; leaving it alone"
		if res.OursToo {
			msg += " -- our own " + res.ServicePath + " also exists; remove it with 'competent-search-thing service uninstall'"
		}
		if res.Hint != "" {
			msg += " -- " + res.Hint
		}
		log.Printf("%s", msg)
	case service.EnsureOptedOut:
		log.Printf("service: auto-registration is disabled (%s present; 'competent-search-thing service install' re-enables it)", res.Note)
	case service.EnsureUnavailable:
		log.Printf("service: %s; login-service registration skipped", res.Note)
	case service.EnsureCurrent, service.EnsureUnsupported:
		// Converged, or nothing to do on this OS: silence is the
		// polite every-boot answer.
	}
}
