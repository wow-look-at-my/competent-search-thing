package watch

import (
	"context"
	"errors"
	"sync"
)

// lifecycle is the shared Start/Stop plumbing for this package's two
// long-running loops (Watcher and Rescanner): an internal context that
// cancels the loop, a done channel the loop closes on exit, and
// idempotent stop semantics that are safe before, during, and after
// start.
type lifecycle struct {
	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// begin transitions to started and returns the loop's context. It fails
// if the loop already started or was stopped first. The caller must
// either launch a goroutine that closes done on exit, or close done
// itself when the launch fails, so end never blocks forever.
func (l *lifecycle) begin() (context.Context, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return nil, errors.New("watch: already started")
	}
	if l.stopped {
		return nil, errors.New("watch: already stopped")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	l.started = true
	return ctx, nil
}

// end cancels the loop's context and blocks until the loop exits.
// Calling it again, or before begin, is a safe no-op.
func (l *lifecycle) end() {
	l.mu.Lock()
	l.stopped = true
	cancel, done := l.cancel, l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// stopping reports whether end has been called (possibly before begin).
func (l *lifecycle) stopping() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopped
}
