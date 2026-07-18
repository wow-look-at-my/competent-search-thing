//go:build !linux

package watch

// newAutoNotifier has no whole-filesystem backend off linux: the
// per-directory fsnotify notifier is the only production choice
// (watch-everything semantics on darwin/windows, unchanged).
func newAutoNotifier([]string) func() (notifier, error) {
	return func() (notifier, error) { return newFSNotifier() }
}
