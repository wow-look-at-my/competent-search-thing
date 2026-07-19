# watch-baseline: characterization of the internal/watch layer (pre-redesign)

Repo: /home/user/competent-search-thing, branch `claude/watcher-scale`, HEAD 3da9a1f ("Implement scan-time case folding, remove lowercased storage (#29)"). Working tree clean. fsnotify v1.9.0 (go.mod:6). All file:line references are against this HEAD.

## 1. Watch registration: where the watch list comes from and the exact call chain

**Source of the directory list: the Manager, not the walk.** The walker has no watch callback; the watcher enumerates directories AFTER the build by iterating the live index via `Manager.ForEachLiveDir` (internal/index/manager.go:135-144), which wraps `Store.ForEachLive` (internal/index/store.go:234-244), filters with `Store.IsDir` (store.go:213) and reconstructs each path with `Store.EntryPath` (store.go:228-230), all under the Manager's RLock for the whole iteration (manager.go:130-134 doc: "fn must be fast and must not call back into the Manager").

**Exact call chain, app startup -> per-dir Add:**

1. `main.go:42` `cli.Execute(app.Version, runGUI)` -> `runGUI` (main.go:50) builds `index.NewManager(cfg.Roots, cfg.Excludes, cfg.MaxResults)` and `app.New(...)` with `RescanEvery: time.Duration(cfg.RescanIntervalMinutes) * time.Minute` (main.go:55-63).
2. Wails OnStartup -> `App.Startup` (internal/app/app.go:206) -> `a.buildOnce.Do` launches `go a.buildIndex(ctx)` under a cancellable context stored in `a.buildCancel` (app.go:233-239).
3. `App.buildIndex` (app.go:267-290) runs `a.manager.BuildFromDisk(ctx, progress)` (app.go:279). On error (incl. cancellation) it returns and **the watch layer never starts** (app.go:280-287). On success -> `a.startWatch()` (app.go:289).
4. `App.startWatch` (app.go:297-322): `index.NewExcluder(a.manager.Excludes())` (app.go:298; a bad pattern logs and degrades to `ex = nil` = exclude nothing, app.go:299-305), then `watch.New(a.manager, a.manager.Roots(), ex, watch.Options{OnDegraded: a.emitDegraded})` (app.go:306) and `watch.NewRescanner(a.manager, w, watch.RescanOptions{Interval: a.opt.RescanEvery})` (app.go:307). Under `watchMu`, skipped entirely if `a.shuttingDown` (app.go:309-313); `w.Start()` (app.go:314, failure logs "watch: live updates unavailable (rescans still work)") and `r.Start()` (app.go:317).
5. `Watcher.Start` (internal/watch/watch.go:130-145): `lc.begin()`, `newNotifier()` (production `newFSNotifier` = `fsnotify.NewWatcher()`, notify.go:36-42; can fail on inotify max_user_instances), then `go w.run(ctx)` (watch.go:143).
6. `Watcher.run` (internal/watch/events.go:18-61) first calls `addInitialWatches(ctx)` (events.go:20).
7. `addInitialWatches` (events.go:67-76) iterates `w.desiredDirs(ctx)` and calls `w.addWatch(d)` per directory, checking `ctx.Err()` between adds so Stop can interrupt the pass (events.go:68-73); finishes with `log.Printf("watch: watching %d directories (%d dropped)", ...)` (events.go:75).
8. `desiredDirs` (watch.go:302-331) = the configured roots (absolutized/cleaned, exclude-filtered, watch.go:312-320) **plus** every live indexed directory from `mgr.ForEachLiveDir` (exclude-filtered, ctx-abortable, watch.go:321-329), deduped via a `seen` map. Roots come first (integration tests rely on "roots are watched first", integration_test.go:16-24).
9. `addWatch` (watch.go:186-215) -> `w.n.Add(dir)` = `fsnotify.Watcher.Add` (notify.go:44).

**One watch per directory, roots included: yes.** The package pins this as a cross-platform invariant: "an fsnotify watch covers exactly ONE directory everywhere ... deliberately uses the same one-watch-per-directory model on the other backends too" (watch.go:6-12); the notifier seam re-states "Watches are NOT recursive on any platform" (notify.go:11-14). Roots are watched directly ("they have no index entry of their own", watch.go:99-101).

