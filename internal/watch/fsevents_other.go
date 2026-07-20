//go:build !darwin

package watch

import "log"

// newStrictFSEventsNotifier off darwin: FSEvents does not exist here,
// so the strict watcher.backend="fsevents" mode always resolves to
// the no-op "none" notifier -- announced loudly, never a silent
// per-directory fallback (the fanotify_other.go stance for the
// mirror-image config); the sweep tier keeps the index converging.
func newStrictFSEventsNotifier([]string) func() (notifier, error) {
	return func() (notifier, error) {
		log.Printf("watch: backend \"fsevents\" required by config but unavailable (fsevents is macOS-only); live watching DISABLED, sweeps keep the index converging")
		return newNoopNotifier(), nil
	}
}
