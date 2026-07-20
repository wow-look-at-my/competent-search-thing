package ipc

// The ONE integration test that signals a real process -- and only a
// child this test spawned itself (the os.Args[0] re-exec helper
// pattern): a real wedged daemon, the real peercred read (the child's
// pid, not ours), the real /proc identity check, and the real
// SIGTERM. Everything else in this package stays scripted.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const helperSockEnv = "IPC_TEST_HELPER_SOCK"

// TestHelperWedgedDaemon is not a test: re-executed as a child process
// (guarded by helperSockEnv) it binds the socket and wedges -- listens,
// never accepts, never replies -- until a signal kills it.
func TestHelperWedgedDaemon(t *testing.T) {
	sock := os.Getenv(helperSockEnv)
	if sock == "" {
		t.Skip("helper mode only (spawned by TestTakeoverSigtermsRealWedgedChild)")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Println("BIND-FAILED:", err)
		return
	}
	defer ln.Close()
	fmt.Println("WEDGED")
	// Wedge without ever accepting. SIGTERM's default disposition ends
	// the process here (no handler installed), leaving the socket file
	// behind exactly like a crashed daemon.
	time.Sleep(5 * time.Minute)
}

func TestTakeoverSigtermsRealWedgedChild(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipcint")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperWedgedDaemon$")
	cmd.Env = append(os.Environ(), helperSockEnv+"="+path)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	child := cmd.Process.Pid
	t.Cleanup(func() {
		// Belt and suspenders: never leave the owned child behind (it
		// is normally dead from the takeover's SIGTERM already; the
		// reaper goroutine below collects it either way).
		_ = cmd.Process.Kill()
	})

	// Wait for the child to report its listener is bound and wedged.
	sc := bufio.NewScanner(out)
	require.True(t, sc.Scan(), "the helper never reported in")
	require.Contains(t, sc.Text(), "WEDGED", "the helper failed to bind: %s", sc.Text())
	reaped := make(chan error, 1)
	go func() { reaped <- cmd.Wait() }()

	// Production seams end to end: real dial, real SO_PEERCRED (the
	// CHILD's pid), real /proc identity (the child runs this same test
	// binary), real kill(2). Only the timings shrink.
	lr := &logRec{}
	s, err := ListenWith(path, "v", ListenOptions{
		Logf:         lr.logf,
		Build:        "b1",
		ProbeTimeout: 150 * time.Millisecond,
		ProbeGap:     20 * time.Millisecond,
		ReleaseWait:  5 * time.Second,
	})
	require.NoError(t, err, "the real wedged child is replaced; logs:\n%s", lr.joined())
	requireWorkingServer(t, s, path)

	lr.contains(t, fmt.Sprintf("sent SIGTERM to pid %d", child))
	lr.contains(t, "released the socket in")
	lr.contains(t, "took over as the single instance")

	// The child really died of that SIGTERM.
	select {
	case werr := <-reaped:
		require.Error(t, werr, "the child was signal-terminated, not a clean exit")
	case <-time.After(10 * time.Second):
		t.Fatal("the SIGTERM never terminated the wedged child")
	}
}
