// Package tray puts a StatusNotifierItem tray icon on the session bus:
// the org.kde.StatusNotifierItem interface plus a com.canonical.dbusmenu
// menu, implemented directly over godbus -- no cgo, no GTK, no
// libappindicator, so there is no second main loop fighting Wails for
// the process. On Ubuntu's GNOME sessions the AppIndicator shell
// extension (ubuntu-appindicators@ubuntu.com, enabled by default) is
// the host that renders it; any other StatusNotifierItem host (KDE,
// waybar, ...) works the same way.
//
// The package carries no app wiring: callers describe the icon
// (Options: id, title, tooltip, menu items, activate callback) and the
// Tray owns one private session-bus connection for its lifetime.
// Everything degrades quietly: no session bus, or a bus without a
// StatusNotifierWatcher, logs one line and never blocks or errors the
// app -- the Tray keeps watching for a watcher to appear (the shell
// extension may load after the app on session startup) and registers
// whenever org.kde.StatusNotifierWatcher gains an owner, which also
// covers GNOME Shell restarts and extension reloads.
//
// Wire facts this implementation follows (verified against the
// gnome-shell-extension-appindicator v42 sources -- the version Ubuntu
// 22.04 ships -- and the freedesktop StatusNotifierItem spec):
// RegisterStatusNotifierItem is called with the OBJECT PATH, which the
// watcher resolves against the sender's unique name directly (a bus
// name argument would go through an extra async name resolution that
// can fail); icon pixmaps are ARGB32 in network byte order (bytes
// A,R,G,B per pixel, straight alpha); the host reads properties through
// org.freedesktop.DBus.Properties.GetAll and requires Id and Menu
// before it shows anything; menu clicks arrive as
// com.canonical.dbusmenu.Event with eventId "clicked".
package tray

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Bus identities of the watcher (the host side) and this item.
const (
	watcherBusName = "org.kde.StatusNotifierWatcher"
	watcherPath    = dbus.ObjectPath("/StatusNotifierWatcher")
	watcherIface   = "org.kde.StatusNotifierWatcher"

	itemPath  = dbus.ObjectPath("/StatusNotifierItem")
	itemIface = "org.kde.StatusNotifierItem"

	menuPath  = dbus.ObjectPath("/MenuBar")
	menuIface = "com.canonical.dbusmenu"
)

// signalBuffer sizes the NameOwnerChanged channel; godbus silently
// DROPS signals a full channel cannot accept (same rule as
// internal/portal).
const signalBuffer = 32

// callTimeout bounds every D-Bus method call the Tray itself makes
// (registration, name requests); nothing here is interactive.
const callTimeout = 10 * time.Second

// MenuItem is one entry of the static tray menu, in display order.
type MenuItem struct {
	// Label is the menu text. Underscores are mnemonic markers to
	// dbusmenu hosts, so avoid them.
	Label string
	// Separator renders a separator line instead of a clickable entry
	// (Label and OnClick are ignored).
	Separator bool
	// OnClick runs when the item is clicked. It is dispatched on a
	// D-Bus handler goroutine, so it must be goroutine-safe and should
	// return quickly.
	OnClick func()
}

// Options configures a Tray.
type Options struct {
	// ID is the StatusNotifierItem Id property, a stable
	// application identifier ("competent-search-thing"). Required.
	ID string
	// Title is the user-visible name (SNI Title, and the tooltip
	// title).
	Title string
	// Tooltip returns extra descriptive tooltip text (e.g. the active
	// summon shortcut), read at Start and refreshed on every
	// (re-)registration with a host; "" means title-only. May be nil.
	// (GNOME's AppIndicator extension does not render SNI tooltips at
	// all -- the property exists for hosts that do, like KDE.)
	Tooltip func() string
	// Menu is the static menu, top to bottom.
	Menu []MenuItem
	// OnActivate runs when the icon itself is activated (Activate /
	// SecondaryActivate -- double or middle click under GNOME's
	// extension, which opens the menu on a plain left click). Same
	// goroutine rules as MenuItem.OnClick.
	OnActivate func()
	// Logf receives the package's few log lines; nil means
	// log.Printf.
	Logf func(format string, v ...interface{})
}

// Tray is one StatusNotifierItem on one private session-bus
// connection. Create with New, bring up with Start, tear down with
// Close.
type Tray struct {
	opts Options

	// dial opens the session-bus connection; tests point it at a
	// throwaway bus. The default never autolaunches a dbus-daemon:
	// a session without a bus is a normal degraded environment
	// (headless CI), not something to spawn daemons over.
	dial func() (*dbus.Conn, error)

	mu      sync.Mutex
	conn    *dbus.Conn
	props   *itemProps
	menu    *menu
	cancel  context.CancelFunc
	done    chan struct{} // closed when the watch goroutine exits
	started bool
	closed  bool
}

// New creates a Tray around opts. Nothing touches the bus until Start.
func New(opts Options) *Tray {
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &Tray{opts: opts, dial: Dial}
}

// Dial opens a private session-bus connection (authenticated and
// hello'd) for the Tray to own. It deliberately does NOT autolaunch a
// dbus-daemon when no session bus exists -- in that case it fails and
// the tray stays off.
func Dial() (*dbus.Conn, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, fmt.Errorf("tray: session bus: %w", err)
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tray: session bus auth: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tray: session bus hello: %w", err)
	}
	return conn, nil
}

