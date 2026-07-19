//go:build !linux && !darwin

package watch

// No readable watch or fd limit off linux/darwin: the raw input is
// unknown (0), so the auto budget resolves to unlimited -- the
// pre-budget watch-everything behavior, unchanged. The formula
// binding is moot at raw 0 but keeps the seam total.
func defaultReadMaxWatches() int    { return 0 }
func defaultAutoBudget(raw int) int { return autoBudgetInotify(raw) }
