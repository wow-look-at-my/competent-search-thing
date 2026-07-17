package app

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/portal"
)

// EnvHotkeyBackend overrides the automatic hotkey backend selection.
// Recognized values: "auto" (or empty; the default), "x11" (the native
// startHotkey path: XGrabKey on linux, RegisterHotKey on windows,
// CGEventTap on darwin), "portal" (XDG Desktop Portal
// GlobalShortcuts), "gsettings" (GNOME custom keybinding running the
// CLI), "none" (no global hotkey; the IPC toggle/show/hide commands
// keep working). Anything else warns once and behaves like auto.
const EnvHotkeyBackend = "COMPETENT_SEARCH_HOTKEY_BACKEND"

// The portal shortcut identity. The id must stay stable across runs:
// the portal keys remembered user approvals on it.
const (
	portalShortcutID   = "toggle"
	portalShortcutDesc = "Summon the searchbar"
)

// hotkeyBackend is one way of obtaining a global summon hotkey.
type hotkeyBackend int

const (
	// backendX11 is the native startHotkey seam -- today's behavior on
	// X11 sessions (and the only path on windows/darwin, whose
	// sessions detect as unknown).
	backendX11 hotkeyBackend = iota
	// backendPortal is the XDG Desktop Portal GlobalShortcuts client.
	backendPortal
	// backendGsettings installs a GNOME custom keybinding that runs
	// "<exe> toggle" (the IPC summon path).
	backendGsettings
	// backendManual has nothing to register: it logs how to bind a key
	// manually and stops the chain.
	backendManual
)

// String names the backend like the override env values do.
func (b hotkeyBackend) String() string {
	switch b {
	case backendX11:
		return "x11"
	case backendPortal:
		return "portal"
	case backendGsettings:
		return "gsettings"
	default:
		return "manual"
	}
}

// hotkeyPlan decides which backends registerHotkey tries, in order.
// An explicit override selects exactly that backend ("none" selects
// nothing); unknown override values report unknownOverride and fall
// through to the automatic choice: X11 sessions (and unknown ones --
// headless CI, windows, darwin) keep today's native path, Wayland
// GNOME tries the portal then the gsettings keybinding, and other
// Wayland desktops try the portal then give manual instructions.
func hotkeyPlan(sess platform.Session, override string) (plan []hotkeyBackend, unknownOverride bool) {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "", "auto":
	case "x11":
		return []hotkeyBackend{backendX11}, false
	case "portal":
		return []hotkeyBackend{backendPortal}, false
	case "gsettings":
		return []hotkeyBackend{backendGsettings}, false
	case "none":
		return nil, false
	default:
		unknownOverride = true
	}
	if sess.Kind == platform.SessionWayland {
		if sess.IsGNOME() {
			return []hotkeyBackend{backendPortal, backendGsettings}, unknownOverride
		}
		return []hotkeyBackend{backendPortal, backendManual}, unknownOverride
	}
	return []hotkeyBackend{backendX11}, unknownOverride
}

