package app

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/wow-look-at-my/competent-search-thing/internal/launch"
)

// Credentialed-launch tuning.
const (
	// launchMintTimeout bounds the GTK-thread dispatch that mints a
	// credential; the bar is visible and its main loop idle, so the
	// idle callback normally runs within a frame.
	launchMintTimeout = 500 * time.Millisecond
	// launchDBusTimeout bounds one org.freedesktop.Application
	// activation call (which may cold-start the service via D-Bus
	// activation).
	launchDBusTimeout = 2 * time.Second
)

// launchEnabled reports whether the credentialed launch path applies:
// linux only -- macOS `open` and Windows ShellExecute activate the
// target natively -- and only with the resolve seam wired.
func (a *App) launchEnabled() bool {
	return a.plat.goos == "linux" && a.plat.resolveHandler != nil
}

// announceLaunch runs once at Startup (linux GUI only): it kicks the
// one-time native launch setup (the Wayland input-serial listener)
// and logs that launches carry activation credentials from here on.
func (a *App) announceLaunch() {
	if a.plat.goos != "linux" {
		return
	}
	if a.plat.prepareLaunch != nil {
		a.plat.prepareLaunch()
	}
	log.Printf("launch: activation credentials enabled (session=%s)", a.session().Kind)
}

// launchWatchCtx returns the app-lifetime context bounding every
// raise-watcher goroutine, creating it on first use. Shutdown cancels
// it and leaves it cancelled, so a post-shutdown launch spawns a
// watcher that exits immediately.
func (a *App) launchWatchCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.launchCtx == nil {
		a.launchCtx, a.launchCancel = context.WithCancel(context.Background())
	}
	return a.launchCtx
}

// openTarget opens one target (file path or URL) through the
// credentialed launch path: resolve the handler, mint a credential
// while the bar still holds focus, dispatch through the transport
// cascade, then arm the raise watcher. Off linux it is exactly the
// old launcher call.
func (a *App) openTarget(target string) error {
	if strings.TrimSpace(target) == "" || !a.launchEnabled() {
		// Blank targets go straight to the launcher's own validation:
		// no point resolving or minting for garbage.
		return a.plat.open(target, nil)
	}
	t := launch.ClassifyTarget(target, false)
	if !t.IsURL && a.targetIsDir(target) {
		t.IsDir = true
	}
	h, resolved := a.plat.resolveHandler(t)
	cred := a.mintFor(h, resolved)
	before, watchable := a.watcherBefore()
	transport, pid, err := a.dispatchOpen(t, h, resolved, cred)
	if err != nil {
		a.endStartupSequence(cred) // nothing launched; reap the sequence now
		return err
	}
	on := a.armRaiseWatcher(watchable, before, pid, cred, h, "", "open", target)
	log.Print(launch.LogLine("open", target, h, resolved, cred, transport, on))
	return nil
}

// dispatchOpen runs the open transport cascade: D-Bus activation for
// DBusActivatable handlers (what raises an existing GApplication
// window on Wayland), the handler's own Exec line with the credential
// in its environment, then the xdg-open candidate table (also
// credentialed). Each step down is a transport change only -- the
// same target opens either way -- and is logged. It returns the
// transport used and the spawned child's pid (0 when there is none).
func (a *App) dispatchOpen(t launch.Target, h launch.Handler, resolved bool, cred launch.Credential) (string, int, error) {
	env := launch.CredentialEnv(cred)
	if resolved {
		if call, ok := launch.ApplicationDBusCall(h, &t, cred); ok && a.plat.dbusLaunch != nil {
			err := a.plat.dbusLaunch(call)
			if err == nil {
				return launch.TransportDBus, 0, nil
			}
			log.Printf("launch: dbus activation of %s failed: %v (falling back to exec)", call.Dest, err)
		}
		if !h.Terminal && h.Exec != "" && a.plat.launchExec != nil {
			if argv := launch.ExpandExec(h.Exec, t); len(argv) > 0 && argv[0] != "" {
				pid, err := a.plat.launchExec(argv, env)
				if err == nil {
					return launch.TransportExec, pid, nil
				}
				log.Printf("launch: exec of %q failed: %v (falling back to xdg-open)", argv[0], err)
			}
		}
	}
	if err := a.plat.open(t.Raw, env); err != nil {
		return "", 0, err
	}
	return launch.TransportXdgOpen, 0, nil
}

// revealTarget shows path in the file manager through the
// credentialed path. The handler that matters is the file MANAGER --
// the default application for directories -- whose capabilities gate
// the mint; the minted id rides both the ShowItems startup-id
// argument and the fallback child's environment. Off linux it is
// exactly the old launcher call.
func (a *App) revealTarget(path string) error {
	if strings.TrimSpace(path) == "" || !a.launchEnabled() {
		return a.plat.reveal(path, nil, "")
	}
	h, resolved := a.plat.resolveHandler(launch.Target{Raw: path, IsDir: true})
	cred := a.mintFor(h, resolved)
	before, watchable := a.watcherBefore()
	if err := a.plat.reveal(path, launch.CredentialEnv(cred), cred.ID); err != nil {
		a.endStartupSequence(cred)
		return err
	}
	on := a.armRaiseWatcher(watchable, before, 0, cred, h, "", "reveal", path)
	log.Print(launch.LogLine("reveal", path, h, resolved, cred, launch.TransportShowItems, on))
	return nil
}

