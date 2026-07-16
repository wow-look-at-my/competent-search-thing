package portal

import (
	"context"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// waitFired asserts OnActivated fires within a generous deadline.
func waitFired(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("OnActivated was not called in time")
	}
}

// expectQuiet asserts OnActivated does NOT fire for the duration.
func expectQuiet(t *testing.T, ch <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("OnActivated fired unexpectedly")
	case <-time.After(d):
	}
}

func TestRegisterHappyPath(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{})
	fired := make(chan struct{}, 16)

	sess, err := Register(context.Background(), conn, Options{
		ShortcutID:       "toggle",
		Description:      "Toggle the search bar",
		PreferredTrigger: "ALT+space",
		OnActivated:      func() { fired <- struct{}{} },
	})
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// The full create -> list(empty) -> bind sequence ran, with the
	// documented wire payloads.
	require.Equal(t, 1, f.BindCalls())
	require.NotEmpty(t, sess.Handle())
	require.Equal(t, []string{sess.Handle()}, f.Sessions())
	require.Equal(t, sess.Handle(), f.LastListSession())
	require.Equal(t, sess.Handle(), f.LastBindSession())
	require.Equal(t, "", f.LastParent())
	bind := f.LastBind()
	require.Len(t, bind, 1)
	require.Equal(t, "toggle", bind[0].ID)
	require.Equal(t, "Toggle the search bar", stringOption(bind[0].Data, "description"))
	require.Equal(t, "ALT+space", stringOption(bind[0].Data, "preferred_trigger"))

	// The bind results' trigger_description is surfaced for logging.
	require.Equal(t, "Alt+Space", sess.BoundDescription)

	// One Activated = exactly one callback; a second fires again.
	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)
	expectQuiet(t, fired, 150*time.Millisecond)

	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)
}

func TestRegisterAlreadyBoundSkipsBind(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{
		listShortcuts: []shortcut{{
			ID:   "toggle",
			Data: map[string]dbus.Variant{"trigger_description": dbus.MakeVariant("Meta+Space")},
		}},
	})
	fired := make(chan struct{}, 16)

	sess, err := Register(context.Background(), conn, Options{
		ShortcutID:  "toggle",
		OnActivated: func() { fired <- struct{}{} },
	})
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// The portal already knew the shortcut: binding twice per session
	// is forbidden, so BindShortcuts must never have been called.
	require.Equal(t, 0, f.BindCalls())
	require.Equal(t, "Meta+Space", sess.BoundDescription)

	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)
}

func TestRegisterBindDenied(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{bindCode: 1})

	sess, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.Nil(t, sess)
	require.ErrorIs(t, err, ErrDenied)
	// The half-made session was closed on the way out.
	require.Equal(t, 1, f.SessionCloses())
}

func TestRegisterBindPortalError(t *testing.T) {
	_, conn := setupPortalTest(t, fakeConfig{bindCode: 2})

	_, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDenied)
	require.Contains(t, err.Error(), "BindShortcuts")
}

func TestRegisterCreateSessionFailure(t *testing.T) {
	_, conn := setupPortalTest(t, fakeConfig{createCode: 2})

	_, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "CreateSession")
}

func TestRegisterListShortcutsFailure(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{listCode: 2})

	_, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ListShortcuts")
	require.Equal(t, 1, f.SessionCloses())
}

func TestRegisterContextCancel(t *testing.T) {
	_, conn := setupPortalTest(t, fakeConfig{muteCreate: true})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := Register(ctx, conn, Options{ShortcutID: "toggle"})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 5*time.Second, "Register must return promptly on ctx cancel")
}

func TestRegisterFallsBackToReturnedRequestPath(t *testing.T) {
	// A pre-0.9 portal returns (and responds on) a request path that
	// differs from the prediction; the client re-subscribes on it.
	f, conn := setupPortalTest(t, fakeConfig{mangleRequest: true})
	fired := make(chan struct{}, 16)

	sess, err := Register(context.Background(), conn, Options{
		ShortcutID:  "toggle",
		OnActivated: func() { fired <- struct{}{} },
	})
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)
}

