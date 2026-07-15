package plugin

import (
	"sync"
	"time"
)

// logThrottleWindow is the minimum gap between log lines for one key
// (one provider): a plugin failing on every keystroke logs once per
// window instead of once per keystroke.
const logThrottleWindow = 5 * time.Second

// logThrottle rate-limits log output per key. Safe for concurrent use.
type logThrottle struct {
	out func(format string, args ...any)
	min time.Duration
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// newLogThrottle builds a throttle writing through out. now is
// injectable for tests (nil means time.Now).
func newLogThrottle(out func(format string, args ...any), min time.Duration, now func() time.Time) *logThrottle {
	if now == nil {
		now = time.Now
	}
	return &logThrottle{out: out, min: min, now: now, last: map[string]time.Time{}}
}

// logf logs unless key already logged within the window.
func (t *logThrottle) logf(key, format string, args ...any) {
	t.mu.Lock()
	n := t.now()
	if last, ok := t.last[key]; ok && n.Sub(last) < t.min {
		t.mu.Unlock()
		return
	}
	t.last[key] = n
	t.mu.Unlock()
	t.out(format, args...)
}
