package app

// The in-app retry for the automatic optimal-watch setup: a user who
// accidentally declined the startup prompt (internal/watchsetup) is
// never stranded in a terminal. The config editor's "Set up
// full-filesystem watching" button calls the bound SetupWatch method,
// which runs the FORCED grant (Attempt: ignores the decline marker and
// the watcher.setupEnabled switch), prompting for privileges (pkexec)
// and running setcap Go-side. Unlike the automatic startup path it does
// NOT re-exec -- the capabilities stick to the binary and take effect at
// the next launch -- so clicking the button never yanks the editor away.

import (
	"context"
	"io"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/watchsetup"
)

// watchSetupRunner is the internal/watchsetup surface SetupWatch needs;
// *watchsetup.Manager satisfies it. A seam so unit tests never probe
// fanotify or spawn pkexec.
type watchSetupRunner interface {
	Attempt(context.Context, io.Writer) watchsetup.Result
}

// buildWatchSetup is the production value behind the newWatchSetup seam:
// a Manager over a fresh standalone config read (the translucent.go
// pattern), so the decline marker path is correct. Attempt is forced, so
// Backend and Enabled do not gate it -- they are passed for completeness.
func (a *App) buildWatchSetup() watchSetupRunner {
	cfg, _ := config.Load()
	dir, _ := config.Dir()
	return watchsetup.New(watchsetup.Config{
		Backend:   cfg.Watcher.Backend,
		Enabled:   config.Enabled(cfg.Watcher.SetupEnabled),
		ConfigDir: dir,
	})
}

// SetupWatch is bound to the frontend (window.go.app.App.SetupWatch):
// the config editor's watch-setup button runs it and shows the returned
// message inline. It blocks while the polkit password dialog is up (the
// bound method runs on its own goroutine, so the UI stays responsive),
// then reports a human-readable outcome. The capabilities apply at the
// next launch. A nil seam (newTestApp) reports "unavailable".
func (a *App) SetupWatch() string {
	if a.newWatchSetup == nil {
		return "Full-filesystem watch setup is unavailable."
	}
	runner := a.newWatchSetup()
	if runner == nil {
		return "Full-filesystem watch setup is unavailable."
	}
	res := runner.Attempt(context.Background(), io.Discard)
	switch res.Action {
	case watchsetup.ActionGranted:
		return "Full-filesystem watching enabled. Restart the app to use it."
	case watchsetup.ActionOptimal:
		return "Full-filesystem watching is already enabled -- nothing to do."
	case watchsetup.ActionAttemptFailed:
		return "Setup did not complete (the request may have been dismissed, or the capability could not be set). You can try again."
	default: // ActionSkipped
		switch res.Reason {
		case "not-linux":
			return "This is a Linux-only feature; macOS uses FSEvents automatically and Windows uses its own watcher."
		case "unsupported":
			return "Full-filesystem watching (fanotify) is not available in this environment (an old kernel or a container); the per-directory fallback is in use."
		default:
			return "Nothing to do."
		}
	}
}
