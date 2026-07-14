package index

import "sync"

// walkQueue is the unbounded work queue behind the parallel walker.
// pending counts tasks that are queued or currently being processed;
// when it reaches zero the walk is complete and all poppers drain.
// stop() aborts the walk early (context cancellation).
type walkQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []string
	pending int
	stopped bool
}

func newWalkQueue() *walkQueue {
	q := &walkQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push enqueues directory paths. Each pushed path must eventually be
// balanced by one taskDone call from the worker that pops it.
func (q *walkQueue) push(paths ...string) {
	if len(paths) == 0 {
		return
	}
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.pending += len(paths)
	q.items = append(q.items, paths...)
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop blocks until a task is available, the walk completes, or the
// queue is stopped. It returns false when the caller should exit.
// Tasks are handed out LIFO (depth-first-ish) to keep the queue small
// and directory locality high.
func (q *walkQueue) pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.pending > 0 && !q.stopped {
		q.cond.Wait()
	}
	if q.stopped || len(q.items) == 0 {
		return "", false
	}
	it := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return it, true
}

// taskDone marks one popped task fully processed (its children already
// pushed). The push-children-then-taskDone order matters: it keeps
// pending from touching zero while work is still being generated.
func (q *walkQueue) taskDone() {
	q.mu.Lock()
	q.pending--
	done := q.pending == 0
	q.mu.Unlock()
	if done {
		q.cond.Broadcast()
	}
}

// stop aborts the walk: current and future pops return false.
func (q *walkQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	q.mu.Unlock()
	q.cond.Broadcast()
}
