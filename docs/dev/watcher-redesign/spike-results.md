# fanotify whole-filesystem dirent-watcher spike -- results

Empirical spike run 2026-07-17 on this machine. Program: `fanospike.c` (this
directory), gcc 13.3.0 `-O2`. Raw full outputs: `main.out` (first run, marked
the wrong fs -- kept as evidence of the ENODEV discovery), `main2.out`
(definitive run, scratch on the markable fs). All quoted lines below are
verbatim from `main2.out` unless labeled otherwise.

VERDICT: the mechanism (FAN_MARK_FILESYSTEM + FAN_REPORT_DFID_NAME) works
exactly as hoped as a whole-filesystem directory-entry watcher -- proven
end-to-end on this kernel -- BUT on THIS machine the root ext4 cannot be
superblock-marked because it was mkfs'd with no UUID (null fsid -> ENODEV).
The full event battery therefore ran against the tmpfs superblock
(/dev/shm), and an ext4-with-UUID superblock (/opt/claude-code) was
successfully marked as a control. Details in test 2.

## Environment

```
$ uname -r
6.18.5
$ id -u
0
$ grep Cap /proc/self/status
CapInh: 0000000000000000
CapPrm: 000001fffeffffff
CapEff: 000001fffeffffff        # full caps incl. CAP_SYS_ADMIN (bit 21)
CapBnd: 000001fffeffffff
CapAmb: 0000000000000000
$ stat -f -c %T /
ext2/ext3                       # (coreutils' name for the ext4 magic 0xef53)
$ head /proc/self/mounts        # trimmed to the relevant lines (21 total)
/dev/vda / ext4 rw,relatime,resuid=65534,resgid=65534 0 0
/dev/vdb /opt/claude-code ext4 ro,relatime 0 0
/dev/vdc /opt/env-runner ext4 ro,relatime 0 0
/dev/vdd /mnt/skills/public squashfs ro,relatime,errors=continue 0 0
tmpfs /dev/shm tmpfs rw,relatime 0 0
$ ls /proc/sys/fs/fanotify/
max_queued_events  max_user_groups  max_user_marks  watchdog_timeout
/proc/sys/fs/fanotify/max_queued_events = 16384
/proc/sys/fs/fanotify/max_user_groups = 128
/proc/sys/fs/fanotify/max_user_marks = 138536
/proc/sys/fs/fanotify/watchdog_timeout = 0
```

Kernel config: `CONFIG_FANOTIFY=y`, `CONFIG_FANOTIFY_ACCESS_PERMISSIONS=y`
(from /proc/config.gz). gcc/capsh already installed. /tmp and /home/user are
on / (same ext4 superblock); /dev/shm is a separate tmpfs superblock.

The decisive environment fact, found during the spike (statfs f_fsid, and
`dumpe2fs -h /dev/vda` reporting `Filesystem UUID: <none>`):

```
statfs(/               ): f_type=0x0000ef53 f_fsid=00000000:00000000  <-- NULL fsid
statfs(/home/user      ): f_type=0x0000ef53 f_fsid=00000000:00000000  <-- NULL fsid
statfs(/dev/shm        ): f_type=0x01021994 f_fsid=871fb805:353794b4
statfs(/opt/claude-code): f_type=0x0000ef53 f_fsid=00000080:00400000
```

## Test 1: fanotify_init -- PASS

```
T1a fanotify_init(FAN_CLASS_NOTIF|FAN_CLOEXEC|FAN_REPORT_DFID_NAME, O_RDONLY): PASS fd=4
T1b fanotify_init(... FAN_REPORT_FID alone): PASS
T1c fanotify_init(... FAN_REPORT_DFID_NAME_TARGET): PASS (bonus probe)
T1d working group re-init with |FAN_NONBLOCK: PASS fd=4
```

All init variants succeed. (T1c matters later: the TARGET_FID variant that
also reports the CHILD's fid is available on this kernel.)

## Test 2: FAN_MARK_FILESYSTEM -- FAIL on "/" (ENODEV), PASS on tmpfs and on ext4-with-UUID

```
T2 mark(/): FAIL errno=19 (No such device) [0.002 ms]
T2 mark(/home/user): FAIL errno=19 (No such device) [0.002 ms]
T2 mark(/dev/shm, CREATE|DELETE|MOVED_FROM|MOVED_TO|ONDIR): PASS in 0.007 ms (7 us)
```

Root cause isolated (test 2x, fresh groups):

```
T2x-a fresh group, sb-mark "/" (ext4, fsid=0:0): FAIL No such device
T2x-b fresh group, sb-mark "/opt/claude-code" (ext4 WITH uuid, fsid!=0): PASS
T2x-c fresh group, plain INODE mark on ext4 dir (weak-fsid path): PASS
  EVENT mask=0x100 [CREATE] ... fsid=00000000:00000000 handle_type=1 handle_bytes=8 handle=1ac21c00808ecc6e name="f1"
      resolve(mount_fd="/")        -> /tmp/fanospike-inodeprobe
CHECK PASS: inode mark on null-fsid ext4 DOES deliver dirent events
T2x-d same group, add mark on OTHER fs (tmpfs) after weak-fsid mark: FAIL Invalid cross-device link
```

Conclusion: ENODEV is NOT an ext4 or kernel limitation -- fid-mode fanotify
derives fsid from the superblock UUID, this VM's root fs was created with
`Filesystem UUID: <none>`, and superblock marks require a non-null fsid.
An ordinary ext4 (with UUID, T2x-b) sb-marks fine. A null-fsid fs still
accepts plain per-inode marks ("weak fsid", kernel 6.8+): events carry
fsid 0:0, handles resolve -- but such marks cannot coexist with marks on any
other fs in the same group (EXDEV, T2x-d). Registration cost: microseconds
(it attaches to the superblock object; no tree walk).

## Tests 3+4: ops -> named events + parent-handle resolution -- ALL PASS

Every op produced exactly the expected event with the entry NAME and the
parent DIRECTORY handle. fd is always -1 (FAN_NOFD) in fid mode; pid is the
acting process. Representative verbatim events (scratch =
/dev/shm/fanospike-scratch):

```
  EVENT mask=0x100 [CREATE] pid=25751 fd=-1 event_len=64 metadata_len=24 vers=3
    INFO type=DFID_NAME(2) len=40 fsid=871fb805:353794b4 handle_type=1 handle_bytes=12 handle=57452ab60300000000000000 name="file-a"
      resolve(mount_fd="/")        -> FAILED errno=116 (Stale file handle)
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch
  EVENT mask=0x40000100 [CREATE|ONDIR] ... name="subdir"
  EVENT mask=0x100 [CREATE] ... handle=a5f528430500000000000000 name="inner.txt"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch/subdir
  EVENT mask=0x40000040 [MOVED_FROM|ONDIR] ... name="subdir"
  EVENT mask=0x40000080 [MOVED_TO|ONDIR] ... name="subdir2"
  EVENT mask=0x200 [DELETE] ... name="inner.txt"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch/subdir2
  EVENT mask=0x40000200 [DELETE|ONDIR] ... name="subdir2"
CHECK PASS: Q5: parent handle is the IMMEDIATE parent ("/dev/shm/fanospike-scratch/subdir" ends with /subdir), not the marked root
CHECK PASS: parent handle resolves to CURRENT path after dir rename ("/dev/shm/fanospike-scratch/subdir2")
```

Resolution: `open_by_handle_at(mount_fd, handle, O_RDONLY|O_PATH)` +
`readlink(/proc/self/fd/N)` works every time WITH A MOUNT FD ON THE SAME
FILESYSTEM; a mount fd of "/" (different fs from the marked tmpfs) fails
with errno=116 ESTALE on every attempt. Note `handle_type=1`
(FILEID_INO32_GEN) on both tmpfs (12 bytes) and this ext4 (8 bytes).

Test 4b (stale handles -- parent rmdir'd before the queue was drained):

```
  EVENT mask=0x300 [CREATE|DELETE] ... handle=62ae65e30700000000000000 name="f"
      resolve(mount_fd=marked-fs)  -> FAILED errno=116 (Stale file handle)
CHECK PASS: create event for tmpd/f still delivered after tmpd was rmdir'd
CHECK PASS: resolving the DELETED parent's handle fails with ESTALE (got errno=116 Stale file handle)
```

Two extra findings in that one event: (1) a deleted parent's handle is
ESTALE at resolve time, and (2) the create+unlink of the SAME name MERGED
into ONE event with mask CREATE|DELETE (0x300) -- op order is not
recoverable from a merged event.

## Test 5: rename semantics -- ALL PASS

Cross-directory rename (`xfile` -> `dstdir/xfile2`):

```
  EVENT mask=0x40 [MOVED_FROM] ... handle=57452ab6... name="xfile"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch
  EVENT mask=0x80 [MOVED_TO] ... handle=faae5ed9... name="xfile2"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch/dstdir
CHECK PASS: Q5: cross-dir MOVED_FROM has OLD parent + OLD name ("xfile")
CHECK PASS: Q5: cross-dir MOVED_TO has NEW parent + NEW name ("xfile2")
CHECK PASS: cross-dir rename: parent handles DIFFER (from=57452ab60300000000000000 to=faae5ed90900000000000000)
```

Pairing identifier: NONE in the metadata. The fanotify metadata struct is
only {event_len, vers, reserved, metadata_len, mask, fd, pid} -- no inotify
style cookie. Same-dir rename FROM/TO carried identical parent handles and
arrived adjacent in one read, but adjacency is the only heuristic.

Test 5b -- the REAL pairing mechanism, FAN_RENAME (mask bit added at
runtime, PASS):

```
  EVENT mask=0x10000000 [RENAME] pid=25751 fd=-1 event_len=104 metadata_len=24 vers=3
    INFO type=OLD_DFID_NAME(10) len=40 ... handle=faae5ed9... name="xfile2"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch/dstdir
    INFO type=NEW_DFID_NAME(12) len=40 ... handle=57452ab6... name="xfile3"
      resolve(mount_fd=marked-fs)  -> /dev/shm/fanospike-scratch
CHECK PASS: Q5 pairing answer: ONE FAN_RENAME event carries OLD_DFID_NAME("xfile2") + NEW_DFID_NAME("xfile3")
```

Test 5c -- FAN_ONDIR requirement (second group marked CREATE|DELETE
without FAN_ONDIR):

```
CHECK PASS: no-ONDIR group still sees FILE create
CHECK PASS: Q5: no-ONDIR group sees NO mkdir/rmdir events -> FAN_ONDIR IS required for dir events
```

## Test 6: ignore marks -- ALL PASS, and NOT recursive (confirmed)

Legacy form `FAN_MARK_IGNORED_MASK|FAN_MARK_IGNORED_SURV_MODIFY` on
`noise/` with mask CREATE|DELETE|MOVED_FROM|MOVED_TO|ONDIR (no
FAN_EVENT_ON_CHILD needed):

```
T6a ignore mark on noise/ (...): PASS
-- drain[ignore-mark ops] --      # ops: noise/nfile +/-, sibling/sfile +/-, mkdir noise/deeper, noise/deeper/dfile +/-
  EVENT mask=0x300 [CREATE|DELETE] ... name="sfile"
  EVENT mask=0x300 [CREATE|DELETE] ... name="dfile"
CHECK PASS: entries DIRECTLY inside noise/ are suppressed (no nfile events)
CHECK PASS: sibling/ events still arrive (sfile create+delete seen)
CHECK PASS: mkdir noise/deeper (an entry IN noise/) is suppressed
CHECK PASS: CRITICAL: events in NESTED dir noise/deeper/ are NOT suppressed (dfile seen) -> ignore mark is NOT recursive
```

Newer form (kernel 6.0+) on `noise2/`:

```
T6b ignore mark (FAN_MARK_IGNORE_SURV, mask=dirent|ONDIR): PASS   # no EINVAL, no FAN_EVENT_ON_CHILD needed
CHECK PASS: FAN_MARK_IGNORE_SURV suppresses direct children of noise2/
CHECK PASS: sibling still reported under IGNORE_SURV form
```

So: an ignore mark on a directory suppresses dirent events for entries
directly inside it (including mkdir of a subdir), but a nested subdir
created afterwards reports normally -- exclusion of a subtree via one
ignore mark is impossible; both API forms behave identically for dirent
masks.

## Test 7: 1000 creates, coalescing -- PASS (no coalescing of distinct names)

```
created 1000 files in 16.0 ms
  drain[bulk-drain]: 1000 events in 1 read(s), 403.6 ms   # incl. the 400ms quiet-poll tail; the read itself ~3.6 ms
FAN_CREATE events for the 1000 files: 1000 (collected 1000 total events)
read() sizes: 64000  (reads=1, ... overflow=no)
CHECK PASS: one distinct event per created name (1000/1000) -- no name-level coalescing
CHECK PASS: no FAN_Q_OVERFLOW at 1000 queued events (max_queued_events=16384)
```

1000 distinct names = 1000 distinct events, 64 bytes each on the wire; ONE
256 KiB read returned all of them (64000 bytes). Only identical
(same-parent same-name) events merge, by OR-ing masks (see 4b/6).

## Test 8: mark scope = one superblock -- PASS

With the tmpfs superblock marked, a create on /tmp (the ext4 root fs) is
silent and a control create on the marked fs still arrives:

```
CHECK PASS: create on /tmp (different superblock) produced NO event
CHECK PASS: control: group still live, create on marked fs reported
```

(The inverse was also observed in `main.out`: with only tmpfs marked, ALL
scratch ops on ext4 produced zero events.)

## Test 9: registration cost -- microseconds

```
re-mark #1 (idempotent FAN_MARK_ADD|FAN_MARK_FILESYSTEM): OK, 8 us
re-mark #2 (idempotent FAN_MARK_ADD|FAN_MARK_FILESYSTEM): OK, 1 us
re-mark #3 (idempotent FAN_MARK_ADD|FAN_MARK_FILESYSTEM): OK, 1 us
FAN_MARK_REMOVE|FAN_MARK_FILESYSTEM: OK, 25 us
CHECK PASS: after FAN_MARK_REMOVE no further events arrive
```

## Test 10: capability drop (capsh) -- privilege boundary mapped

```
=== control: full caps ===
fanotify_init: OK fd=3
fanotify_mark(FAN_MARK_FILESYSTEM, "/dev/shm"): OK
fanotify_mark(FAN_MARK_MOUNT, FAN_OPEN, "/dev/shm"): OK
fanotify_mark(inode mark, dirent mask, "/dev/shm"): OK

=== capsh --drop=cap_sys_admin ===         # CapEff: 000001fffedfffff (bit 21 cleared)
fanotify_init: OK fd=3
fanotify_mark(FAN_MARK_FILESYSTEM, "/dev/shm"): FAILED errno=1 (Operation not permitted)
fanotify_mark(FAN_MARK_MOUNT, FAN_OPEN, "/dev/shm"): FAILED errno=1 (Operation not permitted)
fanotify_mark(inode mark, dirent mask, "/dev/shm"): OK
```

On 6.18, `fanotify_init` with FAN_REPORT_DFID_NAME succeeds WITHOUT
CAP_SYS_ADMIN, and even per-inode dirent marks work unprivileged -- but
FAN_MARK_FILESYSTEM (and FAN_MARK_MOUNT) fail with EPERM. The capability
requirement for whole-filesystem marks is confirmed empirically.

## Implications for the Go implementation

- PROVEN VIABLE: one fanotify group with FAN_CLASS_NOTIF|FAN_REPORT_DFID_NAME plus ONE FAN_MARK_FILESYSTEM mark per superblock delivers named CREATE/DELETE/MOVED_FROM/MOVED_TO (+ONDIR) for EVERY directory on that fs -- no per-directory watches, no inotify max_user_watches ceiling, microsecond registration, and mark removal is instant. This can replace the per-directory fsnotify watcher wholesale on the covered filesystems.
- fsid gate: fid-mode sb-marks return ENODEV on filesystems whose fsid is null (this VM's root ext4 has no UUID; normal desktop ext4 installs have one -- proven by the /opt/claude-code PASS). The Go backend must TRY the mark per superblock at startup and fall back to the existing fsnotify watcher for that fs on ENODEV/EXDEV/EPERM; statfs f_fsid==0 is the cheap pre-check.
- Privilege: FAN_MARK_FILESYSTEM needs CAP_SYS_ADMIN (EPERM otherwise, even as uid 0; init and per-inode marks do NOT). A user-session searchbar only gets this via setcap/capability ambient inheritance or a privileged helper -- so fanotify is an opt-in fast path, never the only path.
- Path resolution: keep one O_PATH/O_RDONLY "mount fd" per marked superblock and route each event by its fsid; open_by_handle_at + readlink(/proc/self/fd/N) reliably returns the parent dir's CURRENT absolute path. A wrong-fs mount fd fails ESTALE, so fsid->mountfd routing is mandatory, not optional.
- Handles are inode-identity, not path-identity: after a dir rename the old handle resolves to the NEW path (resolution is always current truth -- great for keeping the index consistent), and after dir deletion resolution fails ESTALE. Cache dir-handle-bytes -> path in a map to avoid a syscall per event, invalidate on ONDIR rename/delete, and treat ESTALE as "entry's parent already gone" (drop the event; the corresponding DELETE events for the subtree are already in the queue).
- Renames: MOVED_FROM (old parent handle + old name) and MOVED_TO (new parent handle + new name) both arrive but carry NO pairing cookie. Subscribe FAN_RENAME too: it delivers OLD_DFID_NAME + NEW_DFID_NAME atomically in ONE event -- use it as the primary rename signal (move the index subtree) and treat unpaired MOVED_* as delete/create fallbacks.
- FAN_ONDIR must be in the mark mask or mkdir/rmdir/dir-rename events are silently absent; conversely the event's FAN_ONDIR bit is a free isDir classification (index.Add needs no lstat for that bit -- but see the merging caveat).
- Merging: same-name-same-dir events OR their masks (observed a single event with CREATE|DELETE after a quick create+unlink). Op order is unrecoverable, so the consumer must reconcile final state (lstat the resolved path; existence decides add vs remove) -- which matches the existing watcher's ordered-batch "final state wins" design, just keyed on merged masks instead of event order.
- Ignore marks canNOT express the Excluder: they are per-inode and NOT recursive (direct children of the marked dir are suppressed, a nested subdir created later reports normally; identical behavior on legacy IGNORED_MASK and 6.0+ IGNORE_SURV forms). Keep exclusion filtering in userspace exactly like today; ignore marks are only worth it for a handful of FLAT high-churn dirs, if ever.
- Throughput/queue: no coalescing of distinct names (1000 creates = 1000 events, 64 B each; one 256 KiB read drained all 1000 in ~4 ms), default queue is 16384 events (per /proc/sys/fs/fanotify/max_queued_events) and overflow raises FAN_Q_OVERFLOW -- keep the existing overflow->rescan reconcile path, drain with a dedicated goroutine + big buffer, and consider FAN_UNLIMITED_QUEUE (also CAP_SYS_ADMIN) for burst storms; multi-fs coverage = enumerate superblocks (mounts.go already parses /proc/self/mounts) and mark each, remembering that a weak-fsid (null) filesystem cannot share a group with any other fs (EXDEV) -- give such a fs its own group or the fsnotify fallback.