**Add failure (max_user_watches) degradation** (watch.go:201-211): `fsnotify.ErrClosed` = racing Stop, ignored silently (watch.go:202-204). Any other error: `degradeLocked()` flips the sticky `Stats.Degraded` flag and (first flip only) arranges the one-shot `OnDegraded` callback (watch.go:205, 221-228); `stats.DroppedWatches++` (watch.go:206); only the FIRST drop logs -- `"watch: adding watch for %s failed: %v (degraded; further drops are counted silently)"` (watch.go:207-210, gated by `loggedDrop`). `addWatch` then returns and the caller loop **continues with subsequent dirs** (events.go:68-73 keeps iterating; pinned by TestWatcherDroppedWatchesDegrade, watcher_test.go:56-81: 2 drops + 2 live watches + events still applied after degradation). Dropped dirs get no retry; "Changes under those directories are only picked up by rescans" (watch.go:61-63). The app forwards degradation to the frontend as the `watch:degraded` event via `emitDegraded` (app.go:326-332).

## 2. syncWatches after a rescan

`(*Watcher).syncWatches(ctx context.Context)` (watch.go:261-291), called by the Rescanner after every successful rebuild (rescan.go:206-208), and safe concurrently with the event loop (watch.go:253).

- **Enumeration:** same `desiredDirs(ctx)` as the initial pass (watch.go:268) -> `Manager.ForEachLiveDir(fn func(path string) bool)` (manager.go:135-144). There is no dirs-only column: `ForEachLiveDir` visits **every live entry** (files included) under RLock, skipping non-dirs (manager.go:138-142). Doc admits "it visits every live entry, which can take seconds on a huge index" (watch.go:298-300).
- **Complexity:** O(all live entries) index scan + building `seen` map and `dirs` slice of every dir-path string (watch.go:302-311); then a `want` set of size D (watch.go:269-272); **drops** = one pass over `w.watched` deleting entries not in `want`, calling `n.Remove` per drop, all under `w.mu` (watch.go:273-284); **adds** = `addWatch(d)` for EVERY desired dir (watch.go:285-290) -- `addWatch` itself dedups against `w.watched` (watch.go:198-200), so already-watched dirs cost a lock+map-hit each but no syscall. Net: O(entries) + O(watched) + O(desired) with one notifier syscall per actual add/drop.
- **Cancellation:** bails up front if not started/stopping or ctx dead (watch.go:262-267); the drop loop breaks on ctx cancel BEFORE removing anything based on a partial `want` ("partial `want` never drops watches: this loop is dead on cancel", watch.go:275-277); the add loop returns between dirs on cancel (watch.go:286-288). "A later rescan reconciles whatever was left undone" (watch.go:259-260).
- Pinned by TestSyncWatchesReconciles (watcher_test.go:317-348: pre-Start no-op, add appeared dir, drop vanished dir, post-Stop no-op) and TestRescanSyncWatchesTracksVanishedAndNewDirs (rescan_test.go:43-69).

## 3. Event pipeline

Raw fsnotify event -> `run` select loop (events.go:29-59):

