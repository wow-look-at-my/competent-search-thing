package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// readDirFn is swapped by tests to inject deterministic read errors
// regardless of the uid the tests run as (chmod tricks do not bite
// when running as root).
var readDirFn = os.ReadDir

// progressInterval throttles walk progress callbacks.
const progressInterval = 50 * time.Millisecond

// ProgressFunc receives walk progress: the number of entries indexed so
// far, and done=true exactly once at the end with the final count. It
// is called from walker goroutines but never concurrently; it must be
// fast and must not call back into the walk or the Manager.
type ProgressFunc func(indexed int, done bool)

// WalkStats summarizes one Walk.
type WalkStats struct {
	Indexed      int // entries added to the store
	Dirs         int // directories successfully read
	Errors       int // directories that failed to read (permissions etc.)
	SkippedRoots int // roots dropped as duplicates/overlaps/unresolvable
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
	var stats WalkStats
	ex, err := NewExcluder(excludes)
	if err != nil {
		return stats, err
	}
	ex.addFullLiterals(fullLiterals)
	kept, skipped := normalizeRoots(roots)
	stats.SkippedRoots = skipped

	w := &walkState{ctx: ctx, st: st, ex: ex, q: newWalkQueue(), prog: progressReporter{fn: progress}}
	w.q.push(kept...)
	stopWatch := context.AfterFunc(ctx, w.q.stop)
	defer stopWatch()

	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.run()
		}()
	}
	wg.Wait()
	w.prog.finish()

	stats.Indexed = int(w.indexed.Load())
	stats.Dirs = int(w.dirs.Load())
	stats.Errors = int(w.errs.Load())
	return stats, ctx.Err()
}

// walkState is the shared state of one Walk call.
type walkState struct {
	ctx  context.Context
	st   *Store
	ex   *Excluder
	q    *walkQueue
	mu   sync.Mutex // serializes writes to st
	prog progressReporter

	indexed atomic.Int64
	dirs    atomic.Int64
	errs    atomic.Int64
}

// walkBufs holds one worker's reusable scratch space. Every field is
// refilled from scratch per directory and never escapes processDir, so
// the walker allocates per WORKER instead of per directory -- at
// whole-filesystem scale that is the difference between a handful of
// buffers and three allocations for each of a million directories.
type walkBufs struct {
	// path is the joined full-path buffer for file entries: they need
	// one only for the full-pattern exclude check, and materializing a
	// real string per entry was the walk's single largest allocation
	// source (~90-100 transient bytes per entry).
	path []byte
	// batch holds the directory's entries while the store lock is not
	// held; drained inside the lock and reset before returning.
	batch []walkItem
	// subdirs holds the child directories to enqueue. walkQueue.push
	// copies its argument into the queue's own slice, so reusing this
	// across directories is safe.
	subdirs []string
}

func (w *walkState) run() {
	var bufs walkBufs
	for {
		dir, ok := w.q.pop()
		if !ok {
			return
		}
		w.processDir(dir, &bufs)
		w.q.taskDone()
	}
}

// walkItem is one directory entry captured outside the store lock.
// full is the entry's absolute path, set for directories only -- the
// walker builds it once for the queue and appendEntry interns the
// same string instead of re-joining it.
type walkItem struct {
	name  string
	full  string
	isDir bool
}

