# fanotify fact sheet -- whole-filesystem file-change watcher in Go

Scope: Linux fanotify as a notification-only (no permission events) engine for
a whole-filesystem create/delete/move/modify watcher, consumed from Go via
golang.org/x/sys/unix. Target kernels: Ubuntu 22.04 (5.15 GA series) and
Ubuntu 24.04 (6.8 GA).

## Sources and citation keys

- `fanotify(7)`, `fanotify_init(2)`, `fanotify_mark(2)`, `open_by_handle_at(2)`,
  `inotify(7)`: Linux man-pages 6.18 as published on man7.org
  (https://man7.org/linux/man-pages/man7/fanotify.7.html,
  https://man7.org/linux/man-pages/man2/fanotify_init.2.html,
  https://man7.org/linux/man-pages/man2/fanotify_mark.2.html,
  https://man7.org/linux/man-pages/man2/open_by_handle_at.2.html,
  https://man7.org/linux/man-pages/man7/inotify.7.html). Cited as
  "man page, SECTION, entry".
- Kernel source at tags v5.15 and v6.8 from
  git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git (`/plain/<path>?h=<tag>`),
  cited as `<path> @vX.Y:line` (line numbers verified against the fetched files).
- golang.org/x/sys at tag v0.46.0 (the version pinned by this repo's go.mod line 14),
  source github.com/golang/sys, cited as `unix/<file>.go:line @v0.46.0`.
- LWN, "Support more filesystems with FAN_REPORT_FID": https://lwn.net/Articles/948112/
- Ubuntu jammy kernel changelog: https://launchpad.net/ubuntu/jammy/+source/linux/+changelog

---

## 1. fanotify_init flags for a notification-only listener

Recommended init call shape:
`fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_NONBLOCK | <report flags>, O_RDONLY | O_LARGEFILE | O_CLOEXEC)`.

- `FAN_CLASS_NOTIF` is the default class (value 0x0) and "only allows the
  receipt of events notifying that a file has been accessed. Permission
  decisions ... are not possible" (fanotify_init(2), DESCRIPTION,
  FAN_CLASS_NOTIF). The two permission classes (`FAN_CLASS_CONTENT`,
  `FAN_CLASS_PRE_CONTENT`) each "require the CAP_SYS_ADMIN capability"
  (fanotify_init(2), same section) -- irrelevant here.
- FID-identifying groups MUST be `FAN_CLASS_NOTIF`: "The use of
  FAN_CLASS_CONTENT or FAN_CLASS_PRE_CONTENT is not permitted with this flag
  and will result in the error EINVAL" (fanotify_init(2), FAN_REPORT_FID entry);
  kernel check `if (fid_mode && class != FAN_CLASS_NOTIF) return -EINVAL`
  (fs/notify/fanotify/fanotify_user.c @v5.15:1200).
- `FAN_NONBLOCK` sets O_NONBLOCK; reads return EAGAIN when empty
  (fanotify_init(2), DESCRIPTION). `FAN_CLOEXEC` sets FD_CLOEXEC (ibid.).
- `event_f_flags` only governs fds opened for events; in pure FID mode events
  carry no fd (`fd` is FAN_NOFD -- fanotify(7), fanotify_event_metadata `fd`
  field description), so these flags are nearly moot; `O_LARGEFILE` avoids
  EOVERFLOW on >2GB files on 32-bit if fd-mode is ever mixed in
  (fanotify_init(2), event_f_flags).

### The report-FID family

| Flag | Since | What it adds |
|---|---|---|
| `FAN_REPORT_FID` | Linux 5.1 | Info record `FAN_EVENT_INFO_TYPE_FID` identifying the event's object; the event fd is "substituted with a file handle". Required (any fid mode is) for the dirent events FAN_CREATE/DELETE/MOVE/RENAME and for FAN_ATTRIB/DELETE_SELF/MOVE_SELF. Without FAN_REPORT_TARGET_FID, dirent events' record identifies "the modified directory and not the created/deleted/moved child object". (fanotify_init(2), FAN_REPORT_FID entry.) |
| `FAN_REPORT_DIR_FID` | Linux 5.9 | Record `FAN_EVENT_INFO_TYPE_DFID`: handle of the directory object -- for events on a non-directory, the parent directory. Combined with FAN_REPORT_FID, two records (child FID + parent DFID) for non-dirent events. An unlinked-but-open file has no parent: with FAN_REPORT_FID one record; without it "no event will be reported". (fanotify_init(2), FAN_REPORT_DIR_FID entry.) |
| `FAN_REPORT_NAME` | Linux 5.9 | Must accompany FAN_REPORT_DIR_FID (else EINVAL). Replaces DFID with `FAN_EVENT_INFO_TYPE_DFID_NAME`: directory file handle followed by a name. For FAN_CREATE/DELETE/MOVE the name is "that of the created/deleted/moved directory entry". Events on a directory itself: the dir's own handle + name ".". FAN_RENAME gets two records, `FAN_EVENT_INFO_TYPE_OLD_DFID_NAME` and `..._NEW_DFID_NAME`. (fanotify_init(2), FAN_REPORT_NAME entry.) |
| `FAN_REPORT_DFID_NAME` | -- | Synonym for `FAN_REPORT_DIR_FID\|FAN_REPORT_NAME` (fanotify_init(2)). |
| `FAN_REPORT_TARGET_FID` | Linux 5.17, 5.15.154, 5.10.220 | Must accompany FID+DIR_FID+NAME (else EINVAL). Dirent events (CREATE/DELETE/MOVE/RENAME) get an ADDITIONAL `FAN_EVENT_INFO_TYPE_FID` record with the handle of the child object the entry refers to. (fanotify_init(2), FAN_REPORT_TARGET_FID entry -- the stable-series versions are printed in the man page itself.) |
| `FAN_REPORT_DFID_NAME_TARGET` | -- | Synonym for `FAN_REPORT_DFID_NAME\|FAN_REPORT_FID\|FAN_REPORT_TARGET_FID` (fanotify_init(2)). |

For an index watcher, `FAN_REPORT_DFID_NAME` is the workhorse: every dirent
event names (parent-dir handle, entry name) -- exactly the (dir, name) key an
index add/remove needs. FAN_REPORT_TARGET_FID additionally identifies the
child inode (useful to correlate MOVED_FROM/MOVED_TO), at the cost of one
extra record per event and 5.17+/backport availability.

### Wire layouts (all from fanotify(7), "Reading fanotify events")

```c
struct fanotify_event_metadata {          /* 24 bytes; FAN_EVENT_METADATA_LEN */
    __u32 event_len;      /* total size incl. info records = offset to next */
    __u8  vers;           /* compare to FANOTIFY_METADATA_VERSION (3) */
    __u8  reserved;
    __u16 metadata_len;
    __aligned_u64 mask;
    __s32 fd;             /* FAN_NOFD in fid mode / on queue overflow */
    __s32 pid;            /* pid (or tid with FAN_REPORT_TID) of the actor */
};
struct fanotify_event_info_header { __u8 info_type; __u8 pad; __u16 len; };
struct fanotify_event_info_fid {
    struct fanotify_event_info_header hdr;   /* 4 bytes */
    __kernel_fsid_t fsid;                    /* 8 bytes: same value as statfs f_fsid */
    unsigned char   handle[];                /* struct file_handle, variable */
};
```

The embedded handle is a `struct file_handle { unsigned int handle_bytes; int
handle_type; unsigned char f_handle[]; }` (open_by_handle_at(2), DESCRIPTION),
and "If the value of info_type field is FAN_EVENT_INFO_TYPE_DFID_NAME, the
file handle is followed by a null terminated string" naming the entry
(fanotify(7), `handle` field description). The man-page example computes the
name pointer as `file_handle->f_handle + file_handle->handle_bytes`
(fanotify(7), fanotify_fid.c example). `hdr.len` is the whole record size, and
the sum of records is bounded by `event_len - metadata_len` (fanotify(7),
`len` field). Records may be stacked and "fanotify provides no guarantee
around the ordering of information records" (fanotify(7), "Reading fanotify
events") -- always dispatch on `info_type`, never on position.

Info-type values (uapi, mirrored in x/sys `unix/zerrors_linux.go:1302-1310
@v0.46.0`): FID=0x1, DFID_NAME=0x2, DFID=0x3, PIDFD=0x4, ERROR=0x5, RANGE=0x6,
MNT=0x7, OLD_DFID_NAME=0xa, NEW_DFID_NAME=0xc.

---

## 2. Directory-entry (dirent) event mask bits

All from fanotify_mark(2), DESCRIPTION (mask list), which also states for each
that "An fanotify group that identifies filesystem objects by file handles is
required":

| Bit | Since | Meaning |
|---|---|---|
| `FAN_CREATE` | 5.1 | file/dir created in a marked parent directory |
| `FAN_DELETE` | 5.1 | file/dir deleted in a marked parent directory |
| `FAN_MOVED_FROM` | 5.1 | file/dir moved FROM a marked parent directory |
| `FAN_MOVED_TO` | 5.1 | file/dir moved TO a marked parent directory |
| `FAN_RENAME` | 5.17, 5.15.154, 5.10.220 | one event carrying the same info as MOVED_FROM+MOVED_TO; mark target must be a directory (ENOTDIR) |
| `FAN_DELETE_SELF` | 5.1 | the marked object itself was deleted |
| `FAN_MOVE_SELF` | 5.1 | the marked object itself was moved |
| `FAN_ATTRIB` | 5.1 | metadata changed |
| `FAN_MOVE` | -- | composite `FAN_MOVED_FROM\|FAN_MOVED_TO` |

- `FAN_ONDIR` in the MARK mask is required to get dirent events for
  subdirectory entries: "In the context of directory entry events ...
  specifying the flag FAN_ONDIR is required in order to create events when
  subdirectory entries are modified (i.e., mkdir(2)/rmdir(2))"
  (fanotify_mark(2), FAN_ONDIR). In the EVENT mask, FAN_ONDIR is reported (fid
  groups only) so mkdir/rmdir can be told apart from creat/unlink (fanotify(7),
  FAN_ONDIR entry; fs/notify/fanotify/fanotify.c @v5.15:313-320 comment).
- With `FAN_REPORT_DFID_NAME`, a dirent event carries exactly one
  DFID_NAME record: parent directory file handle + NUL-terminated entry name
  (fanotify_init(2), FAN_REPORT_NAME entry; fanotify(7), `handle` field). The
  child object is identified only if `FAN_REPORT_TARGET_FID` is set
  (fanotify(7), `hdr` field: "an information record identifying the
  created/deleted/moved child object is reported only if ... FAN_REPORT_TARGET_FID").
- `FAN_DELETE_SELF`/`FAN_MOVE_SELF` are events on the object itself, not the
  parent. Under a filesystem mark every inode is effectively marked, so these
  duplicate DELETE/MOVED_FROM per object; note "the events FAN_DELETE_SELF and
  FAN_MOVE_SELF are not generated for children of marked directories"
  (fanotify_mark(2), FAN_EVENT_ON_CHILD) -- they only fire via
  filesystem/mount/inode marks on the object. For an index watcher the four
  parent-side dirent bits + FAN_ONDIR are the primary signal.

### Rename pairing: there is NO cookie

- `struct fanotify_event_metadata` has no cookie field -- fields are event_len,
  vers, reserved, metadata_len, mask, fd, pid, nothing else (fanotify(7),
  "Reading fanotify events"). inotify, by contrast, has `cookie`, "a unique
  integer that connects related events ... allows the resulting pair of
  IN_MOVED_FROM and IN_MOVED_TO events to be connected" (inotify(7),
  DESCRIPTION, cookie field).
- Therefore pre-FAN_RENAME, `FAN_MOVED_FROM`/`FAN_MOVED_TO` must be treated as
  an independent delete + create. There is no documented pairing mechanism;
  even in inotify, where a cookie exists, the pair is "not guaranteed" to be
  consecutive and matching "is thus inherently racy" (inotify(7), NOTES,
  "Dealing with rename() events") -- fanotify without a cookie is strictly
  worse for pairing.
- Partial mitigation on 5.17+/backports: `FAN_RENAME` delivers ONE event with
  up to two records, OLD_DFID_NAME + NEW_DFID_NAME (fanotify_init(2),
  FAN_REPORT_NAME entry; fanotify_mark(2), FAN_RENAME entry), i.e. the kernel
  itself pairs old and new (dir handle, name). With FAN_REPORT_TARGET_FID the
  child FID record is added too (fanotify_init(2), FAN_REPORT_TARGET_FID).
  Note FAN_RENAME does not replace MOVED_FROM/MOVED_TO -- you may subscribe
  either or both.
- Treating MOVED_FROM/MOVED_TO as remove+add is semantically safe for an
  index (matches the "create-then-delete ends deleted" ordered-batch model);
  the only cost is losing move identity.

---

## 3. FAN_MARK_FILESYSTEM (and FAN_MARK_MOUNT)

- Since Linux 4.20. "Mark the filesystem specified by path. The filesystem
  containing path will be marked. All the contained files and directories of
  the filesystem from any mount point will be monitored. Use of this flag
  requires the CAP_SYS_ADMIN capability." (fanotify_mark(2),
  FAN_MARK_FILESYSTEM.)
