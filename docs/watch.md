# internal/watch

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/watch` -- keeps the index live after the initial walk;
three cooperating tiers whose CONTRACT is identical final index
state, differing only in latency (pinned by TestTierEquivalence*).
Event model (2026-07 redesign): an event is only a DIRTY PATH -- op
codes are advisory (consulted once at intake to drop Write/Chmod)
and lstat at apply time decides: gone -> `Manager.Remove` (subtree
tombstone) + watches under the path dropped; file -> `Manager.Add`
(a dir->file flip first tombstones the old subtree -- AddEntry only
flips the bit); dir -> Add + `reconcileDir` = shallow readdir diff
vs `Manager.ChildrenOf`, recursing (scanNewDir) ONLY into
index-unknown children, kind flips tombstoned+re-added, missing
children removed -- so application is order-independent by
construction (fanotify-style merged events plug in) and the sweeper
feeds the same reconcile with paths that never had events. `Watcher`
(watch.go = types/lifecycle/state helpers, events.go = the run loop
+ reconcile engine, hotset.go = the hot-set bookkeeping split out
for the 750-line cap): a bounded HOT SET of fsnotify watches --
fsnotify uniform on ALL platforms, never recursive.
`Options.MaxWatches` (config watcher.maxWatches -> app.Options
.WatchMaxWatches): 0 = auto via TWO per-OS seams (`readMaxWatches`
raw limit + `autoBudget` formula, production bindings in
budget_{linux,darwin,other}.go; both formulas live untagged in
watch.go so every job tests both): linux = autoBudgetInotify
(min(max_user_watches/2, 65536), floor 1024), darwin =
autoBudgetDarwinFD over readFDLimit (fdlimit_darwin.go, read-only
Getrlimit -- NEVER add a Setrlimit: the Go runtime already raises
the soft limit at init and restores the original in exec'd
children; min(RLIMIT_NOFILE/16, 8192), floor 256, /16 because
kqueue opens one fd per watched dir PLUS one per direct child
file -- the unbudgeted model pinned a field machine at its fd
ceiling), elsewhere/read failure = unlimited watch-everything;
negative = unlimited. `FormatBudget` renders math.MaxInt as
"unlimited" in BOTH budget log lines (events.go fill summary +
the app summary), never the raw digits.
`Options.WatchEx` (config watcher.watchExcludes; a SECOND
index.Excluder distinct from the walk one): matching dirs AND
their whole subtrees (watchExcluded walks ancestors -- the walk
excluder gets subtree coverage from pruning, watch-excluded trees
stay indexed so it must be reproduced) are skipped at every
watch-issuing point (fill, event/sweep promotion, cold refill,
resync want-set, root pinning) and leave Stats.IndexedDirs, but
stay fully indexed + swept -- staleness bound = the sweep
interval; nil = one nil-check on the hot path. Fill
priority (addInitialWatches + budget-aware syncWatches refill):
roots first (pinned, always watched, never evicted), then dirs
under the `homeDir` seam (os.UserHomeDir) to 75% of budget, then
the rest; fills use cold adds (at budget: NO syscalls issued --
beyond-budget dirs stay cold for sweeps, no failing-syscall storm).
Recency is a container/list LRU: touches = addWatch/refreshWatch on
a watched dir, reconcile touching a watched parent (map hit only --
file events never promote cold parents), and `promote(dir)` = watch
with eviction (reconcileDir's refreshWatch promotes sweep-found
dirs); at budget a new hot dir evicts the least-recently-touched
(Stats.Evictions -- NOT degradation; DroppedWatches stays strictly
"the OS refused"). Events are debounced (debounce.go: dirty-path
set, quiet ~250ms / oldest ~1s / 4096 cap; injectable). Excluded
paths filtered with the SAME `index.Excluder` as the walks. The
notifier seam (notify.go; optional `backendInfo` extension = kind()
name + wideCoverage) keeps unit tests scripted; integration
tests run real inotify/kqueue. The production per-dir `fsnotifier`
implements backendInfo too: kind() = (`PerDirBackendName()`, false)
-- the HONEST per-OS label ("inotify" linux, "kqueue" darwin+BSDs,
"windows" windows; also the New-time Stats default), exported for
app_test's per-GOOS assertions. BACKEND SELECTION: New binds
`newBackendNotifier(Options.Backend, normalized roots)` (notify.go;
config watcher.backend -> app.Options.WatchBackend): "inotify" =
plain fsnotify on every OS, no whole-filesystem probe; "fanotify"
and "fsevents" = STRICT `newStrictFanotifyNotifier` /
`newStrictFSEventsNotifier` (per-OS: fanotify_linux.go +
fsevents_darwin.go carry their own-OS strict + auto selections,
fanotify_other.go is now `!linux && !darwin`, fsevents_other.go =
`!darwin`; off its OS each strict mode is the loud
always-unavailable noop) -- constructor failure = one LOUD
'backend "..." required by config but unavailable ... live
watching DISABLED' line + the no-op `noopNotifier` (notify.go:
accepts everything, delivers nothing, kind ("none", wide) so
Watched/IndexedDirs stay 0 and addInitialWatches logs no
coverage-active line for it; sweeps converge), NEVER a per-dir
fallback; anything else = `newAutoNotifier` -- linux tries
fanotify, darwin tries fsevents, windows/BSDs go straight to
per-dir fsnotify; ANY constructor error = one log line +
per-directory fallback. The `newFanotifyFn` / `newFSEventsFn`
package vars are the constructor seams the selections probe
(scripted in tests, no privileges needed). FSEVENTS BACKEND
(fsevents_darwin.{go,h,c} cgo over CoreServices +
fsevents_events.go, the UNTAGGED pure half unit-tested on linux
CI too): ONE FSEventStreamCreate over the roots'
EvalSymlinks-RESOLVED spellings (FileEvents|NoDefer, sinceNow,
latency 0.3s, callbacks on a private serial dispatch queue via
FSEventStreamSetDispatchQueue -- no run loop), a cgo.Handle
trampoline (launchmint pattern) feeds handleBatch -> `fseDecide`
per record: overflow flags (MustScanSubDirs/UserDropped/
KernelDropped/IdsWrapped) -> the fsnotify overflow sentinel
(degrade + sweep) AND MustScanSubDirs still emits its subtree
root; content/metadata-only flags dropped (the Write/Chmod
analogue; flags==0 KEPT, fail open); `fsePathTranslator` maps
resolved prefixes back to configured spellings (/tmp ->
/private/tmp forking guard); paths outside the roots dropped
(stream-on-"/" sees everything). Close ordering is load-bearing:
closed flag -> FSEventStreamStop/Invalidate/Release -> dispatch
queue DRAIN (dispatch_sync_f) -> only then cgo.Handle delete +
channel closes. fsevents_darwin_test.go runs REAL FSEvents
un-gated on the mac job (delivery incl. symlink translation,
watcher-level convergence, scripted selections, handleBatch
overflow paths -- the integration twin CI's unprivileged fanotify
cannot have). fanotifyNotifier: ONE
FAN_CLASS_NOTIF|FAN_REPORT_DFID_NAME|FAN_CLOEXEC|FAN_NONBLOCK
group; FAN_MARK_FILESYSTEM marks (mask CREATE|DELETE|MOVED_FROM|
MOVED_TO|ONDIR; FAN_RENAME deliberately unused) on every root's
filesystem -- ANY root-mark failure (EPERM without CAP_SYS_ADMIN,
ENODEV null fsid, EXDEV) fails the WHOLE constructor so the
fallback takes over cleanly (no mixed-backend watcher in v1) --
then best-effort marks per extra real mountpoint under the roots
(index.RealMountpoints; a refused mount logs once and is left to
sweeps: coverage holds, latency differs). Events: kernel reports
(parent-dir file handle, name); the read loop routes the handle by
fsid to that superblock's O_PATH mount fd (a handle resolves ONLY
against its own fs), open_by_handle_at + readlink /proc/self/fd ->
parent path (needs CAP_DAC_READ_SEARCH; ESTALE = parent gone =
drop), joins the name, filters to the configured roots (whole-sb
marks see outside paths; the index scope never widens), resolving
each (fsid, handle) once per read batch (deliberately NO
cross-batch cache in v1: a persistent LRU needs rename/delete
invalidation to stay truthful), emits advisory
fsnotify.Create -- reconcile-by-lstat absorbs merged masks. Full
events channel (1024) drops + synthesizes ErrEventOverflow;
parsing lives in fanotify_parse_linux.go (bounds-checked
DFID_NAME record walker, unit-tested on synthetic buffers); ALL
syscalls sit behind seam fields (init/mark/read/resolve/fsid/
mounts) so routing/dedup/overflow/shutdown logic tests run
unprivileged, plus a capability-gated integration test (t.Skip
without CAP_SYS_ADMIN; skipped in CI -- the documented coverage
limitation). `MarkMount(path)` extends coverage to
sweeper-discovered mounts (unmarking on unmount is NOT
implemented; the stale mark pins a little kernel memory until the
group closes). Under wideCoverage the Watcher sets `wide`: hot-set
fill, bookkeeping, and every per-directory watch call become
no-ops (Watched/IndexedDirs stay 0). Degradation (never crash,
never spin): refused watch = counted+logged once; event-queue
overflow = lost events -> Sweeper.Request when wired, else
Rescanner fallback; OnDegraded edge-triggered once -> app's
"watch:degraded".
Stats{Backend "inotify"|"kqueue"|"windows" (per-dir, per-OS honest)
|"fanotify"|"fsevents" (wide)|"none" (strict mode refused: no
live watching, sweeps only), Budget, WatchedDirs,
IndexedDirs, DroppedWatches, Evictions, Overflows, Degraded};
`InitialRegistration()` closes when the first fill finished (the
app waits on it before its summary log). DEFERRED START
(StartDeferred/Release, the app's register-before-index ordering):
StartDeferred = Start with the fill and ALL application HELD --
the notifier is live immediately (wide marks cover everything, the
per-directory model watches just the configured roots), the run
loop's hold phase (collectUntilRelease in events.go) drains events
into the debouncer's dirty set WITHOUT applying (deduped, bounded
by the unexported holdCap, default 65536; new paths beyond it are
dropped + latched), and Release (idempotent; wire the
Sweeper/Rescanner first) lets the loop run the normal fill (against
the CURRENT index -- the app releases after the fresh-store swap)
then flush the held set through the ordinary reconcile;
reportHoldLoss converges any hold loss (cap drops count+log+degrade
as an overflow; overflows that fired while requesters were unwired
re-kick) via one sweep request. Stop works held or released
(the hold phase exits on ctx cancel and the fill/flush no-op);
deferred_test.go pins mid-build-events-reach-final-index,
roots-watched-immediately, cap-loss-degrades-and-sweeps,
overflow-resweep-at-release, stop-without-release, and
Release-as-no-op-on-plain-Start. `Sweeper` (sweep.go): the
always-on convergence tier -- NewSweeper(m, w != nil, SweepOptions
{Interval 20m default, MinGap 1m, InitialWatermark (zero = first
pass re-lists EVERY dir; the app passes build-completion time),
StatsPerSec 50000 sleep-throttle, unexported `mounts` seam
(default index.RealMountpoints over the roots)}). One pass:
mount-table snapshot
under the roots diffed vs the previous pass (symmetric difference
force-reconciled -- mount-onto-existing-dir moves no mtime,
unmounts restore content silently; an APPEARED mountpoint gets
Watcher.markMount first, so a fanotify backend marks the new
filesystem before its content is indexed), then the roots (no index entry
of their own: routed to reconcileDir directly, a full reconcile
would invent one), then every live indexed dir via
Manager.LiveDirsPage(4096): lstat each; gone or mtime >= watermark
- 2s slack -> reconcile (Relisted), else skip (Swept). The
watermark advances to the pass's start ONLY on completion --
cancelled passes redo the window; mtime-BACKDATED mutations (tar
--preserve) are the documented miss, converging via full re-list /
rescan / !rescan. SweepStats{Completed, Cancelled, Running,
LastStart, LastDuration, Swept, Relisted}. COMPACTION
(maybeCompact, run after every COMPLETED pass): index removals only
set a tombstone bit, and the name bytes plus the offset/parent/flag
columns and the children slot come back ONLY through a rebuild into
a fresh store -- with rescanIntervalMinutes defaulting to 0 the only
post-startup rebuild was a manual !rescan, so a machine churning
DISTINCT names (build artifacts, package installs, temp files) grew
the index for as long as the app ran (re-creating the SAME name
resurrects its entry, so stable-name churn never mattered). A
completed pass now reads Manager.TombstoneRatio -- the field that
was documented as the rebuild trigger and had NO caller -- and asks
the Rescanner (via the Watcher's requester, already wired because
the app builds the rescanner first) for a rebuild past
SweepOptions.CompactRatio (0.30) once the store passes
CompactMinEntries (20000); a negative ratio disables it and no
Rescanner means no request. The gates are deliberately high and
self-limiting: a rebuild resets the ratio to zero, so re-triggering
needs another 30% of the index deleted. Note sweepEnabled=false
therefore also turns compaction off (no Sweeper to run the check).
compact_test.go pins the trigger, both gates, the disable, and the
no-Rescanner-is-inert case. `Rescanner` (rescan.go):
serialized full rebuilds -- `Manager.BuildFromDisk` (fresh-store
swap; queries never block) then budget-aware `syncWatches` --
triggered by an optional interval ticker (config
`rescanIntervalMinutes`) and one-shot requests, coalesced through a
1-slot channel, spaced by MinGap (default 30s). Stop cancels
promptly at ANY point on all three loops (fast quit): in-flight
rebuild aborts mid-walk, syncWatches stops between dirs, sweep
passes abort between dirs and inside throttle sleeps, MinGap waits
cut short, queued requests dropped. All three loops share the
lifecycle.go Start/Stop plumbing: idempotent Stop, safe
before/during Start, no goroutine leaks. App wiring: startWatch
builds the watch-only excluder (bad watcher.watchExcludes pattern =
log + nil), passes app.Options {WatchMaxWatches, WatchEx} into
watch.Options, builds watcher + rescanner + sweeper (SweepOptions
.Interval = app.Options.SweepInterval, 0 -> the app-side 20m
default) -- EXCEPT under Options.SweepDisabled (config
watcher.sweepEnabled=false): the Sweeper is never built and ONE loud
warning says unwatched dirs now converge only at full rescans
(overflow recovery then falls back to the Rescanner request path)
-- starts them in that order, waits
for InitialRegistration, then logs ONE summary ("watch: backend %s:
%d/%d dirs live-watched (budget %d); sweep interval %s; full rescan
interval %s" -- sweep interval reads "disabled" when off); Shutdown
stops rescanner, then sweeper (nil-tolerated), then watcher
(the sweeper reconciles through the watcher). measure_test.go is
the env-gated watcher measurement harness (the internal/index
gated-bench pattern: BENCHMARK phase, b.N ignored, skip unless
COMPETENT_SEARCH_WATCH_MEASURE=1, knobs _DIRS/_STORM/_ROOT/_OUT)
backing the PR-body registration/storm/idle numbers.
