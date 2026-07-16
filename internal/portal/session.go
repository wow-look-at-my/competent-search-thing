package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Phase timeouts. CreateSession and ListShortcuts are non-interactive
// round trips; BindShortcuts may pop an approval dialog the user has
// to answer, so it gets an interactive-scale wait. Every phase also
// aborts as soon as the caller's ctx is done.
const (
	createTimeout = 25 * time.Second
	listTimeout   = 25 * time.Second
	bindTimeout   = 5 * time.Minute
)

// signalBuffer sizes every channel handed to godbus's signal
// dispatcher, which silently DROPS a signal whenever the channel
// cannot accept it immediately. Never register an unbuffered channel.
const signalBuffer = 32

// Options configures Register.
type Options struct {
	// ShortcutID is the stable identifier of the shortcut. The portal
	// keys remembered approvals on it, so keep it constant across runs
	// ("toggle"). Required.
	ShortcutID string

	// Description is the user-readable purpose the portal shows in its
	// approval dialog and settings UI.
	Description string

	// PreferredTrigger is the suggested trigger in shortcuts-spec
	// syntax (see TriggerString). Optional; backends may ignore it or
	// let the user rebind.
	PreferredTrigger string

	// OnActivated runs on the session's dispatch goroutine every time
	// the portal reports the shortcut pressed. It must be
	// goroutine-safe and should return quickly.
	OnActivated func()
}

// Session is one registered global shortcut on one portal session.
// Closing the underlying *dbus.Conn also ends the portal session (a
// vanishing client closes its sessions) and stops the dispatch
// goroutine.
type Session struct {
	// BoundDescription is the backend's user-readable description of
	// the trigger actually bound (e.g. "Alt+Space") when the portal
	// provided one, "" otherwise. For logging only -- the portal
	// defines no machine-parseable form of the effective trigger.
	BoundDescription string

	conn   *dbus.Conn
	handle string // session object path; typed "s" on the wire (documented erratum)
	id     string // the shortcut this session dispatches
	ch     chan *dbus.Signal
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// Handle returns the session's object path, for logging.
func (s *Session) Handle() string { return s.handle }

// Close ends the shortcut subscription: it stops the dispatch
// goroutine, removes the Activated signal match and best-effort calls
// org.freedesktop.portal.Session.Close on the session object. It is
// idempotent and never closes the underlying connection.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.RemoveMatchSignal(activatedMatch()...)
		s.conn.RemoveSignal(s.ch)
		call := s.conn.Object(portalBusName, dbus.ObjectPath(s.handle)).Call(sessionIface+".Close", 0)
		if call.Err != nil {
			s.closeErr = fmt.Errorf("portal: closing session %s: %w", s.handle, call.Err)
		}
	})
	return s.closeErr
}

// dispatch pumps Activated signals into OnActivated until Close or
// until godbus closes the channel (connection shutdown).
func (s *Session) dispatch(onActivated func()) {
	for {
		select {
		case <-s.done:
			return
		case sig, ok := <-s.ch:
			if !ok {
				return
			}
			if !s.matches(sig) {
				continue
			}
			if onActivated != nil {
				onActivated()
			}
		}
	}
}

// matches reports whether sig is GlobalShortcuts.Activated for THIS
// session's shortcut. Deactivated, other sessions and other shortcut
// ids are ignored. Body: (session_handle o, shortcut_id s,
// timestamp t, options a{sv}).
func (s *Session) matches(sig *dbus.Signal) bool {
	if sig == nil || sig.Name != signalActivated || len(sig.Body) < 2 {
		return false
	}
	handle, ok := sig.Body[0].(dbus.ObjectPath)
	if !ok {
		return false
	}
	id, ok := sig.Body[1].(string)
	if !ok {
		return false
	}
	return string(handle) == s.handle && id == s.id
}

