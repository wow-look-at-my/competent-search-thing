package launch

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewIdentity(t *testing.T) {
	h := Handler{WMClass: "Code", Exe: "/usr/share/code/code"}
	id := NewIdentity(42, Credential{ID: "tok"}, h, "/usr/bin/code")
	require.Equal(t, 42, id.PID)
	require.Equal(t, "tok", id.StartupID)
	require.Equal(t, []string{"code"}, id.Hints, "wmclass, exe base and argv0 base dedupe case-insensitively")

	id = NewIdentity(0, Credential{}, Handler{WMClass: "Navigator", Exe: "/usr/lib/firefox/firefox"}, "")
	require.Equal(t, []string{"navigator", "firefox"}, id.Hints)
	require.Empty(t, NewIdentity(0, Credential{}, Handler{}, "").Hints, "no hints from an empty handler")
}

func TestWindowMatches(t *testing.T) {
	id := Identity{PID: 7, StartupID: "sid-1", Hints: []string{"code"}}
	tests := []struct {
		name string
		w    XWindow
		want bool
	}{
		{name: "pid", w: XWindow{ID: 1, PID: 7}, want: true},
		{name: "startup id", w: XWindow{ID: 2, StartupID: "sid-1"}, want: true},
		{name: "class field", w: XWindow{ID: 3, Class: "Code"}, want: true},
		{name: "instance field", w: XWindow{ID: 4, Instance: "code"}, want: true},
		{name: "nothing", w: XWindow{ID: 5, PID: 9, Class: "Gedit", StartupID: "other"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, windowMatches(tt.w, id))
		})
	}
	require.False(t, windowMatches(XWindow{PID: 0}, Identity{PID: 0, Hints: []string{"x"}}),
		"zero pids never match each other")
	require.False(t, windowMatches(XWindow{StartupID: ""}, Identity{StartupID: ""}),
		"empty startup ids never match each other")
}

func TestWatchDecide(t *testing.T) {
	id := Identity{PID: 7, StartupID: "sid", Hints: []string{"code"}}
	before := map[uint32]bool{10: true, 11: true}
	barAnd := func(extra ...XWindow) XState {
		st := XState{
			Windows: []XWindow{
				{ID: 10, Instance: "competent-search-thing", PID: 1},
				{ID: 11, Class: "Gedit", PID: 2},
			},
			Active: 10, // our own bar is focused during the launch
		}
		st.Windows = append(st.Windows, extra...)
		return st
	}

	t.Run("nothing yet", func(t *testing.T) {
		act, done := watchDecide(barAnd(), id, before)
		require.False(t, done)
		require.Zero(t, act)
	})
	t.Run("new matching window", func(t *testing.T) {
		act, done := watchDecide(barAnd(XWindow{ID: 20, Class: "Code"}), id, before)
		require.True(t, done)
		require.Equal(t, uint32(20), act)
	})
	t.Run("new window by pid", func(t *testing.T) {
		act, done := watchDecide(barAnd(XWindow{ID: 21, PID: 7}), id, before)
		require.True(t, done)
		require.Equal(t, uint32(21), act)
	})
	t.Run("new window by startup id", func(t *testing.T) {
		act, done := watchDecide(barAnd(XWindow{ID: 22, StartupID: "sid"}), id, before)
		require.True(t, done)
		require.Equal(t, uint32(22), act)
	})
	t.Run("new non-matching window ignored", func(t *testing.T) {
		_, done := watchDecide(barAnd(XWindow{ID: 23, Class: "Other", PID: 99}), id, before)
		require.False(t, done)
	})
	t.Run("self-raised target ends silently", func(t *testing.T) {
		st := barAnd(XWindow{ID: 24, Class: "Code"})
		st.Active = 24
		act, done := watchDecide(st, id, before)
		require.True(t, done)
		require.Zero(t, act, "an already-active target must not be re-activated")
	})
	t.Run("active EXISTING window matching by class ends silently", func(t *testing.T) {
		st := XState{Windows: []XWindow{{ID: 11, Class: "Code"}}, Active: 11}
		act, done := watchDecide(st, id, before)
		require.True(t, done)
		require.Zero(t, act)
	})
	t.Run("topmost new match wins", func(t *testing.T) {
		st := barAnd(XWindow{ID: 30, Class: "Code"}, XWindow{ID: 31, Class: "Code"})
		act, done := watchDecide(st, id, before)
		require.True(t, done)
		require.Equal(t, uint32(31), act, "windows arrive bottom-to-top; the later one is closer to the top")
	})
}

