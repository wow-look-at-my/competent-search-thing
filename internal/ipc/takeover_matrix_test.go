package ipc

// The second half of the takeover matrix (shared fakes/recorders live
// in takeover_test.go): the no-takeover verdicts (healthy booting
// daemon, identity refusals), the mid-probe death recovery, the
// concurrent-decider race, the quit wire shapes, the verified-unlink
// guard, and unit coverage for the pure helpers.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- matrix row 7: a booting (not-ready) daemon is HEALTHY -------------------

func TestBootingSameBuildDaemonIsAlreadyRunning(t *testing.T) {
	path := testSocket(t)
	quiet := &logRec{}
	booting, err := ListenWith(path, "v", ListenOptions{Logf: quiet.logf, Build: "same-build-12"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = booting.Close() })
	// No SetHandlers: the instance is booting; version answers anyway.

	lr, kr := &logRec{}, &killRec{}
	s, err := ListenWith(path, "v", fastOpts(lr, kr, "same-build-12"))
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning,
		"a responsive same-build daemon is the one healthy outcome")
	require.Empty(t, kr.calls, "and is never probed with signals, let alone killed")
	lr.contains(t, "answered in")
}

// --- matrix row 9: holder dies mid-probe -> plain dead recovery --------------

func TestHolderDyingMidProbeBecomesPlainRecovery(t *testing.T) {
	path := testSocket(t)
	wedge := wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	var once sync.Once
	opts := fastOpts(lr, kr, "b1")
	// The gap sleep between attempts is the death moment: the wedge
	// closes (and unlinks), so the next attempt sees a dead socket.
	opts.sleep = func(time.Duration) {
		once.Do(func() { _ = wedge.Close() })
	}

	s, err := ListenWith(path, "v", opts)
	require.NoError(t, err)
	requireWorkingServer(t, s, path)
	require.Empty(t, kr.calls, "a holder that finished dying needs no ladder at all")
	lr.contains(t, "nothing holds the socket")
}

// --- matrix row 11: peer identity mismatch -> refuse to signal ---------------

func TestTakeoverRefusesForeignProcess(t *testing.T) {
	path := testSocket(t)
	_ = wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	opts := fastOpts(lr, kr, "b1")
	opts.procIdent = func(int) (string, string) { return "/usr/bin/definitely-not-us", "not-us" }

	s, err := ListenWith(path, "v", opts)
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning,
		"a foreign process on our path earns today's honest error, never a signal")
	require.Empty(t, kr.sigterms(), "NEVER signal a pid whose identity does not match")
	lr.contains(t, "refusing to signal it")
}

func TestTakeoverRefusesForeignUID(t *testing.T) {
	path := testSocket(t)
	_ = wedgeListener(t, path)
	lr, kr := &logRec{}, &killRec{}
	opts := fastOpts(lr, kr, "b1")
	opts.getuid = func() int { return os.Getuid() + 1 } // "our" uid differs from the peer's

	s, err := ListenWith(path, "v", opts)
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrAlreadyRunning)
	require.Empty(t, kr.sigterms())
	lr.contains(t, "not ours); refusing to signal it")
}

// --- matrix row 12: two concurrent takeover deciders, one winner -------------

func TestConcurrentTakeoverHasExactlyOneWinner(t *testing.T) {
	path := testSocket(t)
	// A dead instance's leftover: a socket file with no listener.
	addr, err := net.ResolveUnixAddr("unix", path)
	require.NoError(t, err)
	stale, err := net.ListenUnix("unix", addr)
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())

	lr := &logRec{}
	kr := &killRec{}
	type outcome struct {
		s   *Server
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			s, err := ListenWith(path, "v", fastOpts(lr, kr, "same-build-77"))
			results <- outcome{s: s, err: err}
		}()
	}
	var servers []*Server
	var already int
	for i := 0; i < 2; i++ {
		o := <-results
		switch {
		case o.err == nil:
			servers = append(servers, o.s)
		case errors.Is(o.err, ErrAlreadyRunning):
			already++
		default:
			t.Fatalf("unexpected outcome: %v", o.err)
		}
	}
	require.Len(t, servers, 1, "exactly one decider binds")
	require.Equal(t, 1, already, "the other concedes to the winner's healthy socket")
	t.Cleanup(func() { _ = servers[0].Close() })
	rep, err := Send(path, CmdPing, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK, "no ghost daemon: the surviving listener serves the path")
	require.Empty(t, kr.sigterms())
}

