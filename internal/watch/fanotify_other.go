//go:build !linux && !darwin

package watch

import "log"

// newAutoNotifier has no whole-filesystem backend off linux/darwin
// (fanotify is linux-only, FSEvents darwin-only): the per-directory
// fsnotify notifier is the only production choice on windows and the
// BSDs, unchanged.
func newAutoNotifier([]string) func() (notifier, error) {
	return func() (notifier, error) { return newFSNotifier() }
}

// newStrictFanotifyNotifier off linux: fanotify does not exist here,
// so the strict watcher.backend="fanotify" mode always resolves to the
// no-op "none" notifier -- announced loudly, never a silent fsnotify
// fallback; the sweep tier keeps the index converging.
func newStrictFanotifyNotifier([]string) func() (notifier, error) {
	return func() (notifier, error) {
		log.Printf("watch: backend \"fanotify\" required by config but unavailable (fanotify is linux-only); live watching DISABLED, sweeps keep the index converging")
		return newNoopNotifier(), nil
	}
}