func TestWatchDeadline(t *testing.T) {
	id := Identity{PID: 7, StartupID: "sid", Hints: []string{"code"}}
	t.Run("no class match", func(t *testing.T) {
		st := XState{Windows: []XWindow{{ID: 1, Class: "Gedit"}, {ID: 2, PID: 7}}}
		_, found := watchDeadline(st, id)
		require.False(t, found, "pid and startup id do not apply to existing windows")
	})
	t.Run("single match", func(t *testing.T) {
		st := XState{Windows: []XWindow{{ID: 1, Class: "Gedit"}, {ID: 2, Class: "Code"}}}
		win, found := watchDeadline(st, id)
		require.True(t, found)
		require.Equal(t, uint32(2), win)
	})
	t.Run("mru by user time", func(t *testing.T) {
		st := XState{Windows: []XWindow{
			{ID: 1, Class: "Code", UserTime: 900},
			{ID: 2, Class: "Code", UserTime: 100},
		}}
		win, found := watchDeadline(st, id)
		require.True(t, found)
		require.Equal(t, uint32(1), win, "highest _NET_WM_USER_TIME wins")
	})
	t.Run("user-time tie prefers the topmost", func(t *testing.T) {
		st := XState{Windows: []XWindow{
			{ID: 1, Class: "Code"},
			{ID: 2, Class: "Code"},
		}}
		win, found := watchDeadline(st, id)
		require.True(t, found)
		require.Equal(t, uint32(2), win, "later in stacking order = closer to the top")
	})
}

// scriptedPoll serves a sequence of states, repeating the last one.
type scriptedPoll struct {
	mu     sync.Mutex
	states []XState
	ok     bool
	calls  int
}

func (s *scriptedPoll) poll() (XState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.states) == 0 {
		return XState{}, s.ok
	}
	st := s.states[0]
	if len(s.states) > 1 {
		s.states = s.states[1:]
	}
	return st, s.ok
}

// watchRecorder captures activations and log lines.
type watchRecorder struct {
	mu        sync.Mutex
	activated []uint32
	logs      []string
	actErr    error
}

func (r *watchRecorder) activate(id uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activated = append(r.activated, id)
	return r.actErr
}

func (r *watchRecorder) logf(format string, v ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, v...))
}

func (r *watchRecorder) acts() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.activated...)
}

func (r *watchRecorder) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.logs...)
}

func testWatchConfig(p *scriptedPoll, r *watchRecorder) WatchConfig {
	return WatchConfig{
		Identity: Identity{Hints: []string{"code"}},
		Before:   map[uint32]bool{1: true},
		Deadline: 80 * time.Millisecond,
		Interval: 5 * time.Millisecond,
		Poll:     p.poll,
		Activate: r.activate,
		Logf:     r.logf,
		Verb:     "open",
		Target:   "/tmp/x",
	}
}

func TestRunWatcherActivatesNewWindow(t *testing.T) {
	p := &scriptedPoll{ok: true, states: []XState{
		{Windows: []XWindow{{ID: 1, Instance: "bar"}}, Active: 1},
		{Windows: []XWindow{{ID: 1, Instance: "bar"}, {ID: 5, Class: "Code"}}, Active: 1},
	}}
	r := &watchRecorder{}
	RunWatcher(context.Background(), testWatchConfig(p, r))
	require.Equal(t, []uint32{5}, r.acts())
	require.Len(t, r.logged(), 1)
	require.Contains(t, r.logged()[0], "raised new window 0x5")
}

