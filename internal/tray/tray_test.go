package tray

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

const awaitTimeout = 10 * time.Second

// counter is a goroutine-safe call counter for callback assertions.
type counter struct {
	mu sync.Mutex
	n  int
	ch chan struct{}
}

func newCounter() *counter { return &counter{ch: make(chan struct{}, 32)} }

func (c *counter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	c.ch <- struct{}{}
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *counter) await(t *testing.T) {
	t.Helper()
	select {
	case <-c.ch:
	case <-time.After(awaitTimeout):
		t.Fatal("callback did not fire in time")
	}
}

func TestStartRegistersWithWatcherByObjectPath(t *testing.T) {
	addr := spawnBus(t)
	w := newFakeWatcher(t, addr)
	tr, rec := setupTray(t, addr, Options{})

	require.NoError(t, tr.Start(context.Background()))
	reg := w.Await(awaitTimeout)
	require.Equal(t, "/StatusNotifierItem", reg.service,
		"registration argument is the object path (resolved against the sender by the host)")
	require.NotEmpty(t, reg.sender)
	require.Equal(t, 1, rec.containing("registered"), "one success log line")
	require.Zero(t, rec.containing("no StatusNotifierItem host"))
}

func TestStartWithoutWatcherDegradesThenRegistersLate(t *testing.T) {
	addr := spawnBus(t)
	tr, rec := setupTray(t, addr, Options{})

	require.NoError(t, tr.Start(context.Background()))
	require.Eventually(t, func() bool {
		return rec.containing("no StatusNotifierItem host") == 1
	}, awaitTimeout, 10*time.Millisecond, "exactly one degradation line")

	// The host appears later (extension loads after the app): the
	// NameOwnerChanged watch registers without further prodding.
	w := newFakeWatcher(t, addr)
	reg := w.Await(awaitTimeout)
	require.Equal(t, "/StatusNotifierItem", reg.service)
	require.Equal(t, 1, rec.containing("no StatusNotifierItem host"), "still just the one line")
}

func TestWatcherRestartReregisters(t *testing.T) {
	addr := spawnBus(t)
	w := newFakeWatcher(t, addr)
	tr, _ := setupTray(t, addr, Options{})

	require.NoError(t, tr.Start(context.Background()))
	w.Await(awaitTimeout)

	// GNOME Shell restart: the watcher name changes owner. Releasing
	// and re-owning from a second connection makes a NEW owner, which
	// must trigger a fresh registration.
	w.Release()
	w2 := newFakeWatcher(t, addr)
	reg := w2.Await(awaitTimeout)
	require.Equal(t, "/StatusNotifierItem", reg.service)
}

func TestStartWithoutSessionBusIsQuiet(t *testing.T) {
	rec := &logRecorder{}
	tr := New(Options{ID: "x", Title: "X", Logf: rec.logf})
	tr.dial = func() (*dbus.Conn, error) { return nil, errors.New("no bus for you") }
	t.Cleanup(func() { _ = tr.Close() })

	require.NoError(t, tr.Start(context.Background()), "no session bus is degradation, not an error")
	require.Equal(t, 1, rec.containing("no session bus"), "exactly one log line")
	require.NoError(t, tr.Close())
}

func TestCloseIsIdempotentAndNilSafe(t *testing.T) {
	var nilTray *Tray
	require.NoError(t, nilTray.Close())

	// Close before Start.
	tr := New(Options{ID: "x", Title: "X", Logf: (&logRecorder{}).logf})
	require.NoError(t, tr.Close())
	require.NoError(t, tr.Close())
	// Start after Close stays off the bus.
	dialed := false
	tr.dial = func() (*dbus.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial")
	}
	require.NoError(t, tr.Start(context.Background()))
	require.False(t, dialed, "a closed tray never dials")
}

func TestCloseAfterStartStopsCleanly(t *testing.T) {
	addr := spawnBus(t)
	w := newFakeWatcher(t, addr)
	tr, _ := setupTray(t, addr, Options{})

	require.NoError(t, tr.Start(context.Background()))
	w.Await(awaitTimeout)

	done := tr.done // grab before Close nils it
	require.NoError(t, tr.Close())
	require.NoError(t, tr.Close(), "double Close")
	select {
	case <-done:
	case <-time.After(awaitTimeout):
		t.Fatal("watch goroutine leaked past Close")
	}

	// A watcher restart after Close must not re-register.
	w.Release()
	w2 := newFakeWatcher(t, addr)
	w2.AwaitNone(300 * time.Millisecond)
}

func TestStartTwiceIsANoOp(t *testing.T) {
	addr := spawnBus(t)
	w := newFakeWatcher(t, addr)
	tr, _ := setupTray(t, addr, Options{})

	require.NoError(t, tr.Start(context.Background()))
	w.Await(awaitTimeout)
	require.NoError(t, tr.Start(context.Background()))
	w.AwaitNone(300 * time.Millisecond)
}

func TestContextCancelStopsWatchLoop(t *testing.T) {
	addr := spawnBus(t)
	tr, _ := setupTray(t, addr, Options{})

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, tr.Start(ctx))
	done := tr.done
	require.NotNil(t, done)
	cancel()
	select {
	case <-done:
	case <-time.After(awaitTimeout):
		t.Fatal("watch goroutine ignored ctx cancellation")
	}
	// A late watcher then finds nobody listening.
	w := newFakeWatcher(t, addr)
	w.AwaitNone(300 * time.Millisecond)
}

func TestActivateMethodsFireCallback(t *testing.T) {
	addr := spawnBus(t)
	newFakeWatcher(t, addr)
	activated := newCounter()
	tr, _ := setupTray(t, addr, Options{OnActivate: activated.inc})
	require.NoError(t, tr.Start(context.Background()))

	host := connectBus(t, addr)
	item := host.Object(trayUniqueName(t, tr), itemPath)
	require.NoError(t, item.Call(itemIface+".Activate", 0, int32(1), int32(2)).Err)
	activated.await(t)
	require.NoError(t, item.Call(itemIface+".SecondaryActivate", 0, int32(1), int32(2)).Err)
	activated.await(t)
	require.NoError(t, item.Call(itemIface+".XAyatanaSecondaryActivate", 0, uint32(1234)).Err)
	activated.await(t)
	require.Equal(t, 3, activated.count())

	// ContextMenu and Scroll are acknowledged no-ops.
	require.NoError(t, item.Call(itemIface+".ContextMenu", 0, int32(1), int32(2)).Err)
	require.NoError(t, item.Call(itemIface+".Scroll", 0, int32(-3), "vertical").Err)
	require.Equal(t, 3, activated.count())
}

// trayUniqueName returns the tray connection's unique bus name, the
// address hosts talk back to.
func trayUniqueName(t *testing.T, tr *Tray) string {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	require.NotNil(t, tr.conn, "tray not started")
	names := tr.conn.Names()
	require.NotEmpty(t, names)
	return names[0]
}