- Privilege, precisely: the kernel checks `capable(CAP_SYS_ADMIN)` at
  fanotify_mark() time AND rejects mount/filesystem marks on groups that were
  created unprivileged: "An unprivileged user is not allowed to setup mount
  nor filesystem marks. This also includes setting up such marks by a group
  that was initialized by an unprivileged user." with
  `if ((!capable(CAP_SYS_ADMIN) || FAN_GROUP_FLAG(group, FANOTIFY_UNPRIV)) &&
  mark_type != FAN_MARK_INODE) -> EPERM`
  (fs/notify/fanotify/fanotify_user.c @v5.15:1430-1439; same check
  @v6.8:1830-1832). `capable()` is defined as
  `ns_capable(&init_user_ns, cap)` (kernel/capability.c @v6.8:434-436), so
  this is CAP_SYS_ADMIN **in the initial user namespace** -- root inside a
  user-namespace container does not qualify.
- Scope: one mark covers exactly one filesystem instance (superblock), across
  ALL of its mounts -- "from any mount point" (fanotify_mark(2),
  FAN_MARK_FILESYSTEM). That includes bind mounts of the same superblock and
  mounts of that superblock created after the mark (the mark is on the
  filesystem object, not a mount; fanotify(7), DESCRIPTION: group entries
  refer "to files and directories via their inode number and to mounts via
  their mount ID" -- a filesystem mark pins the sb). Consequence for a
  multi-filesystem machine: **one FAN_MARK_FILESYSTEM mark per superblock**
  (/, /home, /boot on separate partitions = three marks), all addable to one
  fanotify group; events carry `fsid` to distinguish origin (fanotify(7),
  fsid field).
- Filesystems mounted later that create a NEW superblock (USB stick, new
  loop mount) are NOT covered; the watcher must notice the mount and add a
  mark. Kernel 6.14 adds `FAN_MARK_MNTNS` + `FAN_MNT_ATTACH`/`FAN_MNT_DETACH`
  for exactly this (fanotify_mark(2), FAN_MARK_MNTNS and FAN_MNT_ATTACH
  entries); on 5.15/6.8 poll /proc/self/mountinfo instead.
- Since 6.8, marking is stricter at mark time: ENODEV "when trying to add a
  mount or filesystem mark" on a zero-fsid filesystem, and EXDEV for btrfs
  subvolumes / mixing marks across filesystems where one reports zero fsid
  (fanotify_mark(2), ERRORS, ENODEV and EXDEV entries -- both marked "Since
  Linux 6.8" for these cases).
- `FAN_MARK_MOUNT` differences: (a) covers a single vfsmount only -- "A
  listener that marked a mount will be notified only of events that were
  triggered for a filesystem object using the same mount. Any other event will
  pass unnoticed" (fanotify(7), BUGS -- bind mounts dodge it); (b) dirent and
  other fid-class events are FORBIDDEN on mount marks: "The events which
  require that filesystem objects are identified by file handles, such as
  FAN_CREATE, FAN_ATTRIB, FAN_MOVE, and FAN_DELETE_SELF, cannot be provided as
  a mask when flags contains FAN_MARK_MOUNT. Attempting to do so will result
  in the error EINVAL" (fanotify_mark(2), FAN_MARK_MOUNT); kernel comment:
  "inode events are not supported on a mount mark, because they do not carry
  enough information (i.e. path) to be filtered by mount point"
  (fanotify_user.c @v5.15:1451-1460); (c) also requires CAP_SYS_ADMIN
  (fanotify_mark(2), FAN_MARK_MOUNT). Verdict: mount marks cannot power a
  create/delete watcher at all; FAN_MARK_FILESYSTEM is the only whole-tree
  race-free option (fanotify(7), NOTES: "Monitoring filesystems offers the
  capability to monitor changes made from any mount of a filesystem instance
  in a race-free manner").

---

## 4. Unprivileged fanotify (Linux 5.13+): confirmed useless for whole-FS watching

Since Linux 5.13 (and 5.10.220) fanotify_init() no longer requires
CAP_SYS_ADMIN, with these documented limitations (fanotify_init(2), VERSIONS):

- No `FAN_UNLIMITED_QUEUE`, no `FAN_UNLIMITED_MARKS`.
- No `FAN_CLASS_CONTENT`/`FAN_CLASS_PRE_CONTENT` (no permission events).
- The group is "required to ... identif[y] filesystem objects by file
  handles, for example, by providing the FAN_REPORT_FID flag" -- i.e. FID mode
  is MANDATORY, not merely allowed. Kernel: an unprivileged caller gets EPERM
  unless `fid_mode` is set and no admin-only init flags are present:
  `if ((flags & FANOTIFY_ADMIN_INIT_FLAGS) || !fid_mode) return -EPERM`
  (fanotify_user.c @v5.15:1155-1163).
- "The user is limited to only mark inodes. The ability to mark a mount or
  filesystem via fanotify_mark() through the use of FAN_MARK_MOUNT or
  FAN_MARK_FILESYSTEM is not permitted." (fanotify_init(2), VERSIONS; enforced
  via the internal FANOTIFY_UNPRIV group flag, fanotify_user.c
  @v5.15:1165-1170 and 1430-1439.)
- Events are degraded: the FANOTIFY_UNPRIV flag "prevents setting
  mount/filesystem marks on this group and prevents reporting pid and open fd
  in events" (fanotify_user.c @v5.15:1166-1169 comment); the man page: the
  user "will also not receive the pid that generated the event, unless the
  listening process itself generated the event" (fanotify_init(2), VERSIONS).

Why this cannot do whole-filesystem watching:

1. Inode marks only -> recursive coverage requires marking every directory,
   re-introducing exactly the raciness fanotify's filesystem marks exist to
   fix: "Monitoring ... directories is not recursive ... This approach is
   racy" (fanotify(7), NOTES, "Limitations and caveats").
2. Mark counts are capped (`/proc/sys/fs/fanotify/max_user_marks`; pre-5.13
   hardcoded 8192 per group -- fanotify(7), "/proc interfaces") and
   FAN_UNLIMITED_MARKS is unavailable; a multi-million-directory filesystem
   cannot be covered.
3. The mandatory FID events identify objects by file handle, but resolving a
   handle needs open_by_handle_at(), which needs CAP_DAC_READ_SEARCH (section
   5) -- an unprivileged listener cannot turn handles into paths (it can only
   compare them against handles it computes itself with name_to_handle_at).

Conclusion: as expected, unprivileged fanotify is NOT usable for a
whole-filesystem watcher. A real deployment needs CAP_SYS_ADMIN +
CAP_DAC_READ_SEARCH in the initial user namespace (in practice: root, or a
systemd service with those two ambient capabilities).

---

## 5. open_by_handle_at: privilege, path resolution, failure modes

- "The caller must have the CAP_DAC_READ_SEARCH capability to invoke
  open_by_handle_at()" (open_by_handle_at(2), DESCRIPTION; EPERM in ERRORS).
  The kernel check is plain `capable(CAP_DAC_READ_SEARCH)` (fs/fhandle.c
  @v5.15:179, identical @v6.8:182), and `capable()` tests the INITIAL user
  namespace (kernel/capability.c @v6.8:434-436). So on both target kernels:
  initial-userns capability, container userns-root insufficient.
- `mount_fd` is "a file descriptor for any object (file, directory, etc.) in
  the mounted filesystem with respect to which handle should be interpreted"
  (open_by_handle_at(2), DESCRIPTION). Practical pattern: when adding each
  FAN_MARK_FILESYSTEM mark, keep one long-lived `O_PATH`/`O_RDONLY` fd of that
  superblock's root and select it by the event's `fsid` (fanotify(7), fsid
  field: same value as statfs f_fsid).
- Handle -> path: `open_by_handle_at(mount_fd, handle, O_RDONLY)` then
  `readlink("/proc/self/fd/<N>")` -- this is literally the man-page example
  flow (fanotify(7), EXAMPLES, fanotify_fid.c: open_by_handle_at then
  readlink of /proc/self/fd). For dirent events the handle is the PARENT
  directory; resolve it, then use the entry name from the DFID_NAME record
  with `fstatat(event_fd, name, ...)` (rationale spelled out in
  fanotify_init(2), FAN_REPORT_NAME: "the reported directory file handle can
  be passed to open_by_handle_at(2) to get an open directory file descriptor
  and that file descriptor along with the reported name can be used to call
  fstatat(2)").
- Failure modes:
  - `ESTALE`: "The specified handle is not valid for opening a file. This
    error will occur if, for example, the file has been deleted"
    (open_by_handle_at(2), ERRORS); "A file handle may become invalid
    ('stale') if a file is deleted, or for other filesystem-specific reasons"
    (ibid., NOTES). For FAN_DELETE/FAN_DELETE_SELF this is the EXPECTED
    outcome and must be handled as "object gone", per the example's explicit
    ESTALE branch (fanotify(7), fanotify_fid.c comments). ESTALE also occurs
    for identify-only handles (AT_HANDLE_FID-class, see section 10) on
    filesystems that cannot decode them (open_by_handle_at(2), ERRORS,
    ESTALE).
  - `ELOOP` if the handle is a symlink and O_PATH was not given
    (open_by_handle_at(2), ERRORS); symlink handles must be opened O_PATH
    (ibid., DESCRIPTION).
  - `EINVAL` handle_bytes > MAX_HANDLE_SZ or zero; `EBADF` bad mount_fd
    (ibid., ERRORS).
- Staleness of names: "there is no guarantee that the filesystem object will
  be found at the location described by the directory entry information at
  the time the event is received" (fanotify_init(2), FAN_REPORT_NAME). Every
  event must be reconciled with an lstat/fstatat of the resolved (dir, name);
  ENOENT there means "already gone again" (fanotify(7) example tolerates
  exactly that).
- Self-echo suppression: compare event `pid` with getpid() to skip events the
  watcher itself causes (fanotify(7), pid field discussion). Fds delivered by
  fanotify carry FMODE_NONOTIFY so USING them generates no events (fanotify(7),
  fd field) -- but fds you open via open_by_handle_at are ordinary and DO
  generate events, hence the pid filter matters.

---

## 6. Queue semantics

- Default queue depth is 16384 events: `/proc/sys/fs/fanotify/max_queued_events`
  ("Prior to Linux kernel 5.13, the hardcoded limit was 16384 events" --
  fanotify(7), "/proc interfaces"; the post-5.13 tunable's default is the
  same constant: `#define FANOTIFY_DEFAULT_MAX_EVENTS 16384`, fanotify_user.c
  @v5.15:30 and @v6.8:30, assigned to `fanotify_max_queued_events`
  @v5.15:1603).
- `FAN_UNLIMITED_QUEUE` removes the limit; requires CAP_SYS_ADMIN
  (fanotify_init(2), FAN_UNLIMITED_QUEUE). For a whole-FS watcher prefer the
  bounded queue + rescan-on-overflow: an unlimited queue converts bursts into
  unbounded kernel memory.
- Overflow delivery: "Events in excess of this limit are dropped, but an
  FAN_Q_OVERFLOW event is always generated" (fanotify(7), "/proc
  interfaces"). It arrives as a normal `fanotify_event_metadata` whose mask
  contains `FAN_Q_OVERFLOW` and whose fd is FAN_NOFD ("or FAN_NOFD if a queue
  overflow occurred" -- fanotify(7), fd field; with FAN_REPORT_FD_ERROR the
  value is -EBADF instead, fanotify(7), fd field). "The event queue can
  overflow. In this case, events are lost." (fanotify(7), NOTES.) Correct
  response: full reconcile rescan.
- Merging/coalescing: "consecutive events for the same filesystem object and
  originating from the same process may be merged into a single event, with
  the exception that two permission events are never merged" (fanotify(7),
  event mask discussion). Kernel mechanics @v5.15
  (fs/notify/fanotify/fanotify.c):
  - merge candidates are found via a hash table over the queue, scanning at
    most `FANOTIFY_MAX_MERGE_EVENTS` (128) bucket entries (lines 151-152,
    171-183) -- so merging is not limited to the literally adjacent event;
  - events merge only with same hash, same type, same pid, and equal ISDIR
    flag -- the ISDIR guard exists so "user won't be able to tell ... mkdir+
    unlink pair or rmdir+create pair" cannot happen (lines 114-131 comment);
    FID_NAME events additionally require identical fsid + dir handle + name
    (lines 100-112);
  - merging ORs the masks: `old->mask |= new->mask` (line 178). Consequence:
    one event can arrive with mask `FAN_CREATE|FAN_DELETE` for the same name,
    and the EVENT ORDER WITHIN THE MASK IS LOST -- the consumer cannot infer
    final state from the event and must lstat the (dir, name) to decide.
- Contrast inotify: identical successive events coalesce only if "same wd,
  mask, cookie, and name" and the older is unread (inotify(7), NOTES) -- 
  inotify never ORs different masks together; fanotify does.

---

## 7. Ignore marks

- Model: every group entry has a mark mask and an ignore mask; the design goal
  is "a filesystem, mount, or directory to be marked for receiving events,
  while at the same time ignoring events for specific objects under a mount
  or directory" (fanotify(7), DESCRIPTION).
- `FAN_MARK_IGNORED_MASK` (legacy, always available): adds mask bits to the
  ignore mask. "Note that the flags FAN_ONDIR, and FAN_EVENT_ON_CHILD have no
  effect when provided with this flag. The effect of setting [them] ... is
  undefined and depends on the Linux kernel version. Specifically, prior to
  Linux 5.9, setting a mark mask on a file and a mark with ignore mask on its
  parent directory would not result in ignoring events on the file"
  (fanotify_mark(2), FAN_MARK_IGNORED_MASK).
- `FAN_MARK_IGNORE` (since Linux 6.0, 5.15.154, 5.10.220): same effect but
  the flags DO take effect in the ignore mask -- "unless the FAN_ONDIR flag is
  set with FAN_MARK_IGNORE, events on directories will not be ignored. If the
  flag FAN_EVENT_ON_CHILD is set ... events on children will be ignored"; and
  on "a mount, filesystem, or directory inode mark, the
  FAN_MARK_IGNORED_SURV_MODIFY flag must be specified" (else EINVAL/EISDIR)
  (fanotify_mark(2), FAN_MARK_IGNORE). `FAN_MARK_IGNORE_SURV` =
  IGNORE|IGNORED_SURV_MODIFY (ibid.). Mixing the two APIs on one mark fails
  EEXIST in both directions (fanotify_mark(2), FAN_MARK_IGNORED_MASK and
  ERRORS; kernel comment fanotify_user.c @v6.8:1139-1146).
- `FAN_MARK_IGNORED_SURV_MODIFY`: without it "the ignore mask is cleared when
  a modify event occurs on the marked object"; it "cannot be removed from a
  mark once set" (fanotify_mark(2), FAN_MARK_IGNORED_SURV_MODIFY). Always set
  it for directory-exclusion marks.

### Can an inode ignore mark on a directory drop FAN_CREATE/FAN_DELETE there, under a filesystem mark? YES.

- Dirent events are events on the parent directory object (FAN_CREATE =
  "created in a marked parent directory", fanotify_mark(2)), so the parent
  dir's ignore mask governs them.
- v5.15 event path: the group ORs `ignored_mask` from EVERY matching mark --
  inode, mount, and sb -- with the comment "Apply ignore mask regardless of
  ISDIR and ON_CHILD flags", then computes
  `test_mask = event_mask & marks_mask & ~marks_ignored_mask`
  (fs/notify/fanotify/fanotify.c @v5.15:285-311). So on Ubuntu 22.04's 5.15,
  an inode mark on /some/dir with
  `FAN_MARK_ADD|FAN_MARK_IGNORED_MASK|FAN_MARK_IGNORED_SURV_MODIFY` and mask
  `FAN_CREATE|FAN_DELETE|FAN_MOVED_FROM|FAN_MOVED_TO` suppresses those events
  originating in that directory even though the FAN_MARK_FILESYSTEM mark
  requests them -- and because ISDIR is disregarded for legacy ignore masks,
  subdirectory-entry events (mkdir/rmdir) are suppressed too.
- The 6.8 kernel codifies the legacy semantics explicitly: without the new
  ignore-flags mode a mark's effective ignore mask means "Always ignore
  events on dir" and "Ignore events on child if parent is watching children"
  (include/linux/fsnotify_backend.h @v6.8:667-685,
  fsnotify_ignore_mask()). With FAN_MARK_IGNORE
  (FSNOTIFY_MARK_FLAG_HAS_IGNORE_FLAGS) the flags you set are honored as-is
  (ibid.; fanotify_user.c @v6.8:1139-1146).
- Kernel-version subtlety asked about: the parent-dir-ignore-mark vs
  FAN_EVENT_ON_CHILD interaction was broken before 5.9 -- a parent dir's
  ignore mask did NOT suppress events on its children regardless of
  FAN_EVENT_ON_CHILD (fanotify_mark(2), FAN_MARK_IGNORED_MASK, "prior to
  Linux 5.9"). Both target kernels (5.15, 6.8) postdate the fix. For DIRENT
  events specifically this subtlety is moot: they are events on the directory
  itself, not "events on child" -- the child-related legacy rule affects
  ignoring things like FAN_MODIFY of files via their parent's ignore mark.
- NOT recursive: an ignore mark covers that directory (and, per the rules
  above, its immediate children) -- events in sub-subdirectories under a
  filesystem mark are their own dirs' events. Kernel-side pruning of a large
  excluded subtree therefore needs an ignore mark per directory. That is the
  documented purpose of `FAN_MARK_EVICTABLE` (since 5.19, 5.15.154,
  5.10.220): set ignore marks lazily "on the directory" when uninteresting
  events arrive, without pinning inodes ("Evictable inode marks allow using
  this method for a large number of directories without the concern of
  pinning all inodes and exhausting the system's memory" -- fanotify_mark(2),
  FAN_MARK_EVICTABLE; evictable marks can vanish under memory pressure, so
  treat them as a cache). Evictable cannot be combined with mount/filesystem
  marks themselves (EINVAL, ibid.).
- Practical recipe (5.15-safe): filesystem mark with the dirent mask; for each
  configured exclude dir that exists, add
  `FAN_MARK_IGNORED_MASK|FAN_MARK_IGNORED_SURV_MODIFY` inode marks; on
  6.0+/backports prefer `FAN_MARK_IGNORE_SURV` with explicit
  `FAN_ONDIR|FAN_EVENT_ON_CHILD` in the ignore mask for deterministic
  semantics. Userspace filtering must remain as backstop for subtrees.

---

## 8. golang.org/x/sys/unix surface at v0.46.0 (repo-pinned version)

Wrappers (all linux):

- `func FanotifyInit(flags uint, event_f_flags uint) (fd int, err error)` --
  `unix/syscall_linux.go:50 @v0.46.0` (//sys directive).
- `func FanotifyMark(fd int, flags uint, mask uint64, dirFd int, pathname
  string) (err error)` -- `unix/syscall_linux.go:53-62 @v0.46.0`. An empty
  `pathname` passes NULL, i.e. "dirfd defines the filesystem object to be
  marked" (matches fanotify_mark(2), path resolution rules) -- so you can mark
  by fd alone.

Structs / constants present:

- `type FanotifyEventMetadata struct { Event_len uint32; Vers uint8; Reserved
  uint8; Metadata_len uint16; Mask uint64; Fd int32; Pid int32 }` --
  `unix/ztypes_linux.go:2672-2680 @v0.46.0`.
- `type FanotifyResponse struct { Fd int32; Response uint32 }` --
  `unix/ztypes_linux.go:2682-2685 @v0.46.0` (unused in notification-only mode).
- `FANOTIFY_METADATA_VERSION = 0x3` -- `unix/zerrors_linux.go:1274 @v0.46.0`;
  `FAN_EVENT_METADATA_LEN = 0x18` (24) -- `unix/zerrors_linux.go:1311`.
- `type Fsid struct { Val [2]int32 }` -- `unix/ztypes_linux.go:128 @v0.46.0`
  (the __kernel_fsid_t shape; compare both words to route events to sbs).
- The FAN_* constant set is COMPLETE through kernel 6.14: all 86 FAN_*
  constants at `unix/zerrors_linux.go:1275-1360 @v0.46.0`, including
  FAN_CLASS_NOTIF, FAN_CLOEXEC/NONBLOCK, FAN_REPORT_FID/DIR_FID/NAME/
  DFID_NAME (0xc00)/TARGET_FID/DFID_NAME_TARGET (0x1e00)/PIDFD/FD_ERROR/MNT,
  FAN_MARK_ADD/REMOVE/FLUSH/INODE/MOUNT/FILESYSTEM/MNTNS/ONLYDIR/DONT_FOLLOW/
  IGNORED_MASK/IGNORED_SURV_MODIFY/IGNORE/IGNORE_SURV/EVICTABLE,
  FAN_CREATE/DELETE/DELETE_SELF/MOVED_FROM/MOVED_TO/MOVE/MOVE_SELF/RENAME/
  ATTRIB/MODIFY/CLOSE_WRITE/ONDIR/EVENT_ON_CHILD, FAN_Q_OVERFLOW, FAN_NOFD,
  FAN_EVENT_INFO_TYPE_{FID,DFID,DFID_NAME,OLD_DFID_NAME,NEW_DFID_NAME,PIDFD,
  ERROR,RANGE,MNT}.

MISSING at v0.46.0 (must be hand-rolled in the watcher package):

- NO `FanotifyEventInfoHeader` / `FanotifyEventInfoFid` (or pidfd/error/range)
  struct definitions -- the generator only maps the metadata and response
  structs (`unix/linux/types.go:2673-2677 @v0.46.0`). The info records after
  each metadata block must be parsed manually from the byte layout in section
  1: header {u8,u8,u16}, then Fsid{[2]int32}, then {handle_bytes uint32,
  handle_type int32, f_handle...}, then (for *NAME types) a NUL-terminated
  name.
- NO FAN_EVENT_OK / FAN_EVENT_NEXT (C macros; iterate with Event_len
  yourself) and no FANOTIFY_METADATA_VERSION check helper (compare Vers
  manually).
- NO `MAX_HANDLE_SZ`, no `AT_HANDLE_FID` / `AT_HANDLE_MNT_ID_UNIQUE`
  constants (grep of zerrors_linux.go/types_linux.go @v0.46.0 comes up
  empty). Not blocking: `NameToHandleAt` sizes its buffer via the EOVERFLOW
  retry loop internally (`unix/syscall_linux.go:2370-2396`), and AT_HANDLE_FID
  (0x200, kernel 6.5+) can be passed as a literal if the probe trick from
  fanotify_mark(2) EOPNOTSUPP is wanted.

Handle plumbing:

- `type FileHandle struct { *fileHandle }` over `fileHandle { Bytes uint32;
  Type int32 }` -- `unix/syscall_linux.go:2335-2346 @v0.46.0`.
- `func NewFileHandle(handleType int32, handle []byte) FileHandle` --
  `unix/syscall_linux.go:2348-2357`. This is the bridge from a fanotify info
  record: pass the record's handle_type and the f_handle bytes.
- `func OpenByHandleAt(mountFD int, handle FileHandle, flags int) (fd int,
  err error)` -- `unix/syscall_linux.go:2399-2401`; then
  `unix.Readlink("/proc/self/fd/"+strconv.Itoa(fd), ...)` resolves the path
  (flow per fanotify(7) example, section 5 above).
- `func NameToHandleAt(dirfd int, path string, flags int) (handle FileHandle,
  mountID int, err error)` -- `unix/syscall_linux.go:2370-2396`; useful to
  compute the watcher's own handles (e.g. to key excluded dirs).

---

## 9. Kernel availability: Ubuntu 22.04 (5.15) vs 24.04 (6.8)

Safe on stock 5.15 (Ubuntu 22.04 GA kernel series):

- FAN_CLASS_NOTIF groups with FAN_REPORT_FID / FAN_REPORT_DIR_FID /
  FAN_REPORT_NAME (=FAN_REPORT_DFID_NAME) [5.1/5.9], FAN_REPORT_PIDFD [5.15]
  (fanotify_init(2), per-flag "since" notes).
- Dirent + self events FAN_CREATE/DELETE/MOVED_FROM/MOVED_TO/DELETE_SELF/
  MOVE_SELF/ATTRIB [all 5.1] with FAN_ONDIR (fanotify_mark(2)).
- FAN_MARK_FILESYSTEM [4.20]; legacy ignore marks
  FAN_MARK_IGNORED_MASK|FAN_MARK_IGNORED_SURV_MODIFY (fanotify_mark(2)).
- Unprivileged API and the /proc/sys/fs/fanotify tunables [5.13]
  (fanotify_init(2) VERSIONS; fanotify(7) "/proc interfaces").
- Event-merge hash table and per-user limits are 5.13-era, present
  (fanotify_user.c @v5.15:30-42).

NOT in mainline v5.15.0 -- avoid unless probed at runtime:

- `FAN_RENAME` and `FAN_REPORT_TARGET_FID` / `FAN_REPORT_DFID_NAME_TARGET`
  [5.17], `FAN_MARK_IGNORE`/`FAN_MARK_IGNORE_SURV` [6.0],
  `FAN_MARK_EVICTABLE` [5.19], `FAN_FS_ERROR` [5.16]. HOWEVER the man pages
  record stable backports of ALL of these at "5.15.154, and 5.10.220"
  (fanotify_init(2) FAN_REPORT_TARGET_FID; fanotify_mark(2) FAN_RENAME,
  FAN_MARK_IGNORE, FAN_MARK_EVICTABLE, FAN_FS_ERROR entries). Ubuntu's jammy
  5.15.0-NNN kernels continuously merge upstream 5.15.y -- the Launchpad
  changelog shows rebases up to v5.15.198 (Jammy linux package changelog,
  launchpad.net) -- so an UPDATED 22.04 kernel has them, while an unpatched
  early-22.04 kernel does not. Do not assume: probe.
- Runtime probing is cheap and reliable: fanotify_init returns EINVAL for
  invalid `flags` (fanotify_init(2), ERRORS) and fanotify_mark returns EINVAL
  for an invalid `mask`/`flags` (fanotify_mark(2), ERRORS) -- try the rich
  combination first, fall back on EINVAL.
- `AT_HANDLE_FID` support-probe for name_to_handle_at is 6.5+ only
  (fanotify_mark(2), EOPNOTSUPP entry) -- on 5.15 probe by attempting the mark
  itself and handling EOPNOTSUPP/ENODEV/EXDEV.

Ubuntu 24.04 (6.8) adds, relative to stock 5.15: everything in the previous
bullet natively (FAN_RENAME, TARGET_FID, MARK_IGNORE, EVICTABLE, FS_ERROR),
AT_HANDLE_FID [6.5], overlayfs handle encoding (section 10), plus the
STRICTER mark-time errors: ENODEV/EXDEV now also fire when adding mount/
filesystem marks on zero-fsid filesystems or btrfs subvolumes and when mixing
marks across filesystems with zero-fsid members (fanotify_mark(2), ERRORS,
"Since Linux 6.8" sentences). 6.8 does NOT have: `FAN_REPORT_FD_ERROR` (6.13,
backported to 6.12.4 and 6.6.66 -- 6.8 is absent from that list,
fanotify_init(2)), `FAN_REPORT_MNT`/`FAN_MARK_MNTNS`/`FAN_MNT_ATTACH`/
`FAN_MNT_DETACH`/`FAN_PRE_ACCESS` (6.14, fanotify_init(2)/fanotify_mark(2)),
`AT_HANDLE_MNT_ID_UNIQUE` (6.12) / `AT_HANDLE_CONNECTABLE` (6.13)
(open_by_handle_at(2)).

---

## 10. Filesystem-specific caveats and ordering guarantees

### Requirements a filesystem must meet for fid groups

- Non-zero fsid, else ENODEV: "associated with a filesystem that reports zero
  fsid (e.g., fuse(4))" (fanotify_mark(2), ERRORS, ENODEV); "monitoring
  different filesystem instances that report zero fsid with the same fanotify
  group is not supported" (fanotify(7), fsid field).
- File-handle encoding support, else EOPNOTSUPP: "associated with a
  filesystem that does not support the encoding of file handles"; testable
  since 6.5 with `name_to_handle_at(..., AT_HANDLE_FID)` (fanotify_mark(2),
  ERRORS, EOPNOTSUPP). /proc and /sys do not support handles
  (open_by_handle_at(2), NOTES) -- irrelevant under exclude rules, and they
  are separate superblocks anyway.
- Since 6.8 these are enforced for mount and filesystem marks at mark time
  (`if (mark_type != FAN_MARK_INODE && !exportfs_can_decode_fh(nop))`,
  fanotify_user.c @v6.8:1685; fanotify_mark(2) ENODEV "Since Linux 6.8").

### overlayfs

- Before kernel 6.6, overlayfs could not encode file handles unless mounted
  with NFS-export support, so fid-mode marks fail with EOPNOTSUPP -- i.e. on
  Ubuntu 22.04 you cannot fanotify-watch an overlayfs (container rootfs) in
  fid mode. Kernel 6.6 commit 16aac5ad1fa9 ("ovl: support encoding
  non-decodable file handles", in torvalds/linux, merged v6.6-rc1) added
  encoding of identify-only handles specifically to enable FAN_REPORT_FID on
  overlayfs; background: LWN, "Support more filesystems with FAN_REPORT_FID"
  (https://lwn.net/Articles/948112/). Ubuntu 24.04's 6.8 has it.
- Caveat that follows: such overlayfs handles are the AT_HANDLE_FID kind --
  good for identifying objects, but "a subsequent call to open_by_handle_at()
  with the returned file_handle may fail" (open_by_handle_at(2),
  AT_HANDLE_FID description; ESTALE entry covers the failure). So on
  overlayfs even a privileged watcher may receive events whose handles cannot
  be resolved back to paths -- design the resolver to tolerate ESTALE and fall
  back (e.g. to a rescan of the containing tree).

### btrfs

- Subvolumes report a different fsid than the root superblock. Result: EXDEV
  when marking "a filesystem subvolume ... which uses a different fsid than
  its root superblock" with a fid group; since 6.8 EXDEV also covers mount/
  filesystem marks on a subvolume, inode marks spread across subvolumes, and
  mixing a subvolume with another filesystem in one group (fanotify_mark(2),
  ERRORS, EXDEV). Mark the top-level subvolume/mount of the btrfs.
- Event routing: the event's fsid is "of the filesystem containing the
  object", defined as equal to statfs f_fsid (fanotify(7), fsid field); on
  btrfs statfs differs per subvolume (the premise of the EXDEV rule above),
  so events from inside other subvolumes of the same superblock can carry a
  DIFFERENT fsid than the marked root. Inference for the watcher: do not
  route events strictly by "fsid == fsid of marked path" on btrfs; treat
  unknown fsids as belonging to the marked sb whose mount_fd successfully
  resolves the handle.

### Ordering guarantees vs inotify

- inotify documents a real ordering guarantee: "The events returned by
  reading from an inotify file descriptor form an ordered queue. Thus, for
  example, it is guaranteed that when renaming from one directory to another,
  events will be produced in the correct order" (inotify(7), NOTES).
- fanotify's man pages make NO equivalent cross-object ordering promise, and
  its merge behavior actively destroys per-object ordering: same-object,
  same-pid events within the 128-entry merge window are collapsed with masks
  OR-ed (section 6; fanotify(7) mask discussion + fanotify.c @v5.15:171-183),
  so "created then deleted" and "deleted then created" can be
  indistinguishable from the event alone (the ISDIR merge guard is the only
  concession, fanotify.c @v5.15:123-131). MOVED_FROM/MOVED_TO adjacency is
  not guaranteed by either API (inotify(7), "Dealing with rename() events" --
  and fanotify has no cookie at all, section 2).
- Both APIs are asynchronous after-the-fact notifications; fanotify
  additionally warns the object may no longer be at the reported location
  when the event is read (fanotify_init(2), FAN_REPORT_NAME). fanotify also
  does not report mmap-based writes (fanotify(7), NOTES: "does not report
  file accesses and modifications that may occur because of mmap(2),
  msync(2), and munmap(2)") nor remote changes on network filesystems
  (ibid.).
- Design consequence: consume every event as "entry (dir-handle, name)
  changed somehow", re-lstat to learn the truth, and let FAN_Q_OVERFLOW
  trigger a reconcile rescan. An event stream interpreted as a state DIFF is
  wrong by construction; interpreted as an invalidation stream it is exactly
  right.

---

## Appendix: minimal privileged watcher recipe (5.15-safe baseline)

1. `FanotifyInit(FAN_CLASS_NOTIF|FAN_CLOEXEC|FAN_NONBLOCK|FAN_REPORT_DFID_NAME,
   O_RDONLY|O_LARGEFILE|O_CLOEXEC)` -- probe-upgrade to
   `...|FAN_REPORT_FID|FAN_REPORT_TARGET_FID` (EINVAL = fall back).
2. Enumerate superblocks from /proc/self/mountinfo (dedupe by fsid/st_dev,
   skip virtual/network fstypes); for each: open O_PATH root fd, then
   `FanotifyMark(fd, FAN_MARK_ADD|FAN_MARK_FILESYSTEM,
   FAN_CREATE|FAN_DELETE|FAN_MOVED_FROM|FAN_MOVED_TO|FAN_MODIFY|
   FAN_CLOSE_WRITE|FAN_ONDIR, -1, root)` -- tolerate ENODEV/EOPNOTSUPP/EXDEV
   per section 10 (skip that sb, log once).
3. Add exclude-dir ignore marks:
   `FAN_MARK_ADD|FAN_MARK_IGNORED_MASK|FAN_MARK_IGNORED_SURV_MODIFY` with the
   same event bits (6.0+/backports: `FAN_MARK_IGNORE_SURV` +
   `FAN_ONDIR|FAN_EVENT_ON_CHILD`).
4. Read >= 4KiB buffers (fanotify(7) recommends large buffers); walk records
   by Event_len; check Vers == FANOTIFY_METADATA_VERSION; skip own-pid
   events; parse info records by hand (x/sys has no structs for them);
   resolve dir handle via NewFileHandle + OpenByHandleAt(mount_fd of that
   fsid) + readlink /proc/self/fd/N; fstatat(dirfd, name) to reconcile;
   ESTALE/ENOENT = deleted.
5. FAN_Q_OVERFLOW in any mask -> schedule reconcile rescan. Requires
   CAP_SYS_ADMIN + CAP_DAC_READ_SEARCH in the initial user namespace --
   without both, fanotify whole-FS watching is not available (sections 3-5).