func TestRunWatcherSelfRaiseIsSilent(t *testing.T) {
	p := &scriptedPoll{ok: true, states: []XState{
		{Windows: []XWindow{{ID: 1, Instance: "bar"}, {ID: 5, Class: "Code"}}, Active: 5},
	}}
	r := &watchRecorder{}
	RunWatcher(context.Background(), testWatchConfig(p, r))
	require.Empty(t, r.acts(), "an already-active target is never re-activated")
	require.Empty(t, r.logged())
}

func TestRunWatcherDeadlineFallsBackToExisting(t *testing.T) {
	// The pre-existing window 1 IS the running editor; nothing new ever
	// appears (the launch opened a tab in it).
	p := &scriptedPoll{ok: true, states: []XState{
		{Windows: []XWindow{{ID: 1, Class: "Code"}}, Active: 2},
	}}
	r := &watchRecorder{}
	cfg := testWatchConfig(p, r)
	cfg.Deadline = 30 * time.Millisecond
	RunWatcher(context.Background(), cfg)
	require.Equal(t, []uint32{1}, r.acts(), "the MRU existing WM_CLASS match is raised at the deadline")
	require.Contains(t, r.logged()[len(r.logged())-1], "raised existing window 0x1")
}

func TestRunWatcherDeadlineGivesUpQuietly(t *testing.T) {
	p := &scriptedPoll{ok: true, states: []XState{
		{Windows: []XWindow{{ID: 1, Class: "Gedit"}}, Active: 0},
	}}
	r := &watchRecorder{}
	cfg := testWatchConfig(p, r)
	cfg.Deadline = 25 * time.Millisecond
	RunWatcher(context.Background(), cfg)
	require.Empty(t, r.acts())
	logs := r.logged()
	require.NotEmpty(t, logs)
	require.Contains(t, logs[len(logs)-1], "giving up")
}

func TestRunWatcherContextCancel(t *testing.T) {
	p := &scriptedPoll{ok: true, states: []XState{{}}}
	r := &watchRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := testWatchConfig(p, r)
	cfg.Deadline = time.Hour // only the ctx can end it
	done := make(chan struct{})
	go func() { RunWatcher(ctx, cfg); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunWatcher did not honor ctx cancellation")
	}
	require.Empty(t, r.acts())
}

func TestRunWatcherPollFailureAborts(t *testing.T) {
	p := &scriptedPoll{ok: false}
	r := &watchRecorder{}
	RunWatcher(context.Background(), testWatchConfig(p, r))
	require.Empty(t, r.acts())
	require.NotEmpty(t, r.logged())
	require.Contains(t, r.logged()[0], "window list unavailable")
}

func TestRunWatcherActivateErrorIsLogged(t *testing.T) {
	p := &scriptedPoll{ok: true, states: []XState{
		{Windows: []XWindow{{ID: 5, Class: "Code"}}, Active: 0},
	}}
	r := &watchRecorder{actErr: fmt.Errorf("boom")}
	RunWatcher(context.Background(), testWatchConfig(p, r))
	require.Equal(t, []uint32{5}, r.acts())
	require.Contains(t, r.logged()[0], "boom")
}

func TestRunWatcherDefaultsApplied(t *testing.T) {
	// Zero Deadline/Interval must fall back to the defaults rather than
	// spinning; prove it by cancelling promptly and asserting no panic.
	p := &scriptedPoll{ok: true, states: []XState{{}}}
	r := &watchRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	cfg := testWatchConfig(p, r)
	cfg.Deadline, cfg.Interval = 0, 0
	RunWatcher(ctx, cfg)
	require.Empty(t, r.acts())
}

func TestWatchConfigNilCallbacksAreSafe(t *testing.T) {
	cfg := WatchConfig{}
	cfg.logf("dropped %d", 1) // nil Logf must not panic
	cfg.activate(3, "new")    // nil Activate must not panic
}
