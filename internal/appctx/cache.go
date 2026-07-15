package appctx

import (
	"sync"
	"time"
)

// Cache holds goroutine-safe app-context snapshots over a Source so
// the hotkey path never blocks on the OS: the focused app is captured
// synchronously (it must be read BEFORE the bar window steals focus),
// while the running/installed lists refresh on background goroutines
// with single-flight deduplication.
//
// The zero value and a nil Source are both safe: every method no-ops
// and Snapshot returns empty context (the app runs degraded).
type Cache struct {
	src Source
	now func() time.Time // injectable clock for tests; nil = time.Now

	mu                sync.Mutex
	focused           *AppInfo
	running           []AppInfo
	installed         []InstalledApp
	installedAt       time.Time // last SUCCESSFUL installed refresh
	runningInFlight   bool
	installedInFlight bool
}

// NewCache wraps src (which may be nil for a degraded no-op cache).
func NewCache(src Source) *Cache {
	return &Cache{src: src, now: time.Now}
}

// clock reads the injected clock, defaulting to time.Now so even a
// zero-value Cache cannot panic.
func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// CaptureFocused synchronously asks the Source for the focused app
// and stores the result, clearing any previous capture when the
// Source reports ok=false. The app calls this at hotkey-press, before
// showing the window; it does nothing else so that path stays fast.
func (c *Cache) CaptureFocused() {
	if c == nil || c.src == nil {
		return
	}
	info, ok := c.src.FocusedApp()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !ok {
		c.focused = nil
		return
	}
	c.focused = &info
}

// RefreshRunningAsync refreshes the running-apps list on a background
// goroutine. It never blocks the caller, and while a refresh is
// already in flight further calls are dropped (single-flight). A
// failed refresh keeps the previous list.
func (c *Cache) RefreshRunningAsync() {
	if c == nil || c.src == nil {
		return
	}
	c.mu.Lock()
	if c.runningInFlight {
		c.mu.Unlock()
		return
	}
	c.runningInFlight = true
	c.mu.Unlock()

	go func() {
		apps, ok := c.src.RunningApps()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.runningInFlight = false
		if ok {
			c.running = apps
		}
	}()
}

// RefreshInstalledAsync refreshes the installed-apps list on a
// background goroutine, with the same single-flight and
// keep-old-data-on-failure behavior as RefreshRunningAsync. Only a
// successful refresh advances the freshness timestamp consulted by
// EnsureFreshInstalled, so failures are retried on the next call.
func (c *Cache) RefreshInstalledAsync() {
	if c == nil || c.src == nil {
		return
	}
	c.mu.Lock()
	if c.installedInFlight {
		c.mu.Unlock()
		return
	}
	c.installedInFlight = true
	c.mu.Unlock()

	go func() {
		apps, ok := c.src.InstalledApps()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.installedInFlight = false
		if ok {
			c.installed = apps
			c.installedAt = c.clock()
		}
	}()
}

// EnsureFreshInstalled kicks RefreshInstalledAsync unless the last
// successful installed refresh is younger than ttl. The app calls it
// at summon (with a TTL of a few minutes) so the list stays roughly
// current without rescanning on every hotkey press.
func (c *Cache) EnsureFreshInstalled(ttl time.Duration) {
	if c == nil || c.src == nil {
		return
	}
	c.mu.Lock()
	fresh := !c.installedAt.IsZero() && c.clock().Sub(c.installedAt) < ttl
	c.mu.Unlock()
	if fresh {
		return
	}
	c.RefreshInstalledAsync()
}

// Snapshot returns an immutable copy of the current context: the
// slices and the focused struct are copies, so callers may mutate
// them freely without affecting the cache (and vice versa).
func (c *Cache) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var s Snapshot
	if c.focused != nil {
		f := *c.focused
		s.Focused = &f
	}
	if len(c.running) > 0 {
		s.Running = append([]AppInfo(nil), c.running...)
	}
	if len(c.installed) > 0 {
		s.Installed = append([]InstalledApp(nil), c.installed...)
	}
	return s
}