// --- matrix row 13: quit wire shapes + build-stamped version -----------------

func TestQuitCommandWireShapes(t *testing.T) {
	path := testSocket(t)
	quiet := &logRec{}
	s, err := ListenWith(path, "1.2.3-test", ListenOptions{Logf: quiet.logf, Build: "abcdef123456"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Unwired quit answers not-ready like every command.
	require.JSONEq(t, `{"ok":false,"error":"not ready"}`, rawExchange(t, path, `{"cmd":"quit"}`))

	ran := make(chan string, 1)
	s.SetHandlers(Handlers{Quit: func() { ran <- CmdQuit }})
	require.JSONEq(t, `{"ok":true,"accepted":"quit"}`, rawExchange(t, path, `{"cmd":"quit"}`))
	awaitSignal(t, ran, CmdQuit)

	// a8 parity: the bare legacy word stays an invalid request.
	require.JSONEq(t, `{"ok":false,"error":"invalid request"}`, rawExchange(t, path, "quit"))

	// The version reply carries the build stamp, and Send parses it.
	require.JSONEq(t, `{"ok":true,"version":"1.2.3-test","build":"abcdef123456"}`,
		rawExchange(t, path, `{"cmd":"version"}`))
	rep, err := Send(path, CmdVersion, time.Second)
	require.NoError(t, err)
	require.Equal(t, "abcdef123456", rep.Build)
	require.True(t, rep.Parsed)
}

// --- verified unlink: Close never removes a successor's socket ---------------

func TestCloseLeavesSuccessorSocketAlone(t *testing.T) {
	path := testSocket(t)
	lr := &logRec{}
	s1, err := ListenWith(path, "v", ListenOptions{Logf: lr.logf, Build: "b"})
	require.NoError(t, err)

	// A successor takes over: unlink + fresh bind (what the takeover
	// ladder does to a force-replaced zombie's path).
	require.NoError(t, os.Remove(path))
	ln2, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln2.Close() })

	// The zombie un-wedges into its graceful Close: the successor's
	// socket must survive.
	require.NoError(t, s1.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "the successor's socket file survives the old instance's Close")
	conn, err := net.Dial("unix", path)
	require.NoError(t, err, "and still accepts connections")
	_ = conn.Close()
	lr.contains(t, "now belongs to a newer instance")
}

// --- unit coverage for the helpers ------------------------------------------

func TestSameBinary(t *testing.T) {
	tests := []struct {
		name               string
		exe, comm, ownBase string
		want               bool
	}{
		{"exact exe", "/usr/bin/competent-search-thing", "competent-sear", "competent-search-thing", true},
		{"deleted suffix tolerated", "/usr/bin/competent-search-thing (deleted)", "", "competent-search-thing", true},
		{"comm prefix match", "", "competent-searc", "competent-search-thing", true},
		{"comm exact short name", "", "shortname", "shortname", true},
		{"foreign exe", "/usr/bin/evil", "evil", "competent-search-thing", false},
		{"foreign comm only", "", "evil", "competent-search-thing", false},
		{"empty metadata fails open", "", "", "competent-search-thing", true},
		{"no own base fails open", "/usr/bin/anything", "x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sameBinary(tt.exe, tt.comm, tt.ownBase))
		})
	}
}

func TestOwnBuildIsStable(t *testing.T) {
	a, b := OwnBuild(), OwnBuild()
	require.Equal(t, a, b)
	require.LessOrEqual(t, len(a), 12, "the stamp is the 12-char vcs revision prefix (or empty)")
}

func TestDescribeFailure(t *testing.T) {
	require.Contains(t, describeFailure(syscall.ECONNRESET, time.Second), "connection reset by peer")
	require.Contains(t, describeFailure(fmt.Errorf("read: %w", syscall.EPIPE), time.Second), "broken pipe")
	require.Contains(t, describeFailure(timeoutErr{}, 500*time.Millisecond), "timeout after 500ms")
	require.Equal(t, "weird", describeFailure(fmt.Errorf("weird"), time.Second))
}

// timeoutErr is a minimal net.Error whose Timeout reports true.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "deadline" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }
