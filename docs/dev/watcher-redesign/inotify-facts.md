# Fact sheet: fsnotify (inotify backend) as used by competent-search-thing, and inotify kernel-side costs

Date: 2026-07-17.

Version under study: **fsnotify v1.9.0** -- pinned in
`/home/user/competent-search-thing/go.mod:6` (`github.com/fsnotify/fsnotify v1.9.0`;
go.sum:12-13). The Go module cache on this machine is empty (go-toolchain has not
run), so the source read is the upstream tag `v1.9.0` cloned to
`<scratchpad>/fsnotify-1.9.0/` (github.com/fsnotify/fsnotify, tag v1.9.0) -- the same
content the module resolves to. All fsnotify file:line citations below are into that
tree; all repo citations are into `/home/user/competent-search-thing/` (read only, not
modified).

How the repo consumes it (context for everything below):

- `internal/watch/notify.go:36-42` -- production notifier calls plain
  `fsnotify.NewWatcher()` (NOT `NewBufferedWatcher`).
- `internal/watch/notify.go:10-14` -- one `Add(path)` per directory; "Watches are NOT
  recursive on any platform ... callers add one watch per directory."
- `internal/watch/watch.go:201-214` -- a failed `Add` (e.g. hitting
  `max_user_watches`) is counted (`DroppedWatches++`), logged once, and marks the
  watcher degraded; it never crashes.
- `internal/watch/events.go:48,179-197` -- the `Errors` channel is consumed in the
  same select loop; `fsnotify.ErrEventOverflow` => mark degraded + request a
  reconcile rescan.
- `internal/watch/events.go:78-85` -- `wantEvent` drops everything that is not
  Create/Remove/Rename: "Only Create/Remove/Rename can change the set of indexed
  NAMES -- Write and Chmod never do". Note this filter runs in USER SPACE, after the
  kernel has already queued the event and fsnotify has already read/decoded/sent it
  (see A1/A2).

---

## Part A -- fsnotify v1.9.0 inotify backend

### A1. The inotify mask registered per directory

`Watcher.Add(name)` is exactly `AddWith(name)` with no options
(backend_inotify.go:175). `AddWith` builds the mask from the option set's `op`
bitfield (backend_inotify.go:191-223); with no options the defaults apply
(fsnotify.go:425-428):

```go
var defaultOpts = withOpts{
    bufsize: 65536, // 64K
    op:      Create | Write | Remove | Rename | Chmod,
}
```

Mapping (backend_inotify.go:196-222): Create -> `IN_CREATE`; Write -> `IN_MODIFY`;
Remove -> `IN_DELETE | IN_DELETE_SELF`; Rename ->
`IN_MOVED_TO | IN_MOVED_FROM | IN_MOVE_SELF`; Chmod -> `IN_ATTRIB`.

So the **default per-directory mask** is:

```
IN_CREATE | IN_MODIFY | IN_DELETE | IN_DELETE_SELF |
IN_MOVED_TO | IN_MOVED_FROM | IN_MOVE_SELF | IN_ATTRIB   = 0x00000FC6
```

(0x100 | 0x2 | 0x200 | 0x400 | 0x80 | 0x40 | 0x800 | 0x4; IN_* values per
inotify(7) / sys/inotify.h.)

Direct answers:

- **IN_MODIFY: YES** (always, at this version). **IN_ATTRIB: YES** (always).
  => Every file content write and every attribute change (chmod/chown/utimes,
  and on Linux also each link-count drop before a delete) inside ANY watched
  directory generates a kernel event, occupies a slot in the 16384-event queue,
  is read/decoded/allocated/channel-sent by fsnotify's read loop -- **even though
  this app discards Write/Chmod at intake** (events.go:78-85). The kernel- and
  library-side cost of Write/Chmod noise is paid unconditionally.
- **IN_CLOSE_WRITE: NO.** Only reachable via the `xUnportableCloseWrite` op
  (backend_inotify.go:217-219), which requires `withOps(...)`.
- **IN_ONLYDIR: NOT set** (never appears in backend_inotify.go).
- **IN_EXCL_UNLINK: NOT set** (never appears).
- **IN_DONT_FOLLOW:** only via the `noFollow` option (backend_inotify.go:193-195),
  which is not reachable publicly (below). Default watches follow symlinks.
- **IN_MASK_ADD** is OR'd in only when re-adding an already-watched path, to merge
  flags (backend_inotify.go:258-264).
- **Can callers customize the mask via AddWith at v1.9.0? NO.** The op-selection
  option exists but is **unexported**: `func withOps(op Op) addOpt`
  (fsnotify.go:468) and `func withNoFollow()` (fsnotify.go:474). The only exported
  option is `WithBufferSize` (fsnotify.go:450), documented as "no-op on other
  platforms" -- it only affects Windows. The `AddWith` doc comment lists
  `WithBufferSize` as the sole possible option (fsnotify.go:316-322). (The
  `fsnotify.WithOps` reference in cmd/fsnotify/closewrite.go is inside a
  commented-out block.) So at v1.9.0 an application cannot trim IN_MODIFY/IN_ATTRIB
  out of the kernel mask through the public API; exported `WithOps` was slated for a
  later release ("in some use cases there may be hundreds of thousands of useless
  Write or Chmod operations per second" -- fsnotify.go:457-459).

### A2. Read-loop mechanics

- **Buffer / syscall:** one goroutine (`readEvents`, started in `newBackend`,
  backend_inotify.go:155) reads the inotify fd through an `*os.File` with a
  stack-array buffer `var buf [unix.SizeofInotifyEvent * 4096]byte` = 16 * 4096 =
  **65536 bytes per read(2)** (backend_inotify.go:351,357). SizeofInotifyEvent = 16
  (wd,mask,cookie,len; confirmed by the `buf *[65536]byte` parameter type at
  backend_inotify.go:406). Max 4096 nameless events per syscall; fewer when events
  carry NUL-padded filenames.
- **Per-event work** (loop backend_inotify.go:381-402, `handleEvent` 406-508):
  - The raw struct is cast in place (`unsafe.Pointer`, line 384) -- no alloc.
  - `handleEvent` takes **`w.mu` (the whole-watcher mutex) once per event**
    (406-408). This is the same mutex `AddWith`/`Remove`/`WatchList` take, so event
    decoding serializes against watch registration (relevant during the
    initial-walk-plus-startWatch phase of this app).
  - Name construction (423-433): `name += "/" + strings.TrimRight(string(bytes...))`
    -- typically **2 string allocations per named event** (the `string(bytes)`
    conversion and the concatenation). The `Event` itself is a small value struct
    {Name string, Op uint32, renamedFrom string} (fsnotify.go:147-170) sent by value;
    no per-event heap object beyond the name string(s).
  - Rename-cookie tracking is a fixed `[10]koekje` ring under its own mutex --
    deliberately zero-alloc (backend_inotify.go:33-49, 548-569).
  - Wd-to-path resolution is a map lookup; an unknown wd (racing Remove) is skipped
    (418-421). `IN_IGNORED`/`IN_UNMOUNT` clean the maps and emit nothing (439-442);
    `IN_DELETE_SELF`/`IN_MOVE_SELF` self-clean state (446-463).
- **Channel send:** `sendEvent` does a blocking `select` on `Events` vs `done`
  (shared.go:21-31), and events with `Op == 0` are silently filtered (shared.go:22).
  Crucially, on Linux `var defaultBufferSize = 0` (backend_inotify.go:135) and
  `NewWatcher` does `make(chan Event, defaultBufferSize)` (fsnotify.go:252-253) --
  **the Events channel is UNBUFFERED**. A stalled consumer blocks the read loop,
  read(2) stops draining, and the kernel queue (16384 events) fills and overflows.
  `NewBufferedWatcher(sz)` exists (fsnotify.go:269-276) but this repo uses
  `NewWatcher` (internal/watch/notify.go:37); its debounce loop consumes events in a
  tight select (internal/watch/events.go:48), which is the mitigation.
- **IN_Q_OVERFLOW surfacing:** checked per raw event (backend_inotify.go:386-390);
  surfaced as **`fsnotify.ErrEventOverflow` on the `Errors` channel**
  (fsnotify.go:235-243: "inotify returns IN_Q_OVERFLOW - because there are too many
  queued events (the fs.inotify.max_queued_events sysctl can be used to increase
  this)"). The overflow pseudo-event itself (wd = -1) then falls through handleEvent
  as watch==nil -> Event{} -> filtered by the Op==0 check, so nothing appears on
  `Events`. The repo reacts by degrading + requesting a reconcile rescan
  (internal/watch/events.go:179-197).
- **Bookkeeping per watch** (backend_inotify.go:52-80): a `watches` struct holding
  **two maps** -- `wd map[uint32]*watch` and `path map[string]uint32` -- plus one
  heap-allocated `watch{wd, flags uint32; path string; recurse bool}` per watched
  directory. One `sync.Mutex` (in `shared`, shared.go:5-10) guards everything.

### A3. Cost of one Watcher.Add, and 1M directories

Per `Add(path)` on a new path (backend_inotify.go:175-286):

1. closed-check (channel select), option materialization (stack copy, no heap),
   `w.mu.Lock()` for the duration (226-227).
2. `recursivePath` -> `filepath.Clean(path)` (fsnotify.go:487-488) -- one string
   alloc if the path isn't already clean.
3. `updatePath`: one map lookup by path (112-117).
4. **Exactly ONE syscall: `unix.InotifyAddWatch(w.fd, path, flags)`**
   (backend_inotify.go:264). No stat, no open, no readdir.
5. One `&watch{...}` alloc + two map inserts (269-285, 123-129).

**No O(n) work per add.** Nothing iterates existing watches on the add path.
`filepath.WalkDir` appears only under the `/...` recursive-watch feature
(backend_inotify.go:229-253), which is dead code in the release: `var enableRecurse
= false` -- "Only enabled in tests for now" (fsnotify.go:483-489). (For
completeness, the O(n)-over-all-watches scans that do exist run only on recursive
watches: removePath's prefix sweep (102-108) and the rename-children fixup
(493-503) -- both unreachable at v1.9.0.)

So ~1M `Add` calls = ~1M inotify_add_watch(2) syscalls + ~1M small structs + ~2M map
entries, linear in total, amortized O(1) each (Go map growth rehashes amortize).
Back-of-envelope user-space memory (estimate, not measured): watch struct ~48 B +
two map entries ~110 B + the path string stored once (shared by struct and map key)
-- roughly 160 B + avg path length per directory; ~250 MB at 1M dirs with ~90-byte
paths. Contention note: every event decode takes the same `w.mu`
(backend_inotify.go:407), so a hot event stream slows a bulk Add phase and vice
versa. `Remove` is likewise one map op + one `inotify_rm_watch` syscall
(302-326); the kernel's IN_IGNORED reply is what cleans the maps on implicit
removals (439-442). `WatchList()` snapshots all paths -- O(n) with allocation
(328-340); this repo keeps its own `watched` set instead
(internal/watch/watch.go:198-214).

Instance creation: `inotify_init1(IN_CLOEXEC|IN_NONBLOCK)` once per Watcher
(backend_inotify.go:140); instances are capped by `fs.inotify.max_user_instances`
(default 128, see B6).

### A4. Darwin backend (context paragraph)

On macOS fsnotify v1.9.0 uses **kqueue** (fsnotify.go:4-8), and kqueue watch
descriptors are open file descriptors: every watched path is `unix.Open`ed with
`O_EVTONLY|O_CLOEXEC` (system_darwin.go:7, backend_kqueue.go:382) and registered
with `EV_ADD|EV_CLEAR|EV_ENABLE` on `EVFILT_VNODE` (backend_kqueue.go:395,671-675).
To mimic inotify's directory semantics, adding a directory watch also opens **one fd
per file already in the directory** (`watchDirectoryFiles`: `os.ReadDir` + per-entry
`internalWatch`, backend_kqueue.go:570-601), and each directory-write event triggers
a re-`ReadDir` (`dirChange`, backend_kqueue.go:606+) to synthesize Create events.
fsnotify's own docs: "kqueue requires opening a file descriptor for every file
that's being watched; so if you're watching a directory with five files then that's
six file descriptors. You will run in to your system's 'max open files' limit
faster on these platforms" (fsnotify.go:75-80). Whole-filesystem watching via this
backend is therefore fd-bound and effectively infeasible at index scale; macOS's
native answer is FSEvents, which fsnotify does not use.

---

## Part B -- inotify kernel-side facts

### B5. Kernel memory per watch

The kernel's own cost model lives in `fs/notify/inotify/inotify_user.c`
(mainline master; introduced by the v5.11 commit in B6):

```c
/*
 * An inotify watch requires allocating an inotify_inode_mark structure as
 * well as pinning the watched inode. Doubling the size of a VFS inode
 * should be more than enough to cover the additional filesystem inode
 * size increase.
 */
#define INOTIFY_WATCH_COST	(sizeof(struct inotify_inode_mark) + \
				 2 * sizeof(struct inode))
```

(https://github.com/torvalds/linux/blob/master/fs/notify/inotify/inotify_user.c)

Composition: an `inotify_inode_mark` (an `fsnotify_mark` + the wd; ~tens of bytes)
attached to the inode's mark list, **plus the pinned inode itself** -- the commit
message states "Each watch point adds an inotify_inode_mark structure to an inode to
be watched. It also pins the watched inode" (commit 92890123749b, below). The
2x-inode term is the allowance for real filesystem inode structs (ext4/xfs inode
structs embed and exceed `struct inode`). The pin holds the inode; dentries are not
pinned by the mark.

**The ~1 KB per-watch figure, sourced:** the patch author measured it --
"For 64-bit archs, inotify_inode_mark plus 2 vfs inode have a size that is a bit
over 1 kbytes (**1284 bytes with my x86-64 config**)."
(Waiman Long, patch v4 posting,
https://patchwork.kernel.org/project/linux-fsdevel/patch/20201109035931.4740-1-longman@redhat.com/).
The widely quoted "1080 bytes on 64-bit / 540 on 32-bit" folklore predates this
define and reflects older struct sizes; it is the same order of magnitude, and the
modern authoritative number is INOTIFY_WATCH_COST (~1.25-1.3 KB on current x86_64).
Cross-check on THIS machine (kernel 6.18.5, MemTotal 16461176 kB): max_user_watches
= 129984, and 1% of RAM / 129984 back-computes to ~1296 bytes -- consistent.
(Historical note: the 2004 LWN inotify article already described the pin -- "the
inode is pinned into memory for the duration" -- and quoted 2004-era struct sizes of
just 40 bytes per watch, showing the pinned inode, not the mark, dominates:
https://lwn.net/Articles/104343/.)

### B6. Default fs.inotify.max_user_watches

- **Pre-5.11: fixed 8192** -- "The default value of inotify.max_user_watches sysctl
  parameter was set to 8192 since the introduction of the inotify feature in 2005"
  (commit message). 65536 was never the kernel default; values like 65536/524288
  seen in the wild are distro or admin overrides.
- **5.11+: scaled to 1% of RAM.** Commit **92890123749bafc317bbfacbe0a62ce08d78efb7**
  "inotify: Increase default inotify.max_user_watches limit to 1048576" (author
  Waiman Long, committed by Jan Kara, Nov/Dec 2020, landed v5.11; also picked into
  5.10-stable trees -- https://www.spinics.net/lists/stable-commits/msg356843.html):
  "Allow up to 1% of addressable memory to be allocated for inotify watches (per
  user) limited to the range [8192, 1048576]." In `inotify_user_setup()`:
  `si_meminfo(&si)`; watches_max = 1% of addressable memory / INOTIFY_WATCH_COST;
  `clamp(watches_max, 8192UL, 1048576UL)`; stored in
  `init_user_ns.ucount_max[UCOUNT_INOTIFY_WATCHES]`. "A 64-bit system with 128GB or
  more memory will likely have the maximum value of 1048576."
  (https://github.com/torvalds/linux/commit/92890123749bafc317bbfacbe0a62ce08d78efb7)
- **16 GB machine, in practice: ~125k-130k.** Measured here: this container
  (16461176 kB RAM, kernel 6.18.5) reports **129984**. fsnotify's own package docs
  quote a Linux 5.18 box at **124983** (fsnotify.go:61-62). Ubuntu 22.04 ships a
  5.15+ kernel with this formula and no default override, so a 16 GB 22.04 machine
  reports the same order (~1.2-1.3 x 10^5). Exhausting it makes inotify_add_watch
  fail with ENOSPC -- "no space left on device" (fsnotify.go:72-73) -- which this
  repo's Add-failure path absorbs as degradation
  (internal/watch/watch.go:201-214). Note the default is far below the ~1M
  directories a whole-filesystem index can hold.
- Related defaults set in the same function: `inotify_max_queued_events = 16384;`
  and `init_user_ns.ucount_max[UCOUNT_INOTIFY_INSTANCES] = 128;`
  (fs/notify/inotify/inotify_user.c). Measured here: max_user_instances = 128.

### B7. fs.inotify.max_queued_events and overflow

- Default **16384**, set in `inotify_user_setup()`
  (`inotify_max_queued_events = 16384;`, fs/notify/inotify/inotify_user.c);
  confirmed 16384 on this machine.
- Semantics per inotify(7) (https://man7.org/linux/man-pages/man7/inotify.7.html):
  "The value in this file is used when an application calls inotify_init(2) to set
  an upper limit on the number of events that can be queued to the corresponding
  inotify instance. **Events in excess of this limit are dropped, but an
  IN_Q_OVERFLOW event is always generated.**" The overflow event carries wd = -1
  ("Event queue overflowed (wd is -1 for this event)"). So overflow = silent loss
  of an unknown number of events plus exactly one marker; the only correct recovery
  is a rescan/reconcile -- which is what fsnotify's ErrEventOverflow (A2) and this
  repo's Rescanner request implement (internal/watch/events.go:179-197).

### B8. inotify_add_watch cost; millions of watches

- **Per-call work is O(1) in the number of existing watches.** The syscall performs
  a path lookup (namei -- O(path depth), dcache-hot right after an index walk), then
  `inotify_update_watch()`: try `inotify_update_existing_watch()` (scan the target
  inode's own mark list, typically length 0-1), else `inotify_new_watch()`:
  allocate the mark, **idr allocation** of the wd in the group's IDR (radix tree,
  effectively O(1) amortized), and `fsnotify_add_inode_mark_locked()` to attach the
  mark and pin the inode (fs/notify/inotify/inotify_user.c, mainline). Nothing
  scans other watches, so registering N watches is O(N) total -- 1M adds are 1M
  cheap syscalls (plus user-space path handling).
- One genuine per-add side cost: when a directory inode's mask gains
  "watch-my-children" semantics, fsnotify walks the directory's **cached child
  dentries** to set `DCACHE_FSNOTIFY_PARENT_WATCHED`
  (`fsnotify_set_children_dentry_flags()` -- "run all of the dentries associated
  with this inode" -- fs/notify/fsnotify.c, mainline; clearing is lazy per-dentry).
  After an index walk the dcache is hot, so watching every directory touches every
  cached dentry once -- still linear overall, not quadratic.
- **System-wide impact of huge watch counts** is dominated by memory, not CPU:
  - ~1.3 KB kernel memory per watch (B5) => **1M watches ~ 1.2-1.3 GB** of
    unswappable kernel memory, which is exactly why the kernel caps the default at
    1% of RAM (B6).
  - Every watched inode is **pinned** (B5): the inode cache cannot reclaim those
    inodes under memory pressure for as long as the watches exist -- an icache that
    normally shrinks on demand becomes a permanent resident set.
  - Per-event overhead concentrates on marked objects; unmarked-file operations
    take a fast path (B9). Historic evidence that the hooks' fixed cost matters and
    was engineered down: the 2015 patch "fs: optimize inotify/fsnotify code for
    unwatched files" found srcu_read_lock()'s memory barriers "expensive" on every
    open/read/write of UNWATCHED files and, by checking for marks first, "gave a
    13.8% speedup in writes/second" in a tight will-it-scale microbenchmark
    (https://lwn.net/Articles/649318/). Follow-on work continues ("Further reduce
    overhead of fsnotify permission hooks", 2024,
    https://lwn.net/Articles/965774/). The original 2004 design article claimed the
    VFS-side calls are O(1) (https://lwn.net/Articles/104343/).

### B9. Per-inode marks, not global: the fast path when no marks exist

inotify attaches **inode marks** (fanotify can additionally attach mount-,
superblock- and mntns-marks). The `fsnotify()` hook does run on every relevant VFS
operation, but mainline `fs/notify/fsnotify.c` bails before doing any real work when
no attached mark could care:

```c
if ((!sbinfo || !sbinfo->sb_marks) &&
    (!mnt || !mnt->mnt_fsnotify_marks) &&
    (!inode || !inode->i_fsnotify_marks) &&
    (!inode2 || !inode2->i_fsnotify_marks) &&
    (!mnt_data || !mnt_data->ns->n_fsnotify_marks))
        return 0;
```

with the explaining comment: "Optimization: srcu_read_lock() has a memory barrier
which can be expensive. It protects walking the *_fsnotify_marks lists. However, if
we do not walk the lists, we do not have to do SRCU because we have no references to
any objects and do not need SRCU to keep them alive." Only after that, and after
"return if none of the marks care about this type of event"
(`if (!(test_mask & marks_mask)) return 0;`), does it take
`srcu_read_lock(&fsnotify_mark_srcu)` and walk mark lists. Likewise
`__fsnotify_parent()` short-circuits: "Optimize the likely case of nobody watching
this path" -- `if (likely(!parent_watched && !fsnotify_object_watched(...)))
return 0;`, keyed off the `DCACHE_FSNOTIFY_PARENT_WATCHED` dentry flag maintained in
B8. (Quotes from https://github.com/torvalds/linux/blob/master/fs/notify/fsnotify.c;
the marks-check-before-SRCU structure is the 2015 optimization,
https://lwn.net/Articles/649318/.)

**Conclusion:** N inotify watches do NOT make every VFS operation on unmarked files
meaningfully slower -- those pay a few pointer/mask checks. The costs of N watches
are (a) ~1.3 KB kernel memory + a pinned inode each, and (b) full event generation
(mark-list walk under SRCU, event alloc, queueing) on operations that touch a marked
inode or a child of a marked directory -- which, with the whole filesystem watched
at mask 0xFC6, is essentially every file operation on the system.

---

## Sources

Code (read locally):
- /home/user/competent-search-thing/go.mod:6; internal/watch/{notify.go,watch.go,events.go} (lines cited inline)
- fsnotify v1.9.0 (github.com/fsnotify/fsnotify tag v1.9.0): backend_inotify.go, fsnotify.go, shared.go, backend_kqueue.go, system_darwin.go (lines cited inline)

Kernel / docs (fetched 2026-07-17):
- https://man7.org/linux/man-pages/man7/inotify.7.html
- https://github.com/torvalds/linux/blob/master/fs/notify/inotify/inotify_user.c
- https://github.com/torvalds/linux/blob/master/fs/notify/fsnotify.c
- https://github.com/torvalds/linux/commit/92890123749bafc317bbfacbe0a62ce08d78efb7
- https://patchwork.kernel.org/project/linux-fsdevel/patch/20201109035931.4740-1-longman@redhat.com/ (v4 posting; "1284 bytes with my x86-64 config")
- https://www.spinics.net/lists/stable-commits/msg356843.html (5.10-stable backport)
- https://lwn.net/Articles/649318/ (2015 unwatched-files optimization; 13.8% microbenchmark)
- https://lwn.net/Articles/104343/ (2004 inotify design; inode pinning, O(1) claim)
- https://lwn.net/Articles/965774/ (2024 permission-hook overhead reduction)
- Live measurements, this machine (kernel 6.18.5, 16461176 kB RAM):
  /proc/sys/fs/inotify/{max_user_watches,max_queued_events,max_user_instances} = 129984 / 16384 / 128
