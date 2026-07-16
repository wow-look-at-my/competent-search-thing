package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// newTestIPC starts a real IPC server on a private socket for the
// wiring tests (the ipc package itself is tested in internal/ipc).
func newTestIPC(t *testing.T) (*ipc.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	srv, err := ipc.Listen(path, "test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	return srv, path
}

func TestStartupWiresIPCHandlers(t *testing.T) {
	srv, path := newTestIPC(t)
	a, r := newTestApp(t, nil, Options{IPC: srv})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.Startup(context.Background())
	a.DomReady(context.Background())

	resp, err := ipc.Send(path, ipc.CmdShow, time.Second)
	require.NoError(t, err)
	require.Equal(t, ipc.ReplyOK, resp)
	require.True(t, r.has("show"), "IPC show reaches the window seam")
	require.Len(t, r.emitted(eventShown), 1)

	resp, err = ipc.Send(path, ipc.CmdToggle, time.Second)
	require.NoError(t, err)
	require.Equal(t, ipc.ReplyOK, resp)
	require.True(t, r.has("hide"), "IPC toggle on a visible bar hides it")

	resp, err = ipc.Send(path, ipc.CmdHide, time.Second)
	require.NoError(t, err)
	require.Equal(t, ipc.ReplyOK, resp, "IPC hide is wired (idempotent here)")
}

func TestShutdownClosesIPC(t *testing.T) {
	srv, path := newTestIPC(t)
	a, _ := newTestApp(t, nil, Options{IPC: srv})
	a.Startup(context.Background())
	a.Shutdown(context.Background())

	_, err := ipc.Send(path, ipc.CmdPing, 200*time.Millisecond)
	require.Error(t, err)
	require.True(t, ipc.IsNotRunning(err), "Shutdown closed the socket")

	// The newTestApp cleanup runs Shutdown again: closing must be
	// idempotent, which the deferred cleanup itself verifies.
}

func TestShowOnStartupWaitsForDomReady(t *testing.T) {
	a, r := newTestApp(t, nil, Options{ShowOnStartup: true})
	a.Startup(context.Background())
	require.False(t, r.has("show"), "nothing shows before the frontend is ready")

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "DomReady executes the pending show")

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "the pending show runs exactly once")
}

func TestEarlyToggleDefersToDomReady(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.Startup(context.Background())

	a.toggle() // frontend not ready: deferred
	require.False(t, r.has("show"))

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "the deferred summon runs at DomReady")

	a.toggle() // after DomReady, toggles act immediately
	require.True(t, r.has("hide"))
}

func TestEarlyShowDefersToDomReady(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())

	a.showIfHidden()
	require.False(t, r.has("show"))

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1)
}

func TestHideCancelsPendingShow(t *testing.T) {
	a, r := newTestApp(t, nil, Options{ShowOnStartup: true})
	a.Startup(context.Background())
	a.Hide() // e.g. an IPC hide while still booting
	a.DomReady(context.Background())
	require.False(t, r.has("show"), "hide before DomReady cancels the pending show")
	require.Empty(t, r.emitted(eventShown))
}

func TestShowIfHiddenWhenVisibleJustReShows(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.DomReady(context.Background())

	a.showIfHidden() // hidden -> full show path (cursor unknown: centers)
	require.Equal(t, []string{"center", "show"}, r.callNames())
	require.Len(t, r.emitted(eventShown), 1)

	a.showIfHidden() // visible -> re-show only, no reposition/capture
	require.Equal(t, []string{"center", "show", "show"}, r.callNames())
	require.Len(t, r.emitted(eventShown), 1, "no second shown event")
}

func TestDomReadyWithoutRuntimeCtxIsSafe(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.showIfHidden() // before Startup: deferred
	a.DomReady(nil)  // no runtime ctx anywhere: the show no-ops safely
	require.Empty(t, r.callNames())
	require.Empty(t, r.emits)
}

func TestNilIPCIsTolerated(t *testing.T) {
	a, r := newTestApp(t, nil, Options{}) // IPC nil, ShowOnStartup false
	a.Startup(context.Background())
	a.DomReady(context.Background())
	a.Shutdown(context.Background())
	require.False(t, r.has("show"), "nothing summons the bar on its own")
}
