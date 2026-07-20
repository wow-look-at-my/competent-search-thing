package app

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
	"github.com/wow-look-at-my/competent-search-thing/internal/portal"
)

func waylandGNOME() platform.Session {
	return platform.Session{Kind: platform.SessionWayland, Desktop: "ubuntu:GNOME"}
}

func waylandSway() platform.Session {
	return platform.Session{Kind: platform.SessionWayland, Desktop: "sway"}
}

// fakePortalHandle records closes; BoundDesc mimics the portal's
// trigger_description.
type fakePortalHandle struct {
	mu     sync.Mutex
	desc   string
	closed int
}

func (f *fakePortalHandle) BoundDesc() string { return f.desc }

func (f *fakePortalHandle) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakePortalHandle) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// backendEnv builds a getenv stub answering only EnvHotkeyBackend.
func backendEnv(value string) func(string) string {
	return func(key string) string {
		if key == EnvHotkeyBackend {
			return value
		}
		return ""
	}
}

// verifiedApplied fills the read-back fields the way a healthy dconf
// world would: everything on disk matches what was written.
func verifiedApplied(a gsettings.Applied, command string) gsettings.Applied {
	a.InList = true
	a.DiskBinding = a.Binding
	a.DiskCommand = command
	a.Verified = true
	return a
}

func TestHotkeyPlan(t *testing.T) {
	x11 := platform.Session{Kind: platform.SessionX11, Desktop: "ubuntu:GNOME"}
	unknown := platform.Session{}
	cases := []struct {
		name     string
		sess     platform.Session
		override string
		want     []hotkeyBackend
		warn     bool
	}{
		{"x11 empty", x11, "", []hotkeyBackend{backendX11}, false},
		{"x11 auto", x11, "auto", []hotkeyBackend{backendX11}, false},
		{"unknown session (headless, windows, darwin)", unknown, "", []hotkeyBackend{backendX11}, false},
		{"wayland gnome", waylandGNOME(), "", []hotkeyBackend{backendPortal, backendGsettings}, false},
		{"wayland gnome classic", platform.Session{Kind: platform.SessionWayland, Desktop: "GNOME-Classic:GNOME"}, "", []hotkeyBackend{backendPortal, backendGsettings}, false},
		{"wayland non-gnome", waylandSway(), "", []hotkeyBackend{backendPortal, backendManual}, false},
		{"wayland no desktop", platform.Session{Kind: platform.SessionWayland}, "", []hotkeyBackend{backendPortal, backendManual}, false},
		{"override x11 on wayland", waylandGNOME(), "x11", []hotkeyBackend{backendX11}, false},
		{"override portal on x11", x11, "portal", []hotkeyBackend{backendPortal}, false},
		{"override gsettings anywhere", unknown, "gsettings", []hotkeyBackend{backendGsettings}, false},
		{"override none", waylandGNOME(), "none", nil, false},
		{"override case and space insensitive", waylandGNOME(), "  X11 ", []hotkeyBackend{backendX11}, false},
		{"override NONE uppercase", x11, "NONE", nil, false},
		{"unknown override on x11", x11, "wayland", []hotkeyBackend{backendX11}, true},
		{"unknown override on wayland gnome", waylandGNOME(), "BOGUS", []hotkeyBackend{backendPortal, backendGsettings}, true},
		{"unknown override on wayland sway", waylandSway(), "portal2", []hotkeyBackend{backendPortal, backendManual}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, warn := hotkeyPlan(tc.sess, tc.override)
			require.Equal(t, tc.want, plan)
			require.Equal(t, tc.warn, warn)
		})
	}
}

func TestHotkeyBackendString(t *testing.T) {
	require.Equal(t, "x11", backendX11.String())
	require.Equal(t, "portal", backendPortal.String())
	require.Equal(t, "gsettings", backendGsettings.String())
	require.Equal(t, "manual", backendManual.String())
}

