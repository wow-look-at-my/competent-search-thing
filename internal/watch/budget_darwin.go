//go:build darwin

package watch

// The darwin production budget seams (see resolveBudget): the raw
// input is the process's soft RLIMIT_NOFILE (already raised to the
// kern.maxfilesperproc cap by the Go runtime at init; readFDLimit is
// deliberately read-only), and the auto formula is far more
// conservative than linux's because fsnotify's kqueue backend opens
// one fd per watched directory PLUS one per direct child file --
// autoBudgetDarwinFD's rationale. The unbudgeted pre-fix behavior
// pinned real machines at their fd ceiling (17,704 watched dirs ate
// the whole raised limit and every later open()/exec failed); the
// budget bounds the per-directory FALLBACK, while auto normally
// selects the FSEvents backend and never fills a hot set at all.
func defaultReadMaxWatches() int    { return readFDLimit() }
func defaultAutoBudget(raw int) int { return autoBudgetDarwinFD(raw) }