// runCommandAction executes a run_command action. With a desktop id
// (the builtin app launchers) on linux it takes the credentialed
// path: D-Bus activation for DBusActivatable apps -- what makes
// "launch an app that is already running" focus its window -- else
// the validated argv, spawned with the credential in its environment.
// Without one, byte-identical old behavior (plain detached run).
func (a *App) runCommandAction(argv []string, desktopID string) error {
	if desktopID == "" || !a.launchEnabled() || a.plat.handlerByID == nil {
		return a.plat.run(argv, nil)
	}
	h, resolved := a.plat.handlerByID(desktopID)
	cred := a.mintFor(h, resolved)
	before, watchable := a.watcherBefore()
	transport := launch.TransportExec
	dispatched := false
	if resolved {
		if call, ok := launch.ApplicationDBusCall(h, nil, cred); ok && a.plat.dbusLaunch != nil {
			err := a.plat.dbusLaunch(call)
			if err == nil {
				transport, dispatched = launch.TransportDBus, true
			} else {
				log.Printf("launch: dbus activation of %s failed: %v (falling back to exec)", call.Dest, err)
			}
		}
	}
	if !dispatched {
		if err := a.plat.run(argv, launch.CredentialEnv(cred)); err != nil {
			a.endStartupSequence(cred)
			return err
		}
	}
	on := a.armRaiseWatcher(watchable, before, 0, cred, h, argv[0], "run", argv[0])
	log.Print(launch.LogLine("run", argv[0], h, resolved, cred, transport, on))
	return nil
}

// mintFor mints a credential when the policy says the handler can use
// one; otherwise (or without the seam) the none-credential.
func (a *App) mintFor(h launch.Handler, resolved bool) launch.Credential {
	if !launch.ShouldMint(h, resolved) || a.plat.mintCredential == nil {
		return launch.Credential{Kind: launch.KindNone}
	}
	return a.plat.mintCredential()
}

// targetIsDir stats a path target through the lstat seam; symlinks
// resolve as non-directories, matching how the index stores them.
func (a *App) targetIsDir(target string) bool {
	if a.plat.lstat == nil {
		return false
	}
	fi, err := a.plat.lstat(target)
	return err == nil && fi.IsDir()
}

// watcherBefore snapshots the pre-launch window ids when the raise
// watcher can run at all: an X display must be reachable (X11
// sessions and XWayland on Wayland sessions alike -- the DISPLAY env
// gate) and the snapshot read must work. It runs BEFORE the dispatch
// so the launched window is provably new.
func (a *App) watcherBefore() (map[uint32]bool, bool) {
	if a.plat.watchState == nil || a.plat.getenv("DISPLAY") == "" {
		return nil, false
	}
	st, ok := a.plat.watchState()
	if !ok {
		return nil, false
	}
	before := make(map[uint32]bool, len(st.Windows))
	for _, w := range st.Windows {
		before[w.ID] = true
	}
	return before, true
}

// armRaiseWatcher starts the post-launch raise watcher goroutine
// (bounded by the app-lifetime launch context) and reports whether it
// is on. When the watcher cannot run, an x11-sn startup sequence is
// still reaped after the same deadline the watcher would have used.
func (a *App) armRaiseWatcher(watchable bool, before map[uint32]bool, pid int, cred launch.Credential, h launch.Handler, argv0, verb, target string) bool {
	if !watchable {
		a.endStartupSequenceLater(cred)
		return false
	}
	cfg := launch.WatchConfig{
		Identity: launch.NewIdentity(pid, cred, h, argv0),
		Before:   before,
		Poll:     a.plat.watchState,
		Activate: a.plat.activateWindow,
		Logf:     log.Printf,
		Verb:     verb,
		Target:   target,
	}
	ctx := a.launchWatchCtx()
	go func() {
		launch.RunWatcher(ctx, cfg)
		a.endStartupSequence(cred)
	}()
	return true
}

// endStartupSequence broadcasts the startup-notification "remove:"
// message reaping an X11 startup sequence the launchee may never
// complete itself (chromium-family apps never do; the sequence would
// otherwise keep GNOME's busy cursor until mutter's 15s timeout). A
// launchee that already completed it makes this a harmless no-op --
// the WM ignores unknown ids. Only x11-sn credentials have an X
// sequence to reap; Wayland sequences have no remove API and time out
// server-side exactly like GNOME Shell's own app-grid launches for
// non-notifying apps.
func (a *App) endStartupSequence(cred launch.Credential) {
	if cred.Kind != launch.KindX11SN || cred.ID == "" || a.plat.snRemove == nil {
		return
	}
	if err := a.plat.snRemove(cred.ID); err != nil {
		log.Printf("launch: ending startup sequence: %v", err)
	}
}

// endStartupSequenceLater reaps an x11-sn sequence after the watcher
// deadline when no watcher runs, giving the launchee the same
// completion window it would have had.
func (a *App) endStartupSequenceLater(cred launch.Credential) {
	if cred.Kind != launch.KindX11SN || cred.ID == "" || a.plat.snRemove == nil {
		return
	}
	ctx := a.launchWatchCtx()
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(launch.DefaultWatchDeadline):
			a.endStartupSequence(cred)
		}
	}()
}