// Start connects to the session bus, exports the StatusNotifierItem
// and dbusmenu objects, and registers with the StatusNotifierWatcher
// when one is present -- otherwise it says so once and keeps a
// NameOwnerChanged watch alive so a watcher that appears later (shell
// extension loading after the app, GNOME Shell restarts) picks the
// icon up automatically. Degraded environments are not errors: no
// session bus at all logs one line and returns nil. Start returns an
// error only for real setup failures (exports, subscriptions) and is
// bounded -- the caller may still run it off the startup path.
// Cancelling ctx stops the watch goroutine (Close does too).
func (t *Tray) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.started || t.closed {
		t.mu.Unlock()
		return nil
	}
	t.started = true
	t.mu.Unlock()

	conn, err := t.dial()
	if err != nil {
		t.opts.Logf("tray: no session bus (%v); tray icon disabled", err)
		return nil
	}

	if err := t.export(conn); err != nil {
		_ = conn.Close()
		return fmt.Errorf("tray: exporting objects: %w", err)
	}

	// The well-known name is the KDE convention
	// (org.kde.StatusNotifierItem-<pid>-1); registration itself uses
	// the object path, so a refused name is harmless -- hosts identify
	// the item by unique name + path.
	wellKnown := fmt.Sprintf("%s-%d-1", itemIface, os.Getpid())
	if _, err := conn.RequestName(wellKnown, dbus.NameFlagDoNotQueue); err != nil {
		t.opts.Logf("tray: requesting %s: %v (continuing)", wellKnown, err)
	}

	// Subscribe to watcher (re)appearances BEFORE probing, so a
	// watcher starting between the probe and the subscription is not
	// missed.
	if err := conn.AddMatchSignal(watcherOwnerMatch()...); err != nil {
		_ = conn.Close()
		return fmt.Errorf("tray: subscribing to NameOwnerChanged: %w", err)
	}
	ch := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(ch)

	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	t.mu.Lock()
	if t.closed {
		// Close raced Start: undo and get out.
		t.mu.Unlock()
		cancel()
		close(done)
		_ = conn.Close()
		return nil
	}
	t.conn = conn
	t.cancel = cancel
	t.done = done
	t.mu.Unlock()

	if t.watcherPresent(watchCtx, conn) {
		t.register(watchCtx, conn)
	} else {
		t.opts.Logf("tray: no StatusNotifierItem host on the session bus; tray icon disabled (registering if one appears)")
	}

	go t.watch(watchCtx, conn, ch, done)
	return nil
}

// watcherPresent reports whether org.kde.StatusNotifierWatcher
// currently has an owner.
func (t *Tray) watcherPresent(ctx context.Context, conn *dbus.Conn) bool {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var owned bool
	err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, watcherBusName).Store(&owned)
	return err == nil && owned
}

// register announces the item to the watcher. The argument is the
// item's OBJECT PATH: the AppIndicator extension resolves a leading
// "/" against the sender's unique bus name directly (its most robust
// input -- a bus-name argument takes an extra async name resolution
// that can fail), and KDE's watcher handles the same form. Refreshing
// the tooltip here keeps late-bound summon shortcuts (the portal
// approval can land minutes after startup) visible to hosts that
// re-read it.
func (t *Tray) register(ctx context.Context, conn *dbus.Conn) {
	t.refreshTooltip()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	call := conn.Object(watcherBusName, watcherPath).CallWithContext(
		ctx, watcherIface+".RegisterStatusNotifierItem", 0, string(itemPath))
	if call.Err != nil {
		t.opts.Logf("tray: registering with the StatusNotifierWatcher: %v", call.Err)
		return
	}
	t.opts.Logf("tray: icon registered with the StatusNotifierItem host")
}

// watch re-registers whenever the watcher name gains a new owner --
// GNOME Shell restarts, extension reloads, or a host appearing for the
// first time after a degraded start.
func (t *Tray) watch(ctx context.Context, conn *dbus.Conn, ch chan *dbus.Signal, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-ch:
			if !ok {
				return // connection closed under us
			}
			if newWatcherOwner(sig) {
				t.register(ctx, conn)
			}
		}
	}
}

// newWatcherOwner reports whether sig is NameOwnerChanged announcing a
// fresh owner for the watcher name. Body: (name, oldOwner, newOwner).
func newWatcherOwner(sig *dbus.Signal) bool {
	if sig == nil || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
		return false
	}
	name, _ := sig.Body[0].(string)
	newOwner, _ := sig.Body[2].(string)
	return name == watcherBusName && newOwner != ""
}

// refreshTooltip re-reads Options.Tooltip into the ToolTip property,
// telling hosts via NewToolTip when it changed.
func (t *Tray) refreshTooltip() {
	if t.opts.Tooltip == nil {
		return
	}
	text := t.opts.Tooltip()
	t.mu.Lock()
	props := t.props
	conn := t.conn
	t.mu.Unlock()
	if props == nil || conn == nil {
		return
	}
	if props.setTooltipText(text) {
		if err := conn.Emit(itemPath, itemIface+".NewToolTip"); err != nil {
			t.opts.Logf("tray: emitting NewToolTip: %v", err)
		}
	}
}

// Close tears the tray down: it stops the watch goroutine, releases
// the connection (which unregisters the item -- hosts watch the unique
// name), and is idempotent, nil-safe, and bounded, so quit never
// hangs on it.
func (t *Tray) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conn := t.conn
	cancel := t.cancel
	done := t.done
	t.conn = nil
	t.cancel = nil
	t.done = nil
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var err error
	if conn != nil {
		_ = conn.RemoveMatchSignal(watcherOwnerMatch()...)
		err = conn.Close()
	}
	if done != nil {
		<-done // exits promptly: ctx is cancelled and the channel closes
	}
	return err
}

// watcherOwnerMatch builds the NameOwnerChanged match for the watcher
// name. Add and Remove must use identical options.
func watcherOwnerMatch() []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherBusName),
	}
}
