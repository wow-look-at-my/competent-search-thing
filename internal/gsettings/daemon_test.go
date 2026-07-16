package gsettings

import (
	"bufio"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// spawnBus starts a throwaway private dbus-daemon for one test and
// returns its address (same pattern as internal/portal's tests). The
// daemon is stopped in cleanup by killing the exact PID we spawned.
func spawnBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed; skipping bus test")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	type read struct {
		line string
		err  error
	}
	lineCh := make(chan read, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		lineCh <- read{line: line, err: err}
	}()
	select {
	case r := <-lineCh:
		require.NoError(t, r.err)
		addr := strings.TrimSpace(r.line)
		require.NotEmpty(t, addr)
		return addr
	case <-time.After(10 * time.Second):
		t.Fatal("dbus-daemon did not print an address in time")
		return ""
	}
}

func TestDaemonRunningReportsOwnedName(t *testing.T) {
	addr := spawnBus(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	// Nothing owns the media-keys name on a fresh bus.
	running, err := DaemonRunning(context.Background())
	require.NoError(t, err)
	require.False(t, running)

	// A fake daemon claims the name; the probe must see it.
	owner, err := dbus.Connect(addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Close() })
	reply, err := owner.RequestName(DaemonName, dbus.NameFlagDoNotQueue)
	require.NoError(t, err)
	require.Equal(t, dbus.RequestNameReplyPrimaryOwner, reply)

	running, err = DaemonRunning(context.Background())
	require.NoError(t, err)
	require.True(t, running)
}

func TestDaemonRunningFailsWithoutSessionBus(t *testing.T) {
	// Headless CI: no session bus at all. The probe must return an
	// error (the caller skips the check silently), never crash or hang.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "absent.sock"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := DaemonRunning(ctx)
	require.Error(t, err)
}
