package watch

import "path/filepath"

// setMountSkips replaces the live set of unsafe mount roots. The
// Sweeper refreshes it before applying each mount-table diff; New
// seeds it so event handling is safe before the first sweep.
func (w *Watcher) setMountSkips(paths []string) {
	next := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		if path != string(filepath.Separator) {
			next[path] = struct{}{}
		}
	}
	w.mountMu.Lock()
	w.mountSkips = next
	w.mountMu.Unlock()
}

// mountSkipRoot returns the unsafe mount root containing path. Walking
// ancestors makes one skipped directory cover its whole subtree,
// including queued child events from watches that are being removed.
func (w *Watcher) mountSkipRoot(path string) (string, bool) {
	path = filepath.Clean(path)
	w.mountMu.RLock()
	defer w.mountMu.RUnlock()
	for p := path; ; {
		if _, ok := w.mountSkips[p]; ok {
			return p, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false
		}
		p = parent
	}
}

func (w *Watcher) mountSkipped(path string) bool {
	_, skipped := w.mountSkipRoot(path)
	return skipped
}