// registerHotkey parses the configured hotkey and brings up the first
// working backend of the session's plan. Any failure -- bad spec, no X
// server, no portal, every key combination taken -- is logged once and
// the app runs on without a global hotkey (the IPC summon commands
// always keep working). The native path registers synchronously,
// exactly as it always has; the portal/gsettings chain runs on one
// goroutine because the portal may block on an interactive approval
// dialog, and is aborted by Shutdown.
func (a *App) registerHotkey() {
	spec := strings.TrimSpace(a.opt.Hotkey)
	if spec == "" {
		return
	}
	hk, err := platform.ParseHotkey(spec)
	if err != nil {
		log.Printf("hotkey: %v (running without a global hotkey)", err)
		return
	}
	override := ""
	if a.plat.getenv != nil {
		override = a.plat.getenv(EnvHotkeyBackend)
	}
	plan, unknownOverride := hotkeyPlan(a.session(), override)
	if unknownOverride {
		log.Printf("hotkey: unknown %s value %q (using automatic backend selection)", EnvHotkeyBackend, override)
	}
	if len(plan) == 0 {
		log.Printf("hotkey: global hotkey disabled by %s=none ('competent-search-thing toggle' still summons the bar)", EnvHotkeyBackend)
		return
	}
	if plan[0] == backendX11 {
		a.startNativeHotkey(hk)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.hotkeyCancel = cancel
	a.mu.Unlock()
	go a.runHotkeyPlan(ctx, plan, hk)
}

// startNativeHotkey is the pre-Wayland registration path,
// byte-identical in behavior: start the OS listener with toggle as the
// callback, log the outcome, keep the stop func for Shutdown.
func (a *App) startNativeHotkey(hk platform.Hotkey) {
	if a.plat.startHotkey == nil {
		return
	}
	stop, err := a.plat.startHotkey(hk, a.toggle)
	if err != nil {
		log.Printf("hotkey: registering %s failed: %v (running without a global hotkey)", hk, err)
		return
	}
	log.Printf("hotkey: %s summons the searchbar", hk)
	a.mu.Lock()
	a.hotkeyStop = stop
	a.hotkeyDesc = hk.String()
	a.mu.Unlock()
}

// runHotkeyPlan tries the remaining backends in order until one
// succeeds or declares the chain finished. It only ever receives
// portal/gsettings/manual entries: an x11-first plan is handled
// synchronously by registerHotkey.
func (a *App) runHotkeyPlan(ctx context.Context, plan []hotkeyBackend, hk platform.Hotkey) {
	for _, b := range plan {
		if ctx.Err() != nil {
			return // shutting down
		}
		done := false
		switch b {
		case backendPortal:
			done = a.startPortalHotkey(ctx, hk)
		case backendGsettings:
			done = a.startGnomeBinding(ctx, hk)
		case backendManual:
			done = true
			a.logManualHotkey()
		}
		if done {
			return
		}
	}
	a.logManualHotkey()
}

// startPortalHotkey tries the portal backend; true means the chain is
// finished (success, the user declined, or shutdown), false means the
// next backend should run.
func (a *App) startPortalHotkey(ctx context.Context, hk platform.Hotkey) bool {
	if a.plat.startPortal == nil {
		return false
	}
	h, err := a.plat.startPortal(ctx, hk, a.toggle)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return true // shutdown aborted the registration
		case errors.Is(err, portal.ErrDenied):
			log.Printf("hotkey: the portal global shortcut was declined; bind a key to 'competent-search-thing toggle' yourself (see README, Wayland section)")
			return true
		case errors.Is(err, portal.ErrNoPortal), errors.Is(err, portal.ErrNoGlobalShortcuts):
			log.Printf("hotkey: %v", err)
			return false
		default:
			log.Printf("hotkey: portal global shortcut failed: %v", err)
			return false
		}
	}
	desc := h.BoundDesc()
	if desc == "" {
		if t, terr := portal.TriggerString(hk); terr == nil {
			desc = t
		} else {
			desc = hk.String()
		}
	}
	a.mu.Lock()
	if a.hotkeyCancel == nil {
		// Shutdown already ran and cannot see this handle any more.
		a.mu.Unlock()
		_ = h.Close()
		return true
	}
	a.portalHK = h
	a.hotkeyDesc = desc
	a.mu.Unlock()
	log.Printf("hotkey: portal global shortcut active (%s)", desc)
	return true
}

