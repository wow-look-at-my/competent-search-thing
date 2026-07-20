//go:build linux

package watch

// The linux production budget seams (see resolveBudget): the raw
// input is the kernel's per-user inotify watch allowance, and the
// auto formula takes half of it (capped at 65536, floor 1024) so one
// app never hogs the whole per-user budget.
func defaultReadMaxWatches() int    { return readInotifyMaxWatches() }
func defaultAutoBudget(raw int) int { return autoBudgetInotify(raw) }