func (w *walkState) processDir(dir string, bufs *walkBufs) {
	entries, err := readDirFn(dir)
	if err != nil {
		w.errs.Add(1)
		return
	}
	w.dirs.Add(1)

	checkFull := w.ex.HasFullPatterns()
	batch := bufs.batch[:0]
	subdirs := bufs.subdirs[:0]
	for _, de := range entries {
		name := de.Name()
		if w.ex.MatchBase(name) {
			continue // pruned: a matching directory is never descended
		}
		// DirEntry.IsDir is false for symlinks (even to directories),
		// so the link itself is indexed but never descended.
		if de.IsDir() {
			// Directories materialize the real string: the queue and
			// the store's dir table both keep it anyway.
			full := joinDir(dir, name)
			if checkFull && w.ex.MatchFull(full) {
				continue
			}
			batch = append(batch, walkItem{name: name, full: full, isDir: true})
			subdirs = append(subdirs, full)
			continue
		}
		if checkFull {
			// Files: join into the reusable buffer and hand
			// filepath.Match a transient view -- nothing down that
			// call retains or mutates it, so no per-entry string is
			// allocated.
			bufs.path = appendJoinDir(bufs.path[:0], dir, name)
			if w.ex.MatchFull(unsafeString(bufs.path)) {
				continue
			}
		}
		batch = append(batch, walkItem{name: name})
	}

	if len(batch) > 0 {
		w.mu.Lock()
		// internParentDir: the walk roots have no entry of their own, so
		// they must seed tombstoneSubtree's walk. Every non-root dir is
		// already interned by its parent's appendEntry, so this is a
		// plain map hit that leaves the entryless set alone.
		pid := w.st.internParentDir(dir)
		// One exact-size grow instead of the 1->2->4 append ladder:
		// kills the copy churn and the ~1.32x measured cap overshoot.
		w.st.growChildren(pid, len(batch))
		for _, it := range batch {
			w.st.appendEntry(pid, it.name, it.full, it.isDir)
		}
		w.mu.Unlock()
		w.prog.add(len(batch))
		w.indexed.Add(int64(len(batch)))
	}
	// push COPIES into the queue's own slice, so subdirs is free to be
	// reused right after.
	w.q.push(subdirs...)
	bufs.keep(batch, subdirs)
}

// walkBufMaxEntries bounds what a worker carries between directories.
// One pathological directory (a build cache with a million siblings)
// would otherwise leave every worker pinning a batch that large for the
// rest of the walk; past this size the buffer is handed back to the GC
// and the next directory starts fresh.
const walkBufMaxEntries = 8192

// keep returns the (possibly regrown) buffers to the worker for the
// next directory. The written elements are zeroed so the buffer never
// pins the previous directory's name strings -- which also maintains
// the invariant that everything past len is already zero.
func (b *walkBufs) keep(batch []walkItem, subdirs []string) {
	clear(batch)
	clear(subdirs)
	if cap(batch) > walkBufMaxEntries {
		batch = nil
	}
	if cap(subdirs) > walkBufMaxEntries {
		subdirs = nil
	}
	b.batch, b.subdirs = batch[:0], subdirs[:0]
}

// appendJoinDir appends joinDir(dir, name) to buf without allocating
// a string (same separator rule: only a filesystem root keeps a
// trailing separator after filepath.Clean).
func appendJoinDir(buf []byte, dir, name string) []byte {
	buf = append(buf, dir...)
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		buf = append(buf, filepath.Separator)
	}
	return append(buf, name...)
}

// unsafeString views b as a string for the duration of a call that
// neither retains nor mutates it (here: filepath.Match inside
// Excluder.MatchFull). The walker's scratch buffer is reused per
// worker, so a plain string(b) conversion would allocate once per
// file entry -- the exact churn this path exists to remove. Callers
// must not let the view escape.
func unsafeString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// progressReporter throttles ProgressFunc invocations to at most one
// per progressInterval (plus the final done call), serialized under its
// own mutex.
type progressReporter struct {
	mu    sync.Mutex
	fn    ProgressFunc
	last  time.Time
	count int
}

func (p *progressReporter) add(n int) {
	if p.fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count += n
	if time.Since(p.last) >= progressInterval {
		p.fn(p.count, false)
		p.last = time.Now()
	}
}

func (p *progressReporter) finish() {
	if p.fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fn(p.count, true)
}

// normalizeRoots absolutizes, cleans, and deduplicates walk roots. A
// root equal to or nested inside another kept root is skipped so the
// same subtree is never walked (and indexed) twice.
func normalizeRoots(roots []string) (kept []string, skipped int) {
	abs := make([]string, 0, len(roots))
	for _, r := range roots {
		a, err := filepath.Abs(r)
		if err != nil {
			skipped++
			continue
		}
		abs = append(abs, filepath.Clean(a))
	}
	// Shortest first so ancestors are kept before their descendants.
	sort.Slice(abs, func(i, j int) bool {
		if len(abs[i]) != len(abs[j]) {
			return len(abs[i]) < len(abs[j])
		}
		return abs[i] < abs[j]
	})
	for _, r := range abs {
		covered := false
		for _, k := range kept {
			if isWithin(r, k) {
				covered = true
				break
			}
		}
		if covered {
			skipped++
			continue
		}
		kept = append(kept, r)
	}
	return kept, skipped
}
