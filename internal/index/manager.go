package index

import (
	"context"
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

	roots      []string
	excludes   []string
	maxResults int
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
func (m *Manager) BuildFromDisk(ctx context.Context, progress ProgressFunc) (int, time.Duration, error) {
	start := time.Now()
	fresh := NewStore()
	stats, err := Walk(ctx, fresh, m.roots, m.excludes, progress)
	if err != nil {
		return 0, time.Since(start), err
	}
	m.mu.Lock()
	m.store = fresh
	m.mu.Unlock()
	return stats.Indexed, time.Since(start), nil
}

// Query searches the live store. limit <= 0 selects the configured
// default. Returns nil when nothing matches.
func (m *Manager) Query(q string, limit int) []Result {
	if limit <= 0 {
		limit = m.maxResults
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.Query(q, limit)
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