1. **Intake filter** `wantEvent` (events.go:84-90): only `Create|Remove|Rename` pass (Write/Chmod dropped -- they never change indexed names); the path is `filepath.Clean`ed and checked against the Excluder: `!w.ex.Match(filepath.Base(path), path)`. **This is the first Excluder point** (the second is inside `scanNewDir`, events.go:157-159; the third is `desiredDirs`, watch.go:317/325). The Excluder is the SAME `index.Excluder` type the walker uses ("keeps watch filtering byte-identical to walk pruning", watch.go:101-103; semantics in internal/index/exclude.go:9-26: bare pattern = filepath.Match on base name, pattern containing a separator = filepath.Match on the full absolute path, no `**`).
2. **Debounce** (`debouncer`, debounce.go:26-72; owned exclusively by the run loop, watch.go:96): events accumulate in arrival order; flush when (a) quiet window `Quiet` (default 250ms, debounce.go:11) elapses after the last event, (b) the OLDEST pending event reaches `MaxAge` (default 1s, debounce.go:12) -- bounds staleness under a drizzle, or (c) `MaxPending` (default 4096, debounce.go:13) is hit, which flushes immediately from `add` (events.go:40-44). One `time.Timer` tracks the deadline (events.go:25-27, relies on Go >= 1.23 timer semantics).
3. **Batch apply** `flush` (events.go:97-111): the whole batch in ARRIVAL order, ctx checked per event. `Create` -> `applyCreate`; `Remove` or `Rename` -> `applyRemove` ("Rename reporting the OLD name; the new name arrives as its own Create event", events.go:106-108). Ordered application is the convergence argument: create-then-delete ends deleted (the Create's Lstat already fails, the trailing Remove tombstones), delete-then-create ends live (AddEntry resurrects tombstones) (events.go:92-96; pinned by TestWatcherBatchOrderConverges, watcher_test.go:221-245).
4. **`applyCreate`** (events.go:119-131): `os.Lstat(path)` -- Lstat, never Stat, so symlinks are indexed as non-dirs and never followed, matching the walker (events.go:119-123; pinned watcher_test.go:83-112); gone-again paths are skipped (err return). Then `w.mgr.Add(filepath.Dir(path), filepath.Base(path), fi.IsDir())` with the error deliberately dropped (events.go:124-127). If it is a directory -> `scanNewDir`.
5. **`scanNewDir`** (events.go:140-169): **the watcher itself scans the new subtree** -- iterative LIFO stack, per dir: `addWatch(d)` FIRST, then `w.readDir(d)` (seam over `os.ReadDir`, watch.go:84/120), so nothing slips through: "anything created after ReadDir raises its own event, anything created before is in the listing, and overlaps dedup in AddEntry" (events.go:136-139). Children are exclude-filtered (events.go:157-159), added via `Manager.Add` (duplicate-safe `AddEntry`, unlike the fresh-store walk path, events.go:135-137), and subdirs are pushed for descent (symlinks: `DirEntry.IsDir` false, indexed never descended, events.go:158-166). Unreadable dirs: `continue`, "skipped, like the walker" (events.go:150-151). Ctx-abortable per iteration (events.go:143-145).
6. **`applyRemove`** (events.go:174-177): `w.mgr.Remove(path)` -- `Store.RemoveByPath` tombstones the entry AND, when path is an interned directory, the whole subtree (store.go:167-194) -- then `dropWatchesUnder(path)` removes bookkeeping + notifier watches at/below the path, ignoring notifier errors (kernel already auto-dropped watches of deleted dirs; after a rename the explicit Remove detaches the moved inode's stale watch) (watch.go:236-248; pinned by TestWatcherRemoveDirDropsWatchesAndTombstones, watcher_test.go:150-172).
7. **Rename/cookie handling: there is none.** fsnotify's portable Event carries only Name+Op; no cookie pairing exists anywhere in the package. A rename = independent Remove(old name) + Create(new name); the Create's `scanNewDir` re-indexes the moved subtree under its new path (pinned end-to-end by TestIntegrationRenameFile and TestIntegrationRenameDir, integration_test.go:70-97, including "watches must have moved along").
8. **Overflow** `handleError` (events.go:184-205): errors that are NOT `fsnotify.ErrEventOverflow` are logged (`"watch: notifier error: %v"`) and the loop continues (events.go:185-188). An overflow (wrapped counts too, via errors.Is; pinned watcher_test.go:256-258): `degradeLocked()` + `stats.Overflows++`, first one logs `"watch: event queue overflow, events lost (degraded); requesting reconcile rescan"` (events.go:189-198), then calls the `requestRescan` func (events.go:202-204) -- wired by `NewRescanner` via `setRescanRequester(r.Request)` (rescan.go:84-86, watch.go:175-179). **That is the only trigger of `Rescanner.Request` inside the watch package.** (The app additionally calls `Request` from the `!rescan` builtin and the tray menu via `App.requestRescan`, internal/app/plugins.go:428-437, tray.go:94.)

## 4. Manager public API inventory (internal/index/manager.go)

Complete exported surface on Manager (the whole file was read; this is exhaustive):

| Method | Signature | file:line |
|---|---|---|
| constructor | `NewManager(roots, excludes []string, maxResults int) *Manager` | manager.go:33 |
| build | `BuildFromDisk(ctx context.Context, progress ProgressFunc) (int, time.Duration, error)` -- walks into a fresh store, swaps under short write lock; on error old store kept; recomputes mount skips each call | manager.go:57 |
| query | `Query(q string, limit int) []Result` (limit <= 0 -> configured default; nil when no match) | manager.go:78 |
| add | `Add(parentDir, name string, isDir bool) error` (write lock; wraps `Store.AddEntry`) | manager.go:88 |
| remove | `Remove(path string) int` (write lock; wraps `Store.RemoveByPath`; subtree tombstone for dirs; returns entries removed) | manager.go:97 |
| size | `Len() int` (incl. tombstones) | manager.go:104 |
| size | `LiveCount() int` | manager.go:111 |
| health | `TombstoneRatio() float64` | manager.go:120 |
| **dir enum** | `ForEachLiveDir(fn func(path string) bool)` | manager.go:135 |
| diag | `Footprint() Footprint` | manager.go:148 |
| config | `Roots() []string` (copy) | manager.go:155 |
| config | `Excludes() []string` (copy) | manager.go:158 |
| config | `MaxResults() int` | manager.go:161 |

- **(a) Enumerate all indexed directories: YES** -- `ForEachLiveDir(fn func(path string) bool)` (manager.go:135-144), but it is implemented as a filter over ALL live entries (`Store.ForEachLive`, store.go:234-244 + `IsDir` + `EntryPath`), holds the RLock for the entire iteration, and allocates one reconstructed path string per directory.
- **(b) Enumerate direct children of a given directory: NO public method exists** on Manager or Store. The Store HAS the data -- the unexported `children map[uint32][]int32` (store.go:56) and unexported `findChild(pid, name)` (store.go:158-165) -- but nothing exports a children lookup, and Manager does not expose its `*Store` at all (unexported field, manager.go:24). Closest public things: `ForEachLiveDir` / `Store.ForEachLive` (full scans), and per-id accessors `Store.Name(id)` (store.go:216), `Store.ParentDir(id)` (store.go:219), `Store.IsDir(id)` (store.go:213), `Store.EntryPath(id)` (store.go:228) -- with which a caller holding a bare Store could group by parent itself. A reconciler wanting per-directory children needs a new accessor.
- Store's full exported surface for reference (a separate PR owns internals): `NewStore()` (store.go:62), `AddEntry(parentDir, name string, isDir bool) (int32, error)` (store.go:104; dedups by (parent,name), resurrects tombstones, refreshes the dir bit), `RemoveByPath(path string) int` (store.go:173), `Len` (207), `LiveCount` (210), `IsDir` (213), `Name` (216), `ParentDir` (219), `EntryPath` (228), `ForEachLive` (234), `Query(q string, limit int) []Result` (search.go:39), `Footprint() Footprint` (footprint.go:68). A bare Store is NOT thread-safe (store.go:6-10).
- Walk-level API: `Walk(ctx context.Context, st *Store, roots []string, excludes []string, progress ProgressFunc) (WalkStats, error)` (walker.go:47), `ProgressFunc func(indexed int, done bool)` (walker.go:26), `WalkStats{Indexed, Dirs, Errors, SkippedRoots int}` (walker.go:29-34); NumCPU workers over a shared LIFO queue (walker.go:62); roots deduped by `normalizeRoots` (walker.go:182-214). Mounts: `SystemMountSkips(roots []string) []string` (mounts.go:123, linux-only, nil on failure), pure `ParseMountSkips(r io.Reader, roots []string) []string` (mounts.go:80), cap 256 (mounts.go:30), package var seam `mountSkips` (mounts.go:34); overlay deliberately NOT skipped, fuse/fuse.* always skipped, mountpoint == configured root never skipped (mounts.go:39-57, 20-24).

## 5. Rescanner (internal/watch/rescan.go)

- **Triggers:** (1) optional periodic ticker `RescanOptions.Interval` (rescan.go:19-23, run loop rescan.go:129-140); wired from config: `RescanEvery = cfg.RescanIntervalMinutes * time.Minute` (main.go:56, app.go:307), and the config **default is 0 = disabled** (config.go:227; Normalize clamps negatives to 0, config.go:346-348). (2) One-shot `Request()` (rescan.go:93-98): never blocks, 1-slot buffered channel (rescan.go:79) coalesces storms into at most one follow-up (pinned by TestRescannerCoalescesQueuedRequests, rescan_test.go:100-112). Requesters: watcher overflow (events.go:202-204) and the app's `!rescan`/tray path (plugins.go:428-437).
- **MinGap:** applies to REQUESTED rescans only, measured from the previous rescan's END (`lastEnd`, rescan.go:24-27, 153-172); default 30s (`defaultMinGap`, rescan.go:15). Interval-ticked rescans skip the gap check (rescan.go:139-140 vs 141-145).
- **Cost of one rescan** (rescan.go:181-215): `r.build(ctx)` -- seam defaulting to `mgr.BuildFromDisk(ctx, nil)` (rescan.go:81-83) = recompute mount skips + FULL parallel disk walk of all roots into a fresh Store + swap under write lock (manager.go:57-74; queries keep answering from the old store during the walk) -- then, on success, `w.syncWatches(ctx)` (rescan.go:206-208) = the O(all live entries) enumeration + add/drop pass from section 2. Failure keeps the previous store (manager.go:67-69) and logs "watch: rescan failed (previous index kept)" (rescan.go:202).
- **Cancellation (fast-quit contract):** `Stop()` = `lc.end()` (rescan.go:118) cancels the loop ctx and blocks until exit. An in-flight build aborts mid-walk (Walk returns ctx.Err(); partial store discarded because the swap happens only on success) and is logged `"watch: rescan cancelled (previous index kept)"` -- it counts as **Failed, never Completed** (rescan.go:191-204; pinned rescan_test.go:161-198). A cancelled post-build resync stops between directories and logs `"watch: rescan cancelled (%d entries rebuilt in %s; watch resync incomplete)"` -- the swapped-in store stays, only watch bookkeeping is incomplete (rescan.go:209-212; pinned by TestRescannerStopCancelsWatchResync, rescan_test.go:234-266, explicitly "the 18.6M-file Ctrl+C hang" regression). A MinGap wait is cut short (rescan.go:166-171; pinned rescan_test.go:127-139) and a queued request is dropped, never started (pinned rescan_test.go:203-225). App shutdown order: rescanner.Stop() BEFORE watcher.Stop() because a mid-rescan calls back into the watcher's syncWatches (app.go:344-347, 419-424).
- **Stats:** `RescanStats{Completed, Failed int; Running bool}` (rescan.go:31-39), `Stats()` (rescan.go:121-125).

## 6. Config (internal/config/config.go + migrate.go)

- **Default excludes, verbatim.** `baseExcludes()` (migrate.go:23): `func baseExcludes() []string { return []string{".git", "node_modules", ".cache"} }`. `systemExcludesFor(goos)` (migrate.go:51-56): windows -> `nil`; otherwise `[]string{"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"}`. `defaultExcludes()` = base + system for the running GOOS (migrate.go:60-62). So:
  - linux/darwin default: `.git`, `node_modules`, `.cache`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/var/tmp`, `lost+found` (first three + `lost+found` are base-name patterns; the slash ones are full-path patterns, per exclude.go:9-19).
  - windows default: `.git`, `node_modules`, `.cache` only.
  - **Are .git/node_modules/.cache default-excluded on linux TODAY: YES** -- migrate.go:23 quoted above, reached via `Default()` -> `defaultExcludes()` (config.go:225, migrate.go:60-62). Note Normalize leaves excludes alone: "Excludes are left as the user wrote them (an explicitly empty list means 'exclude nothing')" (config.go:322-324) -- so a user config with `"excludes": []` really indexes everything.
- **Default roots post-#28** (PR #28 = merged branch claude/index-roots): whole filesystem. `defaultRootsFor` (migrate.go:34-43): non-windows -> `[]string{"/"}`; windows -> `%SystemDrive%` with `C:` fallback, normalized to `<drive>\`.
- **rescanIntervalMinutes default: 0** ("no periodic rescan", config.go:220-227; field doc "0 disables", config.go:64-65).
- **rootsVersion migration mechanics** (`currentRootsVersion = 2`, migrate.go:19; version history in migrate.go:11-18: 0/absent = legacy home-dir default; there is no version 1 -- v2 is the only bump). `Load` (config.go:267-295) parses, calls `c.migrateRoots()` (config.go:287), then `Normalize`, then `Save`s the file back iff migrated (config.go:289-293; a failed rewrite still returns the migrated config + error). `migrateRoots` (migrate.go:88-109): no-op if `RootsVersion >= 2`; otherwise stamps version 2, then: **condition for the scope change** = `len(c.Roots) == 0 || (len(c.Roots) == 1 && c.Roots[0] == legacyDefaultRoots()[0])` (migrate.go:93-95, legacy = absolutized `os.UserHomeDir()` falling back to ".", migrate.go:67-76). Customized roots -> stamp-only rewrite (migrate.go:96-98). On-legacy-default -> `c.Roots = defaultRoots()` plus `mergeExcludes(systemExcludesFor(GOOS))` which appends only the MISSING system patterns, never touching/reordering user patterns (migrate.go:99-107, 113-127). Every user-visible change appends a line to `MigrationNotes []string` json:"-" (config.go:88-92): "index roots upgraded to the whole-filesystem default (%s); edit roots in config.json to revert -- the first rescan will re-walk everything" and "system exclude patterns added for whole-filesystem indexing: %s" (migrate.go:100-107). The app logs each note once with a "config:" prefix at Startup (app.go:213-217, wired through `Options.ConfigNotes` from main.go:62).

## 7. Watcher Options struct and the notifier seam

**`watch.Options`** (watch.go:39-55) -- all fields:
- `Quiet time.Duration` -- debounce quiet window, default 250ms (watch.go:40-42, defaults applied in New watch.go:105-107, constant debounce.go:11).
- `MaxAge time.Duration` -- oldest-pending flush bound, default 1s (watch.go:43-46, debounce.go:12).
- `MaxPending int` -- immediate-flush batch size, default 4096 (watch.go:47-48, debounce.go:13).
- `OnDegraded func(Stats)` -- called EXACTLY once, from a watcher goroutine, at the first degradation (sticky flag, no second transition); snapshot carries the trigger; must not call back into Stop (watch.go:49-54). Production wiring: `App.emitDegraded` -> frontend `watch:degraded` event (app.go:306, 326-332).

**Seams NOT in Options** (unexported fields on Watcher, watch.go:82-84): `newNotifier func() (notifier, error)` (default `newFSNotifier`) and `readDir func(string) ([]os.DirEntry, error)` (default `os.ReadDir`), set in `New` (watch.go:119-120) and overwritten directly by tests (helpers_test.go:129, watcher_test.go:120-125). `RescanOptions` (rescan.go:19-28) = `Interval time.Duration` (0 disables ticker) + `MinGap time.Duration` (default 30s); the Rescanner's `build func(ctx) (int, time.Duration, error)` seam (rescan.go:55-57, 81-83) is likewise unexported and test-overwritten (rescan_test.go:167, 209).

**`notifier` interface** (notify.go:10-26):
```go
type notifier interface {
    Add(path string) error      // one non-recursive watch per directory
    Remove(path string) error   // may error if the kernel already dropped it; callers ignore
    Events() <-chan fsnotify.Event
    Errors() <-chan error       // e.g. fsnotify.ErrEventOverflow
    Close() error
}
```
Production implementation: `fsnotifier` wrapping `*fsnotify.Watcher` (notify.go:29-48), constructed by `newFSNotifier` (notify.go:36-42, can fail on max_user_instances). Test implementation: `fakeNotifier` (helpers_test.go:22-103) -- buffered `events` (512) / `errs` (16) channels the test pushes into via `send(op, path)` (helpers_test.go:101-103) and direct `f.errs <-`; scripted per-path Add failures via `addErr func(path) error` (helpers_test.go:25, 51-55) and `addDelay` for slow-add cancellation tests (helpers_test.go:26, 43-45); `watched` map + `has(path)` assertions (helpers_test.go:86-90); `unwatch(path)` simulates the kernel auto-dropping a deleted dir's watch so the next Remove errors (helpers_test.go:92-98); Close closes both channels and makes later Adds return `fsnotify.ErrClosed` (helpers_test.go:74-83). Integration tests pass a nil notifier to keep the real fsnotify (integration_test.go:21, helpers_test.go:123-132).

## 8. Sizing numbers from THIS container (measured 2026-07-17)

- `find / -xdev -type d 2>/dev/null | wc -l` = **21,582**. `-xdev` stays on the root ext4 device, so it skipped: /proc (proc), /sys + cgroup mounts (sysfs/tmpfs/cgroup/cgroup2), /dev + /dev/shm + /dev/pts (devtmpfs/tmpfs/devpts), /opt/claude-code and /opt/env-runner (separate ext4 mounts), /mnt/skills/public + /mnt/skills/examples (squashfs). Notably /tmp and /run are NOT separate mounts here (part of the root fs), so their dirs (92 and 23) ARE inside the 21,582. 21 total mount-table lines.
  - App-perspective deltas: the whole-fs default walk would ALSO descend /opt/claude-code (3 dirs), /opt/env-runner (2), /mnt/skills (84) -- ext4/squashfs are in neither `virtualFSTypes` nor `networkFSTypes` (mounts.go:39-57) -- while default excludes prune /tmp (92), /run (23), /var/tmp (1) and the node_modules/.git/.cache subtrees: 79 node_modules dirs holding 2,735 descendant dirs, 6 .git dirs holding 88, plus 1,697 dirs inside .cache trees. Net realistic watch count for a default config on this container: roughly 17k directories = ~13% of the watch budget below. (A real desktop with millions of directories is where 129,984 breaks.)
- `/proc/sys/fs/inotify/max_user_watches` = **129,984**; `max_queued_events` = **16,384**; `max_user_instances` = 128. Kernel `uname -r` = **6.18.5**. `whoami` = **root**.
- Capabilities (`capsh --print | head -5`): `Current: =ep cap_sys_resource-ep` -- i.e. effectively full root caps EXCEPT `cap_sys_resource` is dropped (Bounding set includes cap_sys_admin etc.; `Current IAB: !cap_sys_resource`). Relevant: raising rlimits/inotify budgets that need CAP_SYS_RESOURCE is off the table in-container.
- `git -C ... log --oneline -8`: 3da9a1f (#29 case folding), 9c2a732 (#28 whole-fs roots, "WIP" prefix), 4180d30 (#26 path-aware search), 02e5ae1 (#27 darwin build), ea77fa9 (#18), 76b4e10 (#25 ci cleanup), 38e9f40 (#23), ecd756e (#16). `git status --short`: clean (empty). Current branch: `claude/watcher-scale`.
- `gh pr list --state open`: **empty -- no open PRs.** `gh pr view 29 --json headRefName,state,title` = `{"headRefName":"claude/store-single-blob","state":"MERGED","title":"index: single original-case name blob with case-folded scanning"}` (PR #29 is MERGED, so the store-internals PR the task brief firewalled is already landed at this HEAD). PR #28 (claude/index-roots, whole-fs roots) is also MERGED.

## 9. Tests that pin current behavior (redesign must respect or consciously renegotiate)

internal/watch/watcher_test.go:
- `TestWatcherInitialWatchSet` (18): initial set = root itself + every live indexed dir; an excluded dir that is somehow LIVE in the index is still never watched (drift-safety); healthy start is not Degraded.
- `TestWatcherExcludedRootIsNotWatched` (39): a root matching its own exclude list gets no watch, while its (live) children do -- the walker only exclude-checks children, the watcher exclude-checks roots too.
- `TestWatcherDroppedWatchesDegrade` (56): scripted Add failures -> exact DroppedWatches count, remaining dirs still watched (no abort), Degraded sticky, event loop keeps applying events afterwards.
- `TestWatcherCreateFileAndSymlink` (83): created files indexed from events; symlink-to-dir indexed as NON-dir, never watched/descended; a Create whose path vanished pre-flush is silently skipped.
- `TestWatcherCreateDirScansSubtree` (114): ONE topmost Create event indexes the whole pre-existing subtree (coalesced mkdir -p); nested dirs gain watches; excluded subdirs neither indexed nor watched; the watch lands BEFORE the (possibly failing) ReadDir; an unreadable dir's own entry is indexed, its contents skipped.
- `TestWatcherRemoveDirDropsWatchesAndTombstones` (150): one Remove event tombstones the subtree and drops all nested watches; notifier Remove errors (kernel auto-drop) are shrugged off, loop survives.
- `TestWatcherRenameOldNameRemoves` (174): a Rename event alone tombstones the old name.
- `TestWatcherWriteAndChmodIgnored` (187): Write/Chmod never create entries, even for unknown paths.
- `TestWatcherExcludedEventsDropped` (204): excluded Create events (base-name and glob patterns) neither index nor watch; watch count unchanged.
- `TestWatcherBatchOrderConverges` (221): in-order batch application: delete-then-create ends LIVE, create-then-delete ends DELETED.
- `TestWatcherOverflowDegradesAndRequestsRescan` (247): every overflow (wrapped included) counts and fires one rescan request each; non-overflow errors keep the loop alive.
- `TestWatcherLifecycle` (269): Stop-before-Start no-op; Start-after-Stop and double-Start error; Stop idempotent and loop actually exited; notifier construction failure surfaces from Start and Stop still safe.
- `TestWatcherStopInterruptsInitialAdds` (298): Stop interrupts a long initial registration pass between Adds (bounded shutdown).
- `TestSyncWatchesReconciles` (317): syncWatches = guarded no-op pre-Start and post-Stop; after an out-of-band rebuild it adds appeared dirs and drops vanished ones exactly.

internal/watch/degraded_test.go:
- `TestOnDegradedFiresOnceForDroppedWatch` (52): OnDegraded fires exactly once, not per drop; snapshot carries >= the triggering drop.
- `TestOnDegradedFiresOnceForOverflow` (75): fires once for the first overflow; snapshot taken AFTER the counter moved; sticky -- no second callback on the second overflow.
- `TestOnDegradedNotCalledWhenHealthy` (94): zero callbacks on a healthy watcher.
- `TestNilOnDegradedIsSafe` (109): nil callback + degradation must not panic.

internal/watch/debounce_test.go:
- `TestDebouncerQuietWindow` (15): deadline = last event + quiet; each event pushes it out; empty debouncer has no deadline.
- `TestDebouncerMaxAgeCapsTheDrizzle` (37): maxAge measured from the FIRST pending event wins over a perpetually-reset quiet window.
- `TestDebouncerSizeCapAndTake` (55): add returns true exactly at maxPending; take preserves arrival order and resets state (fresh first-event clock next burst).

internal/watch/rescan_test.go:
- `TestRescannerRequestRebuildsAndSyncsWatcher` (21): a Request picks up out-of-band changes AND resyncs the watch set; Completed counted, Failed 0.
- `TestRescanSyncWatchesTracksVanishedAndNewDirs` (43): post-rescan resync adds born dirs, drops vanished, keeps survivors + root; no degradation.
- `TestRescannerMinGapSpacesRequests` (71): first request immediate; back-to-back request waits MinGap out but still runs.
- `TestRescannerIntervalTicker` (87): interval ticker rescans with NO watcher attached (nil watcher is legal).
- `TestRescannerCoalescesQueuedRequests` (100): N pre-Start requests collapse to exactly one rescan; no phantom follow-ups.
- `TestRescannerFailureKeepsOldIndex` (114): a failing rebuild (bad exclude) increments Failed and the previous store still answers queries.
- `TestRescannerStopCutsMinGapWait` (127): Stop interrupts a MinGap wait (MinGap=1h must not delay quit).
- `TestRescannerLifecycle` (141): zero MinGap -> defaultMinGap (30s); double Start/Stop-idempotent/start-after-stop semantics; stop-before-start no-op.
- `TestRescannerStopCancelsInFlightRescan` (161): Stop cancels an in-flight build promptly; logged "watch: rescan cancelled", NEVER "complete"/"failed" text; previous store serves; counts as Failed (not Completed); Running false.
- `TestRescannerStopDropsQueuedRequest` (203): a request queued behind an in-flight rescan is dropped by Stop, never started (build count stays 1).
- `TestRescannerStopCancelsWatchResync` (234): THE motivating regression (18.6M-file Ctrl+C hang): Stop interrupts the post-rebuild watch resync between directories; incomplete resync logged as cancellation, not completion.
- `TestOverflowDegradationReconcilesEndToEnd` (268): overflow -> degraded -> reconcile request -> fresh store swap -> lost files indexed -> watch set resynced.

internal/watch/integration_test.go (real fsnotify/inotify, real kernel):
- `TestIntegrationCreateAndDeleteFile` (27): live create/delete round-trips into the index.
- `TestIntegrationNestedDirCreation` (39): mkdir -p subtree fully indexed; every level watched; the DEEPEST new dir delivers its own subsequent events (watch really registered).
- `TestIntegrationDeleteSubtree` (56): RemoveAll -> subtree gone from index AND watch count back down; siblings untouched.
- `TestIntegrationRenameFile` (70): rename = old name gone + new name present.
- `TestIntegrationRenameDir` (81): renamed dir re-indexed under the new path, old paths tombstoned, and events inside the renamed tree keep arriving under the NEW path (watches moved).
- `TestIntegrationExcludedDirStaysDark` (99): excluded dir created live is never indexed/watched; settle marker proves the events were dropped, not pending.
- `TestIntegrationBurstIsCoalescedAndComplete` (116): a 400-file burst lands COMPLETELY through debounced flushes (no lost events under coalescing).

Cross-package pins a redesign must also respect: internal/app/app_test.go:321-325 (a malformed exclude pattern cannot panic `startWatch`; watcher runs with a nil Excluder) and internal/app/tray_test.go:220 (`requestRescan` errors with a friendly message while the rescanner is still nil, i.e. during the initial build -- plugins.go:428-437). Helper contracts baked into many tests: `settle` relies on in-order application to prove drain (helpers_test.go:158-172); `startLive` relies on roots being watched FIRST (integration_test.go:16-24).