func TestNativePathStoresHotkeyDescription(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.Startup(context.Background())
	require.True(t, r.has("startHotkey"))
	require.Equal(t, "alt+space", a.hotkeyDescription())
}

func TestWaylandStartupUsesPortalNotNative(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	h := &fakePortalHandle{desc: "Alt+Space"}
	hkCh := make(chan platform.Hotkey, 1)
	a.plat.startPortal = func(_ context.Context, hk platform.Hotkey, onActivated func()) (portalHandle, error) {
		r.call("startPortal")
		hkCh <- hk
		return h, nil
	}
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return a.hotkeyDescription() == "Alt+Space" },
		5*time.Second, 5*time.Millisecond, "the portal's bound description becomes the summon description")
	require.Equal(t, "alt+space", (<-hkCh).String(), "the parsed config hotkey reaches the portal seam")
	require.False(t, r.has("startHotkey"), "the native hotkey path never runs on wayland")
	require.False(t, r.has("ensureGnomeBinding"), "portal success stops the chain")

	a.Shutdown(context.Background())
	require.Equal(t, 1, h.closeCount(), "Shutdown closes the portal handle")
	a.Shutdown(context.Background())
	require.Equal(t, 1, h.closeCount(), "the close is not repeated")
}

func TestPortalEmptyBoundDescFallsBackToTriggerSyntax(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Hotkey: "ctrl+alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		return &fakePortalHandle{}, nil // backend reported no trigger_description
	}
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return a.hotkeyDescription() == "CTRL+ALT+space" },
		5*time.Second, 5*time.Millisecond)
}

func TestPortalUnavailableFallsBackToGnomeBinding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		r.call("startPortal")
		return nil, fmt.Errorf("probe: %w", portal.ErrNoGlobalShortcuts)
	}
	cmdCh := make(chan string, 1)
	a.plat.ensureGnomeBinding = func(_ context.Context, hk platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		cmdCh <- command
		return verifiedApplied(gsettings.Applied{
			Binding:   "<Control><Alt>space",
			Requested: "<Alt>space",
			FellBack:  true,
			Changed:   true,
		}, command), nil
	}
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Control><Alt>space" },
		5*time.Second, 5*time.Millisecond, "the installed GNOME accelerator becomes the summon description")
	require.Equal(t, gsettings.ToggleCommand("/test/bin/competent-search-thing"), <-cmdCh,
		"the keybinding runs the executable seam's binary with the toggle subcommand")
	require.True(t, r.has("startPortal"), "the portal was tried first")
	require.True(t, r.has("mediaKeysDaemon"), "the daemon self-check ran")
	require.False(t, r.has("startHotkey"))
}

func TestPortalDeniedStopsWithoutGnomeBinding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		r.call("startPortal")
		return nil, fmt.Errorf("bind: %w", portal.ErrDenied)
	}
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return r.has("startPortal") }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return r.has("ensureGnomeBinding") },
		300*time.Millisecond, 20*time.Millisecond,
		"a user denial is respected: no keybinding is written behind their back")
	require.Empty(t, a.hotkeyDescription())
}

func TestNonGnomeWaylandEndsInManualInstructions(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandSway() }
	a.Startup(context.Background()) // default portal stub answers ErrNoPortal

	require.Eventually(t, func() bool { return r.has("startPortal") }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return r.has("ensureGnomeBinding") || r.has("startHotkey") },
		300*time.Millisecond, 20*time.Millisecond)
	require.Empty(t, a.hotkeyDescription())
}

func TestOverrideX11OnWaylandUsesNativePath(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.getenv = backendEnv("x11")
	a.Startup(context.Background())

	require.True(t, r.has("startHotkey"), "the explicit override wins over the session plan")
	require.False(t, r.has("startPortal"))
	require.Equal(t, "alt+space", a.hotkeyDescription())
}

