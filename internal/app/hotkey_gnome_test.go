package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/gsettings"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

func TestGnomeBindingFailureEndsInManualInstructions(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	called := make(chan struct{})
	a.plat.ensureGnomeBinding = func(context.Context, platform.Hotkey, string, bool) (gsettings.Applied, error) {
		close(called)
		return gsettings.Applied{}, fmt.Errorf("%w (tried a, b, c)", gsettings.ErrAllTaken)
	}
	a.Startup(context.Background())

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the gsettings backend was never tried")
	}
	require.Never(t, func() bool { return a.hotkeyDescription() != "" }, 300*time.Millisecond, 20*time.Millisecond)
	require.False(t, r.has("startHotkey"))
}

func TestExistingGnomeBindingBecomesDescription(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		return verifiedApplied(gsettings.Applied{Binding: "<Shift><Super>t", Requested: "<Alt>space", Existing: true}, command), nil
	}
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Shift><Super>t" },
		5*time.Second, 5*time.Millisecond, "a user-edited binding is reported as-is")
}

func TestUnverifiedGnomeBindingClaimsNoHotkey(t *testing.T) {
	// gsettings reported success but the read-back could not confirm
	// the entry: the app must not advertise a summon key it cannot
	// stand behind (the old behavior logged "active" here).
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	called := make(chan struct{})
	a.plat.ensureGnomeBinding = func(context.Context, platform.Hotkey, string, bool) (gsettings.Applied, error) {
		close(called)
		return gsettings.Applied{
			Binding:    "<Control><Alt>space",
			Requested:  "<Alt>space",
			FellBack:   true,
			Changed:    true,
			VerifyNote: "the entry is not in the custom-keybindings list",
		}, nil
	}
	a.Startup(context.Background())

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the gsettings backend was never tried")
	}
	require.Never(t, func() bool { return a.hotkeyDescription() != "" },
		300*time.Millisecond, 20*time.Millisecond,
		"an unverified binding never becomes the summon description")
	require.False(t, r.has("mediaKeysDaemon"), "the daemon probe is pointless when the entry is already unverified")
}

func TestMissingMediaKeysDaemonClaimsNoHotkey(t *testing.T) {
	// The entry verified on disk but nothing owns the media-keys bus
	// name: no process exists to grab the key, so the app must warn
	// instead of claiming an active hotkey.
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		return verifiedApplied(gsettings.Applied{Binding: "<Control><Alt>space", Requested: "<Alt>space", Changed: true, FellBack: true}, command), nil
	}
	a.plat.mediaKeysDaemon = func(context.Context) (bool, error) {
		r.call("mediaKeysDaemon")
		return false, nil
	}
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return r.has("mediaKeysDaemon") }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return a.hotkeyDescription() != "" },
		300*time.Millisecond, 20*time.Millisecond)
}

func TestDaemonProbeErrorIsIgnored(t *testing.T) {
	// No session bus (headless CI): the probe errors and the check is
	// skipped -- a verified binding is still reported as active.
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		return verifiedApplied(gsettings.Applied{Binding: "<Control><Alt>space", Requested: "<Alt>space", Changed: true}, command), nil
	}
	a.plat.mediaKeysDaemon = func(context.Context) (bool, error) {
		return false, errors.New("no session bus")
	}
	a.Startup(context.Background())
	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Control><Alt>space" },
		5*time.Second, 5*time.Millisecond)
}

func TestRelativeExecutableIsResolvedForGnomeBinding(t *testing.T) {
	// gsd runs the command with its own working directory, so a
	// relative executable path would break the keybinding: the app
	// must resolve it before writing.
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.executable = func() (string, error) { return "rel/competent-search-thing", nil }
	cmdCh := make(chan string, 1)
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		cmdCh <- command
		return verifiedApplied(gsettings.Applied{Binding: "<Control><Alt>space", Requested: "<Alt>space", Changed: true}, command), nil
	}
	a.Startup(context.Background())

	select {
	case command := <-cmdCh:
		exe := strings.TrimSuffix(command, " toggle")
		require.True(t, filepath.IsAbs(exe), "the written command names the binary absolutely, got %q", command)
	case <-time.After(5 * time.Second):
		t.Fatal("the gsettings backend was never tried")
	}
}

