package index

import (
	"context"
	"os"

	fstree "github.com/wow-look-at-my/go-fs-tree-fast"
)

// readDirFn is swapped by tests to inject deterministic read errors
// regardless of the uid the tests run as (chmod tricks do not bite
// when running as root).
var readDirFn = os.ReadDir

// ProgressFunc receives walk progress: the number of entries indexed so
// far, and done=true exactly once at the end with the final count. It
// is called from walker goroutines but never concurrently; it must be
// fast and must not call back into the walk or the Manager.
type ProgressFunc = fstree.ProgressFunc

// WalkStats summarizes one Walk.
type WalkStats = fstree.Stats

// Excluder decides which walked entries to skip. The watcher phase
// reuses it to filter fsnotify events with identical walk semantics.
type Excluder = fstree.Excluder

// NewExcluder validates the patterns and splits them by kind. Empty
// patterns are ignored; a malformed pattern (filepath.ErrBadPattern)
// is reported up front instead of silently never matching.
func NewExcluder(patterns []string) (*Excluder, error) {
	return fstree.NewExcluder(patterns)
}

// Walk fills st with everything under roots, in parallel (NumCPU
// workers over a shared directory queue). It must target a fresh store
// that nothing else is touching: writes are serialized internally, and
// per-directory name uniqueness (guaranteed by os.ReadDir) stands in
// for AddEntry's duplicate check.
//
// Behavior notes: exclude semantics are documented on Excluder;
// symlinks are indexed as plain entries and never descended; unreadable
// directories are counted in Errors and skipped, never fatal; roots are
// deduplicated (a root inside another root is skipped); cancellation of
// ctx stops the walk early and returns ctx.Err().
func Walk(ctx context.Context, st *Store, roots []string, excludes []string, progress ProgressFunc) (WalkStats, error) {
	return walk(ctx, st, roots, excludes, nil, progress)
}

// walk is Walk with an additional set of trusted exact full paths. It
// exists for mount-derived skips, whose legal Unix filenames may contain
// filepath.Match metacharacters and therefore must not enter the user glob
// pattern channel.
func walk(ctx context.Context, st *Store, roots []string, excludes, fullLiterals []string, progress ProgressFunc) (WalkStats, error) {
	return fstree.Walk(ctx, storeSink{st}, roots, fstree.Options{
		Excludes:     excludes,
		FullLiterals: fullLiterals,
		Progress:     progress,
		ReadDir:      readDirFn,
	})
}

// storeSink drains one directory's walk output into the store. fstree
// serializes the calls, so nothing here takes a lock, and the whole
// batch is drained in one pass while the walker's workers wait.
type storeSink struct{ st *Store }

func (s storeSink) AddDir(dir string, entries []fstree.Entry) {
	// internParentDir: the walk roots have no entry of their own, so
	// they must seed tombstoneSubtree's walk. Every non-root dir is
	// already interned by its parent's appendEntry, so this is a
	// plain map hit that leaves the entryless set alone.
	pid := s.st.internParentDir(dir)
	// One exact-size grow instead of the doubling append ladder: it
	// kills the copy churn and the measured cap overshoot.
	s.st.growChildren(pid, len(entries))
	for _, it := range entries {
		s.st.appendEntry(pid, it.Name, it.Full, it.IsDir)
	}
}