func TestOverrideNoneRegistersNothing(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.getenv = backendEnv("none")
	a.Startup(context.Background())

	require.False(t, r.has("startHotkey"))
	require.False(t, r.has("startPortal"))
	require.False(t, r.has("ensureGnomeBinding"))
	require.Empty(t, a.hotkeyDescription())
}

func TestUnknownOverrideFallsBackToAuto(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.getenv = backendEnv("bogus-backend")
	a.Startup(context.Background()) // unknown session -> auto -> native

	require.True(t, r.has("startHotkey"))
	require.Equal(t, "alt+space", a.hotkeyDescription())
}

func TestShutdownAbortsPendingPortalRegistration(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	started := make(chan struct{})
	a.plat.startPortal = func(ctx context.Context, _ platform.Hotkey, _ func()) (portalHandle, error) {
		close(started)
		<-ctx.Done() // the approval dialog nobody answers
		return nil, ctx.Err()
	}
	a.Startup(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("portal registration never started")
	}
	a.Shutdown(context.Background())

	require.Never(t, func() bool { return r.has("ensureGnomeBinding") },
		300*time.Millisecond, 20*time.Millisecond,
		"an aborted chain does not fall through to the next backend")
	require.Empty(t, a.hotkeyDescription())
}

func TestPortalHandleArrivingAfterShutdownIsClosed(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	release := make(chan struct{})
	started := make(chan struct{})
	h := &fakePortalHandle{desc: "Alt+Space"}
	a.plat.startPortal = func(context.Context, platform.Hotkey, func()) (portalHandle, error) {
		close(started)
		<-release
		return h, nil
	}
	a.Startup(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("portal registration never started")
	}
	a.Shutdown(context.Background())
	close(release)

	require.Eventually(t, func() bool { return h.closeCount() == 1 },
		5*time.Second, 5*time.Millisecond, "a handle Shutdown could not see is closed by the chain itself")
	require.Empty(t, a.hotkeyDescription())
}

func TestPortalActivationRoutesThroughToggle(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	actCh := make(chan func(), 1)
	a.plat.startPortal = func(_ context.Context, _ platform.Hotkey, onActivated func()) (portalHandle, error) {
		actCh <- onActivated
		return &fakePortalHandle{desc: "Alt+Space"}, nil
	}
	a.Startup(context.Background())
	var onActivated func()
	select {
	case onActivated = <-actCh:
	case <-time.After(5 * time.Second):
		t.Fatal("portal registration never started")
	}

	onActivated() // frontend not ready yet: the summon is deferred
	require.False(t, r.has("show"))
	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "DomReady executes the deferred portal summon")

	onActivated() // visible now: an activation toggles the bar away
	require.True(t, r.has("hide"))
}

func TestWaylandShowSkipsCursorPositioning(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.goos = "linux"
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	// A cursor-positioning world that WOULD move the window on X11
	// (see TestShowPositionsOnCursorDisplay).
	r.cursorOK = true
	r.cursorX, r.cursorY = -1000, 500
	r.displays = testDisplays()
	a.Startup(context.Background())

	a.showOnCursorDisplay()
	a.showOnCursorDisplay() // the compositor-placement log fires once; centering repeats

	require.True(t, r.has("center"), "wayland centers best-effort")
	require.True(t, r.has("show"))
	require.Len(t, r.emitted(eventShown), 2)
	require.False(t, r.has("getPos"), "no cursor-display positioning on wayland")
	require.False(t, r.has("setPos"))
	require.False(t, r.has("moveWindow"))
}

func TestStartPortalShortcutFailsWithoutSessionBus(t *testing.T) {
	// Hermetic: point the session-bus address somewhere dead so the
	// production seam's Dial fails deterministically (never reaching a
	// real desktop portal).
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/no-bus-here")
	h, err := startPortalShortcut(context.Background(), platform.Hotkey{Key: "space"}, nil)
	require.Error(t, err)
	require.Nil(t, h)
}