// brewFixture builds the Homebrew install shape the executable seam
// and PATH see in the field: the real binary in a versioned Cellar
// dir, a stable symlink shim in a bin dir (with a space in its path,
// so quoting is exercised end-to-end) that is put on PATH. Returns
// (resolved Cellar path, shim path).
func brewFixture(t *testing.T) (exe, shim string) {
	t.Helper()
	root := t.TempDir()
	exe = filepath.Join(root, "Cellar", "competent-search-thing", "1.0.0", "bin", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	binDir := filepath.Join(root, "brew prefix", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	shim = filepath.Join(binDir, "competent-search-thing")
	require.NoError(t, os.Symlink(exe, shim))
	t.Setenv("PATH", binDir)
	return exe, shim
}

// gnomeBindingCommand runs Startup on a Wayland-GNOME session with the
// portal unavailable and returns the command the gsettings backend was
// asked to write.
func gnomeBindingCommand(t *testing.T, a *App) string {
	t.Helper()
	cmdCh := make(chan string, 1)
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		cmdCh <- command
		return verifiedApplied(gsettings.Applied{Binding: "<Control><Alt>space", Requested: "<Alt>space", Changed: true}, command), nil
	}
	a.Startup(context.Background())
	select {
	case command := <-cmdCh:
		return command
	case <-time.After(5 * time.Second):
		t.Fatal("the gsettings backend was never tried")
		return ""
	}
}

func TestGnomeBindingUsesStablePathShim(t *testing.T) {
	// The Homebrew field scenario end-to-end through the app: the
	// executable seam reports the resolved versioned Cellar path (what
	// os.Executable yields on Linux), the stable shim is on PATH, and
	// the written command must name the shim -- double-quoted, the
	// prefix contains a space -- never the version-pinned Cellar dir
	// the next upgrade deletes.
	exe, shim := brewFixture(t)
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.executable = func() (string, error) { return exe, nil }

	command := gnomeBindingCommand(t, a)
	require.Equal(t, gsettings.ToggleCommand(shim), command)
	require.Equal(t, `"`+shim+`" toggle`, command, "the space in the prefix keeps the shim quoted")
	require.NotContains(t, command, "Cellar", "the versioned path never reaches the keybinding")
}

func TestGnomeBindingUsesArgs0WhenPathMisses(t *testing.T) {
	// Nothing on PATH, but the binary was launched through a symlink:
	// the args0 seam feeds the launched spelling to the selection.
	root := t.TempDir()
	exe := filepath.Join(root, "store", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	link := filepath.Join(root, "apps", "competent-search-thing")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(exe, link))
	t.Setenv("PATH", t.TempDir())

	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.executable = func() (string, error) { return exe, nil }
	a.plat.args0 = func() string { return link }

	require.Equal(t, gsettings.ToggleCommand(link), gnomeBindingCommand(t, a))
}

// logBuffer is a goroutine-safe log sink (the hotkey chain logs from
// its own goroutine).
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func TestGnomeBindingRepairIsLoggedLoudly(t *testing.T) {
	// A self-healed command surfaces as ONE explicit old -> new repair
	// line, and the flow still ends in the honest existing-entry
	// summary with the accelerator untouched.
	a, _ := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.ensureGnomeBinding = func(_ context.Context, _ platform.Hotkey, command string, _ bool) (gsettings.Applied, error) {
		return verifiedApplied(gsettings.Applied{
			Binding:         "<Control><Alt>space",
			Requested:       "<Alt>space",
			Existing:        true,
			Changed:         true,
			Repaired:        true,
			PreviousCommand: "/old/Cellar/1.0.0/bin/cst toggle",
		}, command), nil
	}
	buf := captureLog(t)
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return a.hotkeyDescription() == "<Control><Alt>space" },
		5*time.Second, 5*time.Millisecond)
	logs := buf.String()
	require.Contains(t, logs, `repaired the GNOME keybinding command: "/old/Cellar/1.0.0/bin/cst toggle" -> `)
	require.Contains(t, logs, "using existing GNOME keybinding <Control><Alt>space",
		"the repair still ends in the honest existing-entry summary")
}

func TestEmptyExecutableSkipsGnomeBinding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.executable = func() (string, error) { return "", nil }
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return r.has("startPortal") }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return r.has("ensureGnomeBinding") }, 300*time.Millisecond, 20*time.Millisecond,
		"an empty executable path is refused, never written into a keybinding")
}

func TestExecutableFailureSkipsGnomeBinding(t *testing.T) {
	a, r := newTestApp(t, nil, Options{Hotkey: "alt+space"})
	a.plat.detectSession = func() platform.Session { return waylandGNOME() }
	a.plat.executable = func() (string, error) { return "", errors.New("no argv0") }
	a.Startup(context.Background())

	require.Eventually(t, func() bool { return r.has("startPortal") }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return r.has("ensureGnomeBinding") }, 300*time.Millisecond, 20*time.Millisecond)
}