// startGnomeBinding tries the GNOME custom-keybinding backend; true
// means the chain is finished. The one loud summary line here is what
// a GNOME-Wayland user reads to learn their effective summon key --
// and it is only the confident "active" wording when the read-back
// verified the entry on disk AND gsd-media-keys (the process that
// turns the entry into a compositor grab) is reachable; a gsettings
// write returning success proves neither, and claiming a hotkey that
// cannot fire is worse than saying so.
func (a *App) startGnomeBinding(ctx context.Context, hk platform.Hotkey) bool {
	if a.plat.ensureGnomeBinding == nil || a.plat.executable == nil {
		return false
	}
	exe, err := a.plat.executable()
	if err != nil {
		log.Printf("hotkey: locating the executable for the GNOME keybinding: %v", err)
		return false
	}
	// The keybinding runs outside the app's environment (gsd spawns it
	// with GLib's shell parsing and its own PATH), so the command must
	// name the binary absolutely -- never rely on a relative argv[0].
	if exe == "" {
		log.Printf("hotkey: the executable path is empty; cannot write a GNOME keybinding command")
		return false
	}
	if !filepath.IsAbs(exe) {
		abs, aerr := filepath.Abs(exe)
		if aerr != nil {
			log.Printf("hotkey: cannot resolve executable path %q for the GNOME keybinding: %v", exe, aerr)
			return false
		}
		exe = abs
	}
	// Prefer a stable spelling of that path: os.Executable is fully
	// resolved, so under a symlinked install layout (Homebrew's
	// versioned Cellar, Nix, stow) it names a directory the next
	// upgrade deletes, killing the keybinding. The PATH shim -- or the
	// symlink argv[0] was launched through -- keeps pointing at the
	// current version; StableExecutable only ever substitutes a path
	// proven (os.SameFile) to be this very binary.
	args0 := ""
	if a.plat.args0 != nil {
		args0 = a.plat.args0()
	}
	if stable := platform.StableExecutable(exe, args0); stable != exe {
		log.Printf("hotkey: using stable executable path %q for the GNOME keybinding (running binary %q)", stable, exe)
		exe = stable
	}
	command := gsettings.ToggleCommand(exe)
	applied, err := a.plat.ensureGnomeBinding(ctx, hk, command)
	if err != nil {
		if ctx.Err() != nil {
			return true
		}
		log.Printf("hotkey: installing a GNOME keybinding failed: %v", err)
		return false
	}

	if applied.Repaired {
		// The one loud repair line: an entry written by an older run
		// pointed at an executable that is gone or no longer this
		// binary (a versioned install dir after an upgrade), and only
		// its command was rewritten -- the accelerator is untouched.
		log.Printf("hotkey: repaired the GNOME keybinding command: %q -> %q (the stored command no longer launched this binary)",
			applied.PreviousCommand, command)
	}

	// The evidence line: exactly what the read-back found on disk.
	log.Printf("hotkey: GNOME keybinding entry %s: binding %q, command %q, in custom-keybindings list: %v",
		gsettings.OurPath, applied.DiskBinding, applied.DiskCommand, applied.InList)

	if !applied.Verified {
		log.Printf("hotkey: WARNING: the GNOME keybinding could not be verified (%s) -- the summon key is probably NOT active; bind a key to '%s' in GNOME Settings > Keyboard, or see the README's GNOME Wayland troubleshooting section", applied.VerifyNote, command)
		return true
	}
	if a.plat.mediaKeysDaemon != nil {
		if running, derr := a.plat.mediaKeysDaemon(ctx); derr == nil && !running {
			// No error + no owner: the session bus is fine but GNOME's
			// media-keys daemon is gone, so nothing will grab the key.
			// (An error means no usable session bus -- headless CI --
			// and the check is skipped without noise.)
			log.Printf("hotkey: WARNING: the keybinding is on disk but GNOME's media-keys daemon (%s) is not running, so %s will not summon anything; bind a key to '%s' manually or restart the session", gsettings.DaemonName, applied.Binding, command)
			return true
		}
	}

	switch {
	case applied.Existing:
		log.Printf("hotkey: using existing GNOME keybinding %s (edit in GNOME Settings > Keyboard)", applied.Binding)
	case applied.FellBack:
		log.Printf("hotkey: GNOME keybinding active: %s (requested %s is taken by GNOME; using fallback)", applied.Binding, applied.Requested)
	default:
		log.Printf("hotkey: GNOME keybinding active: %s", applied.Binding)
	}
	a.mu.Lock()
	a.hotkeyDesc = applied.Binding
	a.mu.Unlock()
	return true
}

// logManualHotkey is the end of a plan that found no working backend.
func (a *App) logManualHotkey() {
	log.Printf("hotkey: no global hotkey backend available on this session; bind a key to 'competent-search-thing toggle' (see README, Wayland section)")
}

// session detects the desktop session once and caches it (it cannot
// change while the process lives).
func (a *App) session() platform.Session {
	a.sessionOnce.Do(func() {
		if a.plat.detectSession != nil {
			a.sessionVal = a.plat.detectSession()
		}
	})
	return a.sessionVal
}

// hotkeyDescription returns a user-readable description of the
// effective summon trigger ("" while none is active): the parsed
// config hotkey on the native path, the portal's bound-trigger
// description, or the GNOME accelerator actually installed.
func (a *App) hotkeyDescription() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hotkeyDesc
}

// portalHandle is the app-facing surface of an active portal global
// shortcut: a description of the bound trigger for logging, and
// teardown.
type portalHandle interface {
	BoundDesc() string
	Close() error
}

// portalShortcut is the production portalHandle: one private
// session-bus connection plus the shortcut session registered on it.
// Closing the connection is the definitive portal-session teardown
// (Session.Close never closes it).
type portalShortcut struct {
	conn *dbus.Conn
	sess *portal.Session
}

func (p *portalShortcut) BoundDesc() string { return p.sess.BoundDescription }

func (p *portalShortcut) Close() error {
	err := p.sess.Close()
	if cerr := p.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// startPortalShortcut is the production startPortal seam: dial a
// private session-bus connection, probe for a usable GlobalShortcuts
// backend, and register the stable "toggle" shortcut. Register may
// block on the portal's interactive approval dialog for minutes; ctx
// (cancelled at Shutdown) aborts it.
func startPortalShortcut(ctx context.Context, hk platform.Hotkey, onActivated func()) (portalHandle, error) {
	conn, err := portal.Dial()
	if err != nil {
		return nil, err
	}
	if _, err := portal.Available(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	trigger, err := portal.TriggerString(hk)
	if err != nil {
		trigger = "" // optional: the portal/user picks a trigger instead
	}
	sess, err := portal.Register(ctx, conn, portal.Options{
		ShortcutID:       portalShortcutID,
		Description:      portalShortcutDesc,
		PreferredTrigger: trigger,
		OnActivated:      onActivated,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &portalShortcut{conn: conn, sess: sess}, nil
}