// Register creates a GlobalShortcuts session on conn, makes sure
// opts.ShortcutID is bound (asking the portal to bind -- possibly via
// an interactive approval dialog -- only when ListShortcuts does not
// already report it: a session may attempt BindShortcuts exactly once,
// and the portal remembers approvals across sessions), and starts a
// goroutine that invokes opts.OnActivated on every activation of that
// shortcut. The connection must stay open for the Session's lifetime;
// a user refusal surfaces as ErrDenied (errors.Is).
func Register(ctx context.Context, conn *dbus.Conn, opts Options) (*Session, error) {
	if opts.ShortcutID == "" {
		return nil, errors.New("portal: Options.ShortcutID is required")
	}
	sender, err := connSender(conn)
	if err != nil {
		return nil, err
	}

	c := &caller{
		conn:   conn,
		obj:    conn.Object(portalBusName, desktopPath),
		sender: sender,
		ch:     make(chan *dbus.Signal, signalBuffer),
	}
	conn.Signal(c.ch)
	defer conn.RemoveSignal(c.ch)

	// Phase 1: CreateSession. The extra session_handle_token fixes the
	// session object path, but the handle is taken from the response.
	sessTok, err := newToken()
	if err != nil {
		return nil, err
	}
	code, results, err := c.request(ctx, createTimeout, "CreateSession", map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessTok),
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("portal: CreateSession failed with response code %d", code)
	}
	handle, err := sessionHandleFrom(results)
	if err != nil {
		return nil, err
	}

	// Phase 2: ListShortcuts -- is the shortcut already bound (this
	// session, or remembered from an earlier one)?
	code, results, err = c.request(ctx, listTimeout, "ListShortcuts", nil, dbus.ObjectPath(handle))
	if err != nil {
		closeSessionObject(conn, handle)
		return nil, err
	}
	if code != 0 {
		closeSessionObject(conn, handle)
		return nil, fmt.Errorf("portal: ListShortcuts failed with response code %d", code)
	}
	bound, boundDesc := findShortcut(shortcutsFrom(results), opts.ShortcutID)

	// Phase 3: BindShortcuts, only when needed (binding twice on one
	// session is forbidden). This is the phase that may block on a
	// portal approval dialog.
	if !bound {
		data := map[string]dbus.Variant{}
		if opts.Description != "" {
			data["description"] = dbus.MakeVariant(opts.Description)
		}
		if opts.PreferredTrigger != "" {
			data["preferred_trigger"] = dbus.MakeVariant(opts.PreferredTrigger)
		}
		code, results, err = c.request(ctx, bindTimeout, "BindShortcuts",
			nil,
			dbus.ObjectPath(handle),
			[]shortcut{{ID: opts.ShortcutID, Data: data}},
			"", // parent_window: the bar has no portal-addressable parent
		)
		switch {
		case err != nil:
			closeSessionObject(conn, handle)
			return nil, err
		case code == 1:
			closeSessionObject(conn, handle)
			return nil, fmt.Errorf("portal: BindShortcuts: %w", ErrDenied)
		case code != 0:
			closeSessionObject(conn, handle)
			return nil, fmt.Errorf("portal: BindShortcuts failed with response code %d", code)
		}
		_, boundDesc = findShortcut(shortcutsFrom(results), opts.ShortcutID)
	}

	// Phase 4: subscribe to Activated and start dispatching.
	if err := conn.AddMatchSignal(activatedMatch()...); err != nil {
		closeSessionObject(conn, handle)
		return nil, fmt.Errorf("portal: subscribing to Activated: %w", err)
	}
	s := &Session{
		BoundDescription: boundDesc,
		conn:             conn,
		handle:           handle,
		id:               opts.ShortcutID,
		ch:               make(chan *dbus.Signal, signalBuffer),
		done:             make(chan struct{}),
	}
	conn.Signal(s.ch)
	go s.dispatch(opts.OnActivated)
	return s, nil
}

// caller drives Request-convention method calls on the desktop object:
// subscribe on the predicted request path first, call, await Response.
type caller struct {
	conn   *dbus.Conn
	obj    dbus.BusObject
	sender string
	ch     chan *dbus.Signal
}

// request performs one GlobalShortcuts method call and waits for its
// org.freedesktop.portal.Request.Response, returning the response code
// and results vardict. args are the method's leading arguments; the
// trailing options vardict (handle_token plus extraOpts) is appended
// here. The wait is bounded by timeout and by ctx.
func (c *caller) request(ctx context.Context, timeout time.Duration, method string, extraOpts map[string]dbus.Variant, args ...interface{}) (uint32, map[string]dbus.Variant, error) {
	tok, err := newToken()
	if err != nil {
		return 0, nil, err
	}
	options := map[string]dbus.Variant{"handle_token": dbus.MakeVariant(tok)}
	for k, v := range extraOpts {
		options[k] = v
	}

	// Subscribe on the predicted request path BEFORE calling, per the
	// Request docs, so a fast portal cannot respond before we listen.
	predicted := requestPath(c.sender, tok)
	if err := c.conn.AddMatchSignal(responseMatch(predicted)...); err != nil {
		return 0, nil, fmt.Errorf("portal: %s: subscribing for the response: %w", method, err)
	}
	defer func() { _ = c.conn.RemoveMatchSignal(responseMatch(predicted)...) }()

	phaseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	call := c.obj.CallWithContext(phaseCtx, shortcutsIface+"."+method, 0, append(args, options)...)
	if call.Err != nil {
		return 0, nil, fmt.Errorf("portal: %s: %w", method, call.Err)
	}
	var handle dbus.ObjectPath
	if err := call.Store(&handle); err != nil {
		return 0, nil, fmt.Errorf("portal: %s returned no request handle: %w", method, err)
	}

	// Very old portals predate predictable request paths: when the
	// returned handle differs from the prediction, listen there too.
	accept := map[dbus.ObjectPath]bool{predicted: true}
	if handle != "" && handle != predicted {
		if err := c.conn.AddMatchSignal(responseMatch(handle)...); err != nil {
			return 0, nil, fmt.Errorf("portal: %s: subscribing for the response: %w", method, err)
		}
		defer func() { _ = c.conn.RemoveMatchSignal(responseMatch(handle)...) }()
		accept[handle] = true
	}

	for {
		select {
		case <-phaseCtx.Done():
			return 0, nil, fmt.Errorf("portal: %s: awaiting response: %w", method, phaseCtx.Err())
		case sig, ok := <-c.ch:
			if !ok {
				return 0, nil, fmt.Errorf("portal: %s: connection closed while awaiting response", method)
			}
			if sig == nil || sig.Name != signalResponse || !accept[sig.Path] {
				continue
			}
			return parseResponse(sig)
		}
	}
}

