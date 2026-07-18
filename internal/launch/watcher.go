package launch

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// Watcher defaults: DefaultWatchDeadline is how long a launched
// window gets to appear before the existing-window fallback fires
// (covers slow cold starts without keeping a goroutine around
// forever); DefaultWatchInterval is the poll cadence (one X
// connection and a handful of property reads per poll).
const (
	DefaultWatchDeadline = 6 * time.Second
	DefaultWatchInterval = 200 * time.Millisecond
)

// XWindow is one window row of a watcher poll, as read from the X
// server by the native layer.
type XWindow struct {
	ID uint32
	// PID is _NET_WM_PID (0 when absent).
	PID int
	// Instance and Class are WM_CLASS's two fields.
	Instance string
	Class    string
	// StartupID is _NET_STARTUP_ID ("" when absent).
	StartupID string
	// UserTime is _NET_WM_USER_TIME (0 when absent) -- the MRU signal
	// for the existing-window fallback.
	UserTime uint32
}

// XState is one watcher poll: the client windows in stacking order
// (bottom to top, so later entries are closer to the top) and the
// active window id (0 when none).
type XState struct {
	Windows []XWindow
	Active  uint32
}

// Identity is what the watcher looks for: the spawned child's pid,
// the minted credential id (matched against _NET_STARTUP_ID), and the
// lowercased WM_CLASS hints (StartupWMClass, executable base names).
type Identity struct {
	PID       int
	StartupID string
	Hints     []string
}

// NewIdentity builds the watcher identity for one launch: pid is the
// spawned child (0 when the transport has none), cred the minted
// credential, and the class hints come from the handler's
// StartupWMClass, its executable's base name, and argv0's base name
// (all lowercased, deduplicated, blanks dropped).
func NewIdentity(pid int, cred Credential, h Handler, argv0 string) Identity {
	id := Identity{PID: pid, StartupID: cred.ID}
	seen := map[string]bool{}
	for _, hint := range []string{h.WMClass, filepath.Base(h.Exe), filepath.Base(argv0)} {
		hint = strings.ToLower(strings.TrimSpace(hint))
		if hint == "" || hint == "." || seen[hint] {
			continue
		}
		seen[hint] = true
		id.Hints = append(id.Hints, hint)
	}
	return id
}

// classMatches reports whether w's WM_CLASS matches any hint.
func classMatches(w XWindow, hints []string) bool {
	for _, hint := range hints {
		if strings.EqualFold(w.Class, hint) || strings.EqualFold(w.Instance, hint) {
			return true
		}
	}
	return false
}

// windowMatches reports whether w is the launched target: the spawned
// child's own window, a window stamped with our startup id, or a
// WM_CLASS match.
func windowMatches(w XWindow, id Identity) bool {
	if id.PID != 0 && w.PID == id.PID {
		return true
	}
	if id.StartupID != "" && w.StartupID == id.StartupID {
		return true
	}
	return classMatches(w, id.Hints)
}

// watchDecide is one poll's decision: done=true ends the watch --
// with activate=0 when the target is already the active window (the
// app raised itself; activating again would be redundant), or with
// the id of a NEW window (not in before) matching the identity, which
// the caller activates.
func watchDecide(st XState, id Identity, before map[uint32]bool) (activate uint32, done bool) {
	for _, w := range st.Windows {
		if w.ID == st.Active && st.Active != 0 && windowMatches(w, id) {
			return 0, true
		}
	}
	for i := len(st.Windows) - 1; i >= 0; i-- {
		w := st.Windows[i]
		if !before[w.ID] && windowMatches(w, id) {
			return w.ID, true
		}
	}
	return 0, false
}

// watchDeadline is the end-of-watch fallback: no new window appeared,
// so the launch handed off to an EXISTING instance (a tab in a
// running editor, a reveal in a running file manager). It picks the
// most-recently-used existing window matching the WM_CLASS hints --
// highest _NET_WM_USER_TIME, ties broken toward the top of the
// stacking order -- which is mechanically the window a taskbar click
// would raise. PID and startup-id identity do not apply here: an
// existing instance predates both.
func watchDeadline(st XState, id Identity) (uint32, bool) {
	var best uint32
	var bestTime uint32
	found := false
	for _, w := range st.Windows { // later = higher in the stack
		if !classMatches(w, id.Hints) {
			continue
		}
		if !found || w.UserTime >= bestTime {
			best, bestTime, found = w.ID, w.UserTime, true
		}
	}
	return best, found
}

// WatchConfig configures one raise watch.
type WatchConfig struct {
	// Identity is the launched target's expected identity.
	Identity Identity
	// Before holds the window ids that existed before the launch;
	// windows outside it are "new".
	Before map[uint32]bool
	// Deadline and Interval default to DefaultWatchDeadline and
	// DefaultWatchInterval when zero.
	Deadline time.Duration
	Interval time.Duration
	// Poll reads the current X state; ok=false aborts the watch (the
	// X server went away).
	Poll func() (XState, bool)
	// Activate raises one window (the timestamped _NET_ACTIVE_WINDOW
	// message in production).
	Activate func(id uint32) error
	// Logf receives the watcher's log lines (nil drops them).
	Logf func(format string, v ...interface{})
	// Verb and Target label the log lines ("open", "/tmp/x").
	Verb   string
	Target string
}

func (cfg *WatchConfig) logf(format string, v ...interface{}) {
	if cfg.Logf != nil {
		cfg.Logf(format, v...)
	}
}

// RunWatcher polls until the launched window is found (and, unless it
// already raised itself, activates it), the deadline passes (then the
// MRU existing-window fallback applies), or ctx is cancelled. It
// blocks; callers run it on a goroutine bounded by an app-lifetime
// context. Exactly one activation is ever issued.
func RunWatcher(ctx context.Context, cfg WatchConfig) {
	deadline := cfg.Deadline
	if deadline <= 0 {
		deadline = DefaultWatchDeadline
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultWatchInterval
	}
	expire := time.NewTimer(deadline)
	defer expire.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			st, ok := cfg.Poll()
			if !ok {
				return
			}
			id, found := watchDeadline(st, cfg.Identity)
			if !found {
				cfg.logf("launch: watcher: no window matched %s %s within %v (hints %v); giving up",
					cfg.Verb, cfg.Target, deadline, cfg.Identity.Hints)
				return
			}
			cfg.activate(id, "existing")
			return
		case <-tick.C:
			st, ok := cfg.Poll()
			if !ok {
				cfg.logf("launch: watcher: window list unavailable; giving up on %s %s", cfg.Verb, cfg.Target)
				return
			}
			id, done := watchDecide(st, cfg.Identity, cfg.Before)
			if !done {
				continue
			}
			if id != 0 {
				cfg.activate(id, "new")
			}
			return
		}
	}
}

// activate raises win and logs the outcome.
func (cfg *WatchConfig) activate(win uint32, kind string) {
	if cfg.Activate == nil {
		return
	}
	if err := cfg.Activate(win); err != nil {
		cfg.logf("launch: watcher: activating %s window 0x%x for %s %s: %v", kind, win, cfg.Verb, cfg.Target, err)
		return
	}
	cfg.logf("launch: watcher: raised %s window 0x%x for %s %s", kind, win, cfg.Verb, cfg.Target)
}