func TestActivatedFiltering(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{})
	fired := make(chan struct{}, 16)

	sess, err := Register(context.Background(), conn, Options{
		ShortcutID:  "toggle",
		OnActivated: func() { fired <- struct{}{} },
	})
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	// Wrong session, wrong shortcut id, and Deactivated must all be
	// ignored. D-Bus delivers in order per sender, so once the real
	// activation lands we know the earlier three were dropped.
	f.EmitActivated("/org/freedesktop/portal/desktop/session/9_99/other", "toggle")
	f.EmitActivated(sess.Handle(), "other-shortcut")
	f.EmitDeactivated(sess.Handle(), "toggle")
	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)
	expectQuiet(t, fired, 150*time.Millisecond)
}

func TestSessionCloseIdempotentAndSilencing(t *testing.T) {
	f, conn := setupPortalTest(t, fakeConfig{})
	fired := make(chan struct{}, 16)

	sess, err := Register(context.Background(), conn, Options{
		ShortcutID:  "toggle",
		OnActivated: func() { fired <- struct{}{} },
	})
	require.NoError(t, err)

	f.EmitActivated(sess.Handle(), "toggle")
	waitFired(t, fired)

	require.NoError(t, sess.Close())
	require.Equal(t, 1, f.SessionCloses())

	// The match is gone: further activations reach nobody.
	f.EmitActivated(sess.Handle(), "toggle")
	expectQuiet(t, fired, 200*time.Millisecond)

	// Idempotent: same result, no second portal-side close.
	require.NoError(t, sess.Close())
	require.Equal(t, 1, f.SessionCloses())
}

func TestSessionCloseAfterConnClose(t *testing.T) {
	_, conn := setupPortalTest(t, fakeConfig{})

	sess, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.NoError(t, err)

	// Closing the conn kills the dispatch channel; Session.Close stays
	// safe afterwards (the portal-side close fails, reported once).
	require.NoError(t, conn.Close())
	err = sess.Close()
	require.Error(t, err)
	// Still idempotent: the stored error comes back unchanged.
	require.Equal(t, err, sess.Close())
}

func TestRegisterRequiresShortcutID(t *testing.T) {
	_, err := Register(context.Background(), nil, Options{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ShortcutID")
}

func TestRegisterClosedConn(t *testing.T) {
	_, conn := setupPortalTest(t, fakeConfig{})
	require.NoError(t, conn.Close())

	_, err := Register(context.Background(), conn, Options{ShortcutID: "toggle"})
	require.Error(t, err)
}

func TestSessionMatches(t *testing.T) {
	s := &Session{handle: "/h", id: "toggle"}
	body := func(handle interface{}, id interface{}) []interface{} {
		return []interface{}{handle, id, uint64(1), map[string]dbus.Variant{}}
	}

	require.True(t, s.matches(&dbus.Signal{
		Name: signalActivated, Body: body(dbus.ObjectPath("/h"), "toggle"),
	}))
	require.False(t, s.matches(nil))
	require.False(t, s.matches(&dbus.Signal{
		Name: shortcutsIface + ".Deactivated", Body: body(dbus.ObjectPath("/h"), "toggle"),
	}))
	require.False(t, s.matches(&dbus.Signal{Name: signalActivated, Body: []interface{}{}}))
	require.False(t, s.matches(&dbus.Signal{
		Name: signalActivated, Body: body("/h", "toggle"), // handle not an ObjectPath
	}))
	require.False(t, s.matches(&dbus.Signal{
		Name: signalActivated, Body: body(dbus.ObjectPath("/h"), uint32(7)), // id not a string
	}))
	require.False(t, s.matches(&dbus.Signal{
		Name: signalActivated, Body: body(dbus.ObjectPath("/other"), "toggle"),
	}))
	require.False(t, s.matches(&dbus.Signal{
		Name: signalActivated, Body: body(dbus.ObjectPath("/h"), "other"),
	}))
}