// parseResponse unpacks a Request.Response signal body:
// (response u, results a{sv}).
func parseResponse(sig *dbus.Signal) (uint32, map[string]dbus.Variant, error) {
	if len(sig.Body) < 2 {
		return 0, nil, fmt.Errorf("portal: malformed Response signal with %d fields", len(sig.Body))
	}
	code, ok := sig.Body[0].(uint32)
	if !ok {
		return 0, nil, fmt.Errorf("portal: malformed Response code of type %T", sig.Body[0])
	}
	results, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return 0, nil, fmt.Errorf("portal: malformed Response results of type %T", sig.Body[1])
	}
	return code, results, nil
}

// shortcut is the wire shape of one a(sa{sv}) element: BindShortcuts
// input and Bind/ListShortcuts response entries. Field order is the
// wire order; do not reorder.
type shortcut struct {
	ID   string
	Data map[string]dbus.Variant
}

// shortcutsFrom extracts the "shortcuts" a(sa{sv}) array from Response
// results. Missing or malformed both come back empty -- callers treat
// them as "nothing bound".
func shortcutsFrom(results map[string]dbus.Variant) []shortcut {
	v, ok := results["shortcuts"]
	if !ok {
		return nil
	}
	var scs []shortcut
	if err := dbus.Store([]interface{}{v.Value()}, &scs); err != nil {
		return nil
	}
	return scs
}

// findShortcut reports whether id is among scs, and the
// trigger_description its entry carries, when any.
func findShortcut(scs []shortcut, id string) (found bool, triggerDesc string) {
	for _, sc := range scs {
		if sc.ID != id {
			continue
		}
		found = true
		if v, ok := sc.Data["trigger_description"]; ok {
			if s, ok := v.Value().(string); ok {
				triggerDesc = s
			}
		}
	}
	return found, triggerDesc
}

// sessionHandleFrom pulls session_handle out of CreateSession results.
// The portal types it "s" -- a documented erratum kept for backwards
// compatibility -- but an object path is accepted too, for robustness.
func sessionHandleFrom(results map[string]dbus.Variant) (string, error) {
	v, ok := results["session_handle"]
	if !ok {
		return "", errors.New("portal: CreateSession response lacks session_handle")
	}
	switch h := v.Value().(type) {
	case string:
		return h, nil
	case dbus.ObjectPath:
		return string(h), nil
	}
	return "", fmt.Errorf("portal: CreateSession session_handle has unexpected type %T", v.Value())
}

// senderElement derives the request/session path element from a bus
// unique name: strip the leading ':' and turn every '.' into '_'
// (":1.42" -> "1_42"), per the portal Request/Session docs.
func senderElement(uniqueName string) string {
	return strings.ReplaceAll(strings.TrimPrefix(uniqueName, ":"), ".", "_")
}

// connSender returns conn's unique name as a path element.
func connSender(conn *dbus.Conn) (string, error) {
	names := conn.Names()
	if len(names) == 0 || names[0] == "" {
		return "", errors.New("portal: connection has no unique bus name")
	}
	return senderElement(names[0]), nil
}

// requestPath predicts the Request object path for a handle_token.
func requestPath(sender, token string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
}

// newToken returns a fresh random token for handle_token /
// session_handle_token: unique, unguessable, and a valid D-Bus path
// element ([A-Za-z0-9_]).
func newToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("portal: generating token: %w", err)
	}
	return "cst" + hex.EncodeToString(b[:]), nil
}

// responseMatch builds the match options for Request.Response on one
// request path. Add and Remove must use identical options.
func responseMatch(path dbus.ObjectPath) []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchInterface(requestIface),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(path),
	}
}

// activatedMatch builds the match options for GlobalShortcuts.Activated.
func activatedMatch() []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchInterface(shortcutsIface),
		dbus.WithMatchMember("Activated"),
	}
}

// closeSessionObject best-effort closes a session that Register
// created but cannot hand to the caller.
func closeSessionObject(conn *dbus.Conn, handle string) {
	_ = conn.Object(portalBusName, dbus.ObjectPath(handle)).Call(sessionIface+".Close", 0).Err
}
