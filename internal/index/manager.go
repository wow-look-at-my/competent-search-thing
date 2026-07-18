package index

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

// DefaultMaxResults is the query limit used when the caller passes a
// non-positive one.
const DefaultMaxResults = 50

// Manager owns the live Store plus the locking contract around it:
// queries take the read lock, mutations the write lock. It also holds
// the indexing knobs (roots, excludes, default result limit).
//
// The watcher phase drives Add/Remove for live filesystem events and
// watches TombstoneRatio to decide when a rebuild is worthwhile;
// BuildFromDisk doubles as the periodic full rescan.
type Manager struct {
	mu    sync.RWMutex
	store *Store

	roots         []string
	excludes      []string
	maxResults    int
	fuzzyDisabled bool
	blend         *Blend
}

// NewManager creates a Manager with an empty store. maxResults <= 0
// selects DefaultMaxResults.
func NewManager(roots, excludes []string, maxResults int) *Manager {
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	return &Manager{
		store:      NewStore(),
		roots:      copyStrings(roots),
		excludes:   copyStrings(excludes),
		maxResults: maxResults,
	}
}

// BuildFromDisk walks the configured roots into a FRESH store and then
// swaps it in under a short write lock, so queries keep answering from
// the previous store for the whole duration of the walk. On error
// (cancellation, bad exclude pattern) the old store is kept. Returns
// the number of entries indexed and the wall time spent.
//
// Every rebuild recomputes the mount-derived skip list (mounts change
// between rebuilds; see mounts.go) and appends it to the configured
// excludes as full-path patterns, so network and virtual filesystem
// mountpoints under the roots are pruned exactly like excludes. The
// skipped mountpoints never enter the index, so the watch layer never
// watches them either.
func (m *Manager) BuildFromDisk(ctx context.Context, progress ProgressFunc) (int, time.Duration, error) {
	start := time.Now()
	fresh := NewStore()
	excludes := m.excludes
	if skips := mountSkips(m.roots); len(skips) > 0 {
		log.Printf("index: skipping %d mounted filesystems (network/virtual): %s",
			len(skips), strings.Join(skips, ", "))
		excludes = append(copyStrings(m.excludes), skips...)
	}
	stats, err := Walk(ctx, fresh, m.roots, excludes, progress)
	if err != nil {
		return 0, time.Since(start), err
	}
	m.mu.Lock()
	m.store = fresh
	m.mu.Unlock()
	return stats.Indexed, time.Since(start), nil
}

// SetFuzzyDisabled turns the fuzzy (subsequence) name-match tier off
// or on for subsequent queries. The zero value keeps fuzzy matching on
// (config search.fuzzyDisabled; main.go wires it right after
// construction).
func (m *Manager) SetFuzzyDisabled(disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fuzzyDisabled = disabled
}

// SetBlend swaps the frecency ranking blend subsequent queries use
// (see blend.go). The Blend is treated as immutable from here on --
// callers hand over a fresh copy to change anything (the focused-app
// cwd changes per summon). Nil, or an inactive blend, keeps the
// ranking byte-identical to the pre-blend engine.
func (m *Manager) SetBlend(b *Blend) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blend = b
}

// Blend returns the blend subsequent queries use (diagnostics and
// tests); nil when none was set.
func (m *Manager) Blend() *Blend {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blend
}

// Query searches the live store. limit <= 0 selects the configured
// default. Returns nil when nothing matches.
func (m *Manager) Query(q string, limit int) []Result {
	if limit <= 0 {
		limit = m.maxResults
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.QueryWith(q, limit, QueryOptions{FuzzyDisabled: m.fuzzyDisabled, Blend: m.blend})
}

// Add inserts or refreshes one entry (watcher pass-through).
func (m *Manager) Add(parentDir, name string, isDir bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.store.AddEntry(parentDir, name, isDir)
	return err
}

// Remove tombstones the entry (and, for a directory, its subtree) at
// path (watcher pass-through). Returns the number of entries removed.
func (m *Manager) Remove(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.RemoveByPath(path)
}

// Len returns the total entry count including tombstones.
func (m *Manager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.Len()
}

// LiveCount returns the live entry count.
func (m *Manager) LiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.LiveCount()
}

// TombstoneRatio returns the fraction of entries that are tombstoned
// (0 when the store is empty). A high ratio means a rebuild would
// reclaim noticeable memory and scan time.
func (m *Manager) TombstoneRatio() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total := m.store.Len()
	if total == 0 {
		return 0
	}
	return float64(total-m.store.LiveCount()) / float64(total)
}

// ForEachLiveDir calls fn, under the read lock, with the absolute path
// of every live directory entry until fn returns false. The watcher
// uses it to enumerate the directories that need an fsnotify watch. fn
// must be fast and must not call back into the Manager (the read lock
// is held for the whole iteration).
func (m *Manager) ForEachLiveDir(fn func(path string) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.store.ForEachLive(func(id int32) bool {
		if !m.store.IsDir(id) {
			return true
		}
		return fn(m.store.EntryPath(id))
	})
}

// Footprint returns the live store's memory accounting under the read
// lock (diagnostics; see Store.Footprint).
func (m *Manager) Footprint() Footprint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.Footprint()
}

// Roots returns a copy of the configured walk roots.
func (m *Manager) Roots() []string { return copyStrings(m.roots) }

// Excludes returns a copy of the configured exclude patterns.
func (m *Manager) Excludes() []string { return copyStrings(m.excludes) }

// MaxResults returns the default query limit.
func (m *Manager) MaxResults() int { return m.maxResults }

func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}
