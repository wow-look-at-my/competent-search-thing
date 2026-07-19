/* fanospike.c -- empirical spike: can fanotify FAN_MARK_FILESYSTEM +
 * FAN_REPORT_DFID_NAME serve as a whole-filesystem directory-entry watcher?
 *
 * Modes:
 *   ./fanospike main <scratchdir>   -- full test sequence (tests 1-9)
 *                                      scratchdir MUST be on the first markable fs
 *   ./fanospike caps                -- init+mark probe only (for capsh runs, test 10)
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <poll.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/fanotify.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <sys/vfs.h>
#include <time.h>
#include <unistd.h>

/* ---- fallback defines for older userspace headers ---- */
#ifndef FAN_REPORT_DIR_FID
#define FAN_REPORT_DIR_FID 0x00000400
#endif
#ifndef FAN_REPORT_NAME
#define FAN_REPORT_NAME 0x00000800
#endif
#ifndef FAN_REPORT_DFID_NAME
#define FAN_REPORT_DFID_NAME (FAN_REPORT_DIR_FID | FAN_REPORT_NAME)
#endif
#ifndef FAN_REPORT_TARGET_FID
#define FAN_REPORT_TARGET_FID 0x00001000
#endif
#ifndef FAN_REPORT_DFID_NAME_TARGET
#define FAN_REPORT_DFID_NAME_TARGET \
    (FAN_REPORT_DFID_NAME | FAN_REPORT_FID | FAN_REPORT_TARGET_FID)
#endif
#ifndef FAN_RENAME
#define FAN_RENAME 0x10000000
#endif
#ifndef FAN_MARK_IGNORE
#define FAN_MARK_IGNORE 0x00000400
#endif
#ifndef FAN_MARK_IGNORE_SURV
#define FAN_MARK_IGNORE_SURV (FAN_MARK_IGNORE | FAN_MARK_IGNORED_SURV_MODIFY)
#endif
#ifndef FAN_EVENT_INFO_TYPE_DFID_NAME
#define FAN_EVENT_INFO_TYPE_DFID_NAME 2
#endif
#ifndef FAN_EVENT_INFO_TYPE_DFID
#define FAN_EVENT_INFO_TYPE_DFID 3
#endif
#ifndef FAN_EVENT_INFO_TYPE_PIDFD
#define FAN_EVENT_INFO_TYPE_PIDFD 4
#endif
#ifndef FAN_EVENT_INFO_TYPE_ERROR
#define FAN_EVENT_INFO_TYPE_ERROR 5
#endif
#ifndef FAN_EVENT_INFO_TYPE_OLD_DFID_NAME
#define FAN_EVENT_INFO_TYPE_OLD_DFID_NAME 10
#endif
#ifndef FAN_EVENT_INFO_TYPE_NEW_DFID_NAME
#define FAN_EVENT_INFO_TYPE_NEW_DFID_NAME 12
#endif
#ifndef MAX_HANDLE_SZ
#define MAX_HANDLE_SZ 128
#endif

#define BUFSZ (256 * 1024)
#define DIRENT_MASK (FAN_CREATE | FAN_DELETE | FAN_MOVED_FROM | FAN_MOVED_TO | FAN_ONDIR)

static char scratch[PATH_MAX];
static int mount_fd_root = -1;   /* open("/")            */
static int mount_fd_marked = -1; /* open(<marked fs root>) */
static const char *marked_path = NULL;

/* ------------------------------------------------------------------ utils */
static double now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return ts.tv_sec * 1000.0 + ts.tv_nsec / 1e6;
}

static void msleep(int ms) {
    struct timespec ts = {ms / 1000, (ms % 1000) * 1000000L};
    nanosleep(&ts, NULL);
}

static void print_fsid(const char *p) {
    struct statfs sf;
    if (statfs(p, &sf) == 0) {
        unsigned *v = (unsigned *)&sf.f_fsid;
        printf("statfs(%-16s): f_type=0x%08lx f_fsid=%08x:%08x%s\n", p,
               (unsigned long)sf.f_type, v[0], v[1],
               (v[0] == 0 && v[1] == 0) ? "  <-- NULL fsid" : "");
    } else {
        printf("statfs(%s): %s\n", p, strerror(errno));
    }
}

static const char *decode_mask(uint64_t mask) {
    static char out[512];
    out[0] = 0;
    struct { uint64_t bit; const char *name; } bits[] = {
        {FAN_ACCESS, "ACCESS"}, {FAN_MODIFY, "MODIFY"}, {FAN_ATTRIB, "ATTRIB"},
        {FAN_CLOSE_WRITE, "CLOSE_WRITE"}, {FAN_CLOSE_NOWRITE, "CLOSE_NOWRITE"},
        {FAN_OPEN, "OPEN"}, {FAN_MOVED_FROM, "MOVED_FROM"}, {FAN_MOVED_TO, "MOVED_TO"},
        {FAN_CREATE, "CREATE"}, {FAN_DELETE, "DELETE"}, {FAN_DELETE_SELF, "DELETE_SELF"},
        {FAN_MOVE_SELF, "MOVE_SELF"}, {FAN_OPEN_EXEC, "OPEN_EXEC"},
        {FAN_Q_OVERFLOW, "Q_OVERFLOW"}, {FAN_RENAME, "RENAME"},
        {FAN_ONDIR, "ONDIR"}, {FAN_EVENT_ON_CHILD, "EVENT_ON_CHILD"},
    };
    for (size_t i = 0; i < sizeof(bits) / sizeof(bits[0]); i++) {
        if (mask & bits[i].bit) {
            if (out[0]) strcat(out, "|");
            strcat(out, bits[i].name);
        }
    }
    if (!out[0]) strcpy(out, "(none)");
    return out;
}

static const char *info_type_name(int t) {
    switch (t) {
    case FAN_EVENT_INFO_TYPE_FID: return "FID";
    case FAN_EVENT_INFO_TYPE_DFID_NAME: return "DFID_NAME";
    case FAN_EVENT_INFO_TYPE_DFID: return "DFID";
    case FAN_EVENT_INFO_TYPE_PIDFD: return "PIDFD";
    case FAN_EVENT_INFO_TYPE_ERROR: return "ERROR";
    case FAN_EVENT_INFO_TYPE_OLD_DFID_NAME: return "OLD_DFID_NAME";
    case FAN_EVENT_INFO_TYPE_NEW_DFID_NAME: return "NEW_DFID_NAME";
    default: return "?";
    }
}

/* ------------------------------------------------------- collected events */
#define MAXINFO 4
#define MAXNAME 128
#define MAXPATH 384
#define HANDLEHEX 66

typedef struct {
    uint8_t type;
    uint32_t fsid0, fsid1;
    uint32_t handle_bytes;
    int32_t handle_type;
    char handle_hex[HANDLEHEX];
    char name[MAXNAME];
    char resolved_root[MAXPATH];
    int resolve_root_errno; /* 0 = ok, -1 = not attempted */
    char resolved_marked[MAXPATH];
    int resolve_marked_errno;
} inforec_t;

typedef struct {
    uint64_t mask;
    int32_t pid;
    int32_t fd;
    int n_info;
    inforec_t info[MAXINFO];
} ev_t;

static int resolve_handle(int mount_fd, uint32_t hb, int32_t ht, const unsigned char *raw,
                          char *out, size_t outsz) {
    struct { struct file_handle fh; unsigned char pad[MAX_HANDLE_SZ]; } h;
    memset(&h, 0, sizeof(h));
    if (hb > MAX_HANDLE_SZ) return EOVERFLOW;
    h.fh.handle_bytes = hb;
    h.fh.handle_type = ht;
    memcpy(h.fh.f_handle, raw, hb);
    int fd = open_by_handle_at(mount_fd, &h.fh, O_RDONLY | O_PATH);
    if (fd < 0) return errno;
    char proc[64];
    snprintf(proc, sizeof(proc), "/proc/self/fd/%d", fd);
    ssize_t n = readlink(proc, out, outsz - 1);
    int e = (n < 0) ? errno : 0;
    if (n >= 0) out[n] = 0; else out[0] = 0;
    close(fd);
    return e;
}

typedef struct {
    int reads;
    ssize_t read_sizes[64];
    double drain_ms;
    int overflow_seen;
    int total_events;
} drainstat_t;

/* Drain the fanotify queue: poll with quiet_ms timeout, read 256KB buffer,
 * parse, optionally resolve parent handles, print up to print_limit events. */
static int drain(int fanfd, const char *label, int quiet_ms, ev_t *evs, int maxev,
                 int do_resolve, int print_limit, drainstat_t *st) {
    static char buf[BUFSZ] __attribute__((aligned(8)));
    int nev = 0;
    memset(st, 0, sizeof(*st));
    double t0 = now_ms();
    printf("-- drain[%s] --\n", label);
    for (;;) {
        struct pollfd p = {.fd = fanfd, .events = POLLIN};
        int pr = poll(&p, 1, quiet_ms);
        if (pr == 0) break; /* quiet -> queue drained */
        if (pr < 0) { printf("  poll error: %s\n", strerror(errno)); break; }
        ssize_t n = read(fanfd, buf, sizeof(buf));
        if (n < 0) {
            if (errno == EAGAIN) continue;
            printf("  read error: %s\n", strerror(errno));
            break;
        }
        if (st->reads < 64) st->read_sizes[st->reads] = n;
        st->reads++;
        struct fanotify_event_metadata *meta = (void *)buf;
        while (FAN_EVENT_OK(meta, n)) {
            st->total_events++;
            if (meta->mask & FAN_Q_OVERFLOW) st->overflow_seen = 1;
            ev_t *e = NULL;
            if (nev < maxev) {
                e = &evs[nev++];
                memset(e, 0, sizeof(*e));
                e->mask = meta->mask;
                e->pid = meta->pid;
                e->fd = meta->fd;
            }
            int printing = (st->total_events <= print_limit);
            if (printing)
                printf("  EVENT mask=0x%llx [%s] pid=%d fd=%d event_len=%u metadata_len=%u vers=%u\n",
                       (unsigned long long)meta->mask, decode_mask(meta->mask),
                       (int)meta->pid, meta->fd, meta->event_len, meta->metadata_len,
                       (unsigned)meta->vers);
            size_t off = meta->metadata_len;
            while (off + sizeof(struct fanotify_event_info_header) <= meta->event_len) {
                struct fanotify_event_info_header *hdr = (void *)((char *)meta + off);
                if (hdr->len == 0) break;
                if (hdr->info_type == FAN_EVENT_INFO_TYPE_FID ||
                    hdr->info_type == FAN_EVENT_INFO_TYPE_DFID ||
                    hdr->info_type == FAN_EVENT_INFO_TYPE_DFID_NAME ||
                    hdr->info_type == FAN_EVENT_INFO_TYPE_OLD_DFID_NAME ||
                    hdr->info_type == FAN_EVENT_INFO_TYPE_NEW_DFID_NAME) {
                    struct fanotify_event_info_fid *fid = (void *)hdr;
                    struct file_handle *fh = (void *)fid->handle;
                    const char *name = "";
                    if (hdr->info_type == FAN_EVENT_INFO_TYPE_DFID_NAME ||
                        hdr->info_type == FAN_EVENT_INFO_TYPE_OLD_DFID_NAME ||
                        hdr->info_type == FAN_EVENT_INFO_TYPE_NEW_DFID_NAME)
                        name = (char *)fh->f_handle + fh->handle_bytes;
                    inforec_t *ir = NULL;
                    if (e && e->n_info < MAXINFO) {
                        ir = &e->info[e->n_info++];
                        ir->type = hdr->info_type;
                        ir->fsid0 = (uint32_t)fid->fsid.val[0];
                        ir->fsid1 = (uint32_t)fid->fsid.val[1];
                        ir->handle_bytes = fh->handle_bytes;
                        ir->handle_type = fh->handle_type;
                        for (uint32_t i = 0; i < fh->handle_bytes && i < (HANDLEHEX - 2) / 2; i++)
                            sprintf(ir->handle_hex + 2 * i, "%02x", fh->f_handle[i]);
                        snprintf(ir->name, MAXNAME, "%s", name);
                        ir->resolve_root_errno = -1;
                        ir->resolve_marked_errno = -1;
                    }
                    if (printing)
                        printf("    INFO type=%s(%u) len=%u fsid=%08x:%08x handle_type=%d handle_bytes=%u handle=%s name=\"%s\"\n",
                               info_type_name(hdr->info_type), hdr->info_type, hdr->len,
                               (unsigned)fid->fsid.val[0], (unsigned)fid->fsid.val[1],
                               fh->handle_type, fh->handle_bytes, ir ? ir->handle_hex : "?", name);
                    if (do_resolve && ir) {
                        ir->resolve_root_errno = resolve_handle(
                            mount_fd_root, fh->handle_bytes, fh->handle_type,
                            fh->f_handle, ir->resolved_root, MAXPATH);
                        ir->resolve_marked_errno = resolve_handle(
                            mount_fd_marked, fh->handle_bytes, fh->handle_type,
                            fh->f_handle, ir->resolved_marked, MAXPATH);
                        if (printing) {
                            if (ir->resolve_root_errno == 0)
                                printf("      resolve(mount_fd=\"/\")        -> %s\n", ir->resolved_root);
                            else
                                printf("      resolve(mount_fd=\"/\")        -> FAILED errno=%d (%s)\n",
                                       ir->resolve_root_errno, strerror(ir->resolve_root_errno));
                            if (ir->resolve_marked_errno == 0)
                                printf("      resolve(mount_fd=marked-fs)  -> %s\n", ir->resolved_marked);
                            else
                                printf("      resolve(mount_fd=marked-fs)  -> FAILED errno=%d (%s)\n",
                                       ir->resolve_marked_errno, strerror(ir->resolve_marked_errno));
                        }
                    }
                } else if (printing) {
                    printf("    INFO type=%s(%u) len=%u\n", info_type_name(hdr->info_type),
                           hdr->info_type, hdr->len);
                }
                off += hdr->len;
            }
            if (meta->fd >= 0) close(meta->fd);
            meta = FAN_EVENT_NEXT(meta, n);
        }
    }
    st->drain_ms = now_ms() - t0;
    if (st->total_events > print_limit)
        printf("  (... %d more events not printed ...)\n", st->total_events - print_limit);
    printf("  drain[%s]: %d events in %d read(s), %.1f ms\n", label, st->total_events,
           st->reads, st->drain_ms);
    return nev;
}

static ev_t *find_ev(ev_t *evs, int n, uint64_t need_mask, const char *name) {
    for (int i = 0; i < n; i++) {
        if ((evs[i].mask & need_mask) != need_mask) continue;
        if (!name) return &evs[i];
        for (int j = 0; j < evs[i].n_info; j++)
            if (strcmp(evs[i].info[j].name, name) == 0) return &evs[i];
    }
    return NULL;
}

static int ends_with(const char *s, const char *suffix) {
    size_t ls = strlen(s), lx = strlen(suffix);
    return ls >= lx && strcmp(s + ls - lx, suffix) == 0;
}

static void check(int ok, const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    printf(ok ? "CHECK PASS: " : "CHECK FAIL: ");
    vprintf(fmt, ap);
    printf("\n");
    va_end(ap);
}

static void creat_file(const char *path) {
    int fd = open(path, O_CREAT | O_WRONLY, 0644);
    if (fd < 0) { printf("  !! creat %s: %s\n", path, strerror(errno)); return; }
    close(fd);
}

static char *jp(const char *a, const char *b) { /* join path (static rotate) */
    static char bufs[8][PATH_MAX];
    static int i = 0;
    char *out = bufs[i++ & 7];
    snprintf(out, PATH_MAX - 1, "%s/%s", a, b);
    return out;
}

/* ------------------------------------------------------------- caps mode */
static int caps_mode(void) {
    printf("caps-mode probe (uid=%d euid=%d)\n", getuid(), geteuid());
    int fd = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_REPORT_DFID_NAME, O_RDONLY);
    if (fd < 0) {
        printf("fanotify_init(FAN_CLASS_NOTIF|FAN_CLOEXEC|FAN_REPORT_DFID_NAME): FAILED errno=%d (%s)\n",
               errno, strerror(errno));
        return 0;
    }
    printf("fanotify_init: OK fd=%d\n", fd);
    int r = fanotify_mark(fd, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, DIRENT_MASK, AT_FDCWD, "/dev/shm");
    if (r < 0)
        printf("fanotify_mark(FAN_MARK_FILESYSTEM, \"/dev/shm\"): FAILED errno=%d (%s)\n",
               errno, strerror(errno));
    else
        printf("fanotify_mark(FAN_MARK_FILESYSTEM, \"/dev/shm\"): OK\n");
    r = fanotify_mark(fd, FAN_MARK_ADD | FAN_MARK_MOUNT, FAN_OPEN, AT_FDCWD, "/dev/shm");
    if (r < 0)
        printf("fanotify_mark(FAN_MARK_MOUNT, FAN_OPEN, \"/dev/shm\"): FAILED errno=%d (%s)\n",
               errno, strerror(errno));
    else
        printf("fanotify_mark(FAN_MARK_MOUNT, FAN_OPEN, \"/dev/shm\"): OK\n");
    r = fanotify_mark(fd, FAN_MARK_ADD, DIRENT_MASK, AT_FDCWD, "/dev/shm");
    if (r < 0)
        printf("fanotify_mark(inode mark, dirent mask, \"/dev/shm\"): FAILED errno=%d (%s)\n",
               errno, strerror(errno));
    else
        printf("fanotify_mark(inode mark, dirent mask, \"/dev/shm\"): OK\n");
    close(fd);
    return 0;
}

/* --------------------------------------------------------------- main run */
static ev_t evbuf[4096];

int main(int argc, char **argv) {
    setvbuf(stdout, NULL, _IONBF, 0);
    if (argc >= 2 && strcmp(argv[1], "caps") == 0) return caps_mode();
    if (argc < 3 || strcmp(argv[1], "main") != 0) {
        fprintf(stderr, "usage: %s main <scratchdir> | %s caps\n", argv[0], argv[0]);
        return 2;
    }
    snprintf(scratch, sizeof(scratch), "%s", argv[2]);
    mkdir(scratch, 0755);
    mount_fd_root = open("/", O_RDONLY | O_DIRECTORY);
    printf("scratch=%s\n", scratch);
    print_fsid("/");
    print_fsid("/home/user");
    print_fsid("/dev/shm");
    print_fsid("/opt/claude-code");
    printf("\n");
    drainstat_t st;
    int n, r;

    /* ============================ TEST 1: fanotify_init variants */
    printf("=== TEST 1: fanotify_init ===\n");
    int fan = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_REPORT_DFID_NAME, O_RDONLY);
    if (fan >= 0)
        printf("T1a fanotify_init(FAN_CLASS_NOTIF|FAN_CLOEXEC|FAN_REPORT_DFID_NAME, O_RDONLY): PASS fd=%d\n", fan);
    else
        printf("T1a fanotify_init(FAN_CLASS_NOTIF|FAN_CLOEXEC|FAN_REPORT_DFID_NAME, O_RDONLY): FAIL errno=%d (%s)\n",
               errno, strerror(errno));
    int fidonly = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_REPORT_FID, O_RDONLY);
    printf("T1b fanotify_init(... FAN_REPORT_FID alone): %s%s\n",
           fidonly >= 0 ? "PASS" : "FAIL ", fidonly >= 0 ? "" : strerror(errno));
    if (fidonly >= 0) close(fidonly);
    int tgt = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_REPORT_DFID_NAME_TARGET, O_RDONLY);
    printf("T1c fanotify_init(... FAN_REPORT_DFID_NAME_TARGET): %s%s (bonus probe)\n",
           tgt >= 0 ? "PASS" : "FAIL ", tgt >= 0 ? "" : strerror(errno));
    if (tgt >= 0) close(tgt);
    if (fan < 0) { printf("cannot continue without T1a fd\n"); return 1; }
    close(fan);
    fan = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_NONBLOCK | FAN_REPORT_DFID_NAME, O_RDONLY);
    printf("T1d working group re-init with |FAN_NONBLOCK: %s fd=%d\n\n",
           fan >= 0 ? "PASS" : "FAIL", fan);
    if (fan < 0) return 1;

    /* ============================ TEST 2: FAN_MARK_FILESYSTEM (timed) */
    printf("=== TEST 2: fanotify_mark FAN_MARK_ADD|FAN_MARK_FILESYSTEM ===\n");
    const char *mark_targets[] = {"/", "/home/user", "/dev/shm"};
    for (int i = 0; i < 3 && !marked_path; i++) {
        double t0 = now_ms();
        r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, DIRENT_MASK,
                          AT_FDCWD, mark_targets[i]);
        double t1 = now_ms();
        if (r == 0) {
            printf("T2 mark(%s, CREATE|DELETE|MOVED_FROM|MOVED_TO|ONDIR): PASS in %.3f ms (%.0f us)\n",
                   mark_targets[i], t1 - t0, (t1 - t0) * 1000);
            marked_path = mark_targets[i];
        } else {
            printf("T2 mark(%s): FAIL errno=%d (%s) [%.3f ms]\n", mark_targets[i],
                   errno, strerror(errno), t1 - t0);
        }
    }
    if (!marked_path) { printf("no filesystem could be marked; aborting\n"); return 1; }
    mount_fd_marked = open(marked_path, O_RDONLY | O_DIRECTORY);
    printf("marked filesystem: superblock of %s (mount_fd_marked=%d)\n", marked_path,
           mount_fd_marked);

    /* T2x: null-fsid diagnostics with fresh groups */
    printf("\n--- TEST 2x: why did \"/\" fail? null-fsid diagnostics ---\n");
    int g3 = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_NONBLOCK | FAN_REPORT_DFID_NAME, O_RDONLY);
    if (g3 >= 0) {
        r = fanotify_mark(g3, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, DIRENT_MASK, AT_FDCWD, "/");
        printf("T2x-a fresh group, sb-mark \"/\" (ext4, fsid=0:0): %s%s\n",
               r == 0 ? "PASS (unexpected)" : "FAIL ", r == 0 ? "" : strerror(errno));
        r = fanotify_mark(g3, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, DIRENT_MASK, AT_FDCWD,
                          "/opt/claude-code");
        printf("T2x-b fresh group, sb-mark \"/opt/claude-code\" (ext4 WITH uuid, fsid!=0): %s%s\n",
               r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
        close(g3);
    }
    g3 = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_NONBLOCK | FAN_REPORT_DFID_NAME, O_RDONLY);
    if (g3 >= 0) {
        mkdir("/tmp/fanospike-inodeprobe", 0755);
        r = fanotify_mark(g3, FAN_MARK_ADD, DIRENT_MASK, AT_FDCWD, "/tmp/fanospike-inodeprobe");
        printf("T2x-c fresh group, plain INODE mark on ext4 dir (weak-fsid path): %s%s\n",
               r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
        if (r == 0) {
            creat_file("/tmp/fanospike-inodeprobe/f1");
            msleep(50);
            n = drain(g3, "inode mark on null-fsid ext4", 250, evbuf, 4096, 1, 10, &st);
            ev_t *e0 = find_ev(evbuf, n, FAN_CREATE, "f1");
            check(e0 != NULL, "inode mark on null-fsid ext4 DOES deliver dirent events");
            if (e0)
                printf("NOTE: event fsid on null-fsid fs = %08x:%08x\n", e0->info[0].fsid0,
                       e0->info[0].fsid1);
            unlink("/tmp/fanospike-inodeprobe/f1");
            r = fanotify_mark(g3, FAN_MARK_ADD, DIRENT_MASK, AT_FDCWD, "/dev/shm");
            printf("T2x-d same group, add mark on OTHER fs (tmpfs) after weak-fsid mark: %s%s\n",
                   r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
        }
        rmdir("/tmp/fanospike-inodeprobe");
        close(g3);
    }
    printf("\n");

    /* ===================== TEST 3+4: dirent ops with incremental drains */
    printf("=== TEST 3+4: ops on marked fs (%s), event decode + handle resolution ===\n",
           marked_path);
    msleep(50);
    drain(fan, "pre-existing noise", 250, evbuf, 4096, 0, 10, &st);

    /* a) create a file */
    creat_file(jp(scratch, "file-a"));
    msleep(50);
    n = drain(fan, "creat file-a", 250, evbuf, 4096, 1, 40, &st);
    ev_t *e = find_ev(evbuf, n, FAN_CREATE, "file-a");
    check(e != NULL, "creat file -> FAN_CREATE with name \"file-a\" arrived");
    if (e) {
        check(!(e->mask & FAN_ONDIR), "file create has no FAN_ONDIR bit");
        check(e->info[0].resolve_marked_errno == 0 &&
              ends_with(e->info[0].resolved_marked, "/fanospike-scratch"),
              "parent handle resolved via mount_fd=marked-fs to the scratch dir (got \"%s\")",
              e->info[0].resolved_marked);
        printf("NOTE: resolve via WRONG-fs mount_fd (\"/\") gave errno=%d (%s)\n",
               e->info[0].resolve_root_errno,
               e->info[0].resolve_root_errno > 0 ? strerror(e->info[0].resolve_root_errno) : "ok");
    }

    /* b) mkdir */
    mkdir(jp(scratch, "subdir"), 0755);
    msleep(50);
    n = drain(fan, "mkdir subdir", 250, evbuf, 4096, 1, 40, &st);
    e = find_ev(evbuf, n, FAN_CREATE | FAN_ONDIR, "subdir");
    check(e != NULL, "mkdir -> FAN_CREATE|FAN_ONDIR with name \"subdir\" arrived");

    /* c) create a file inside the subdir -- immediate-parent check */
    creat_file(jp(scratch, "subdir/inner.txt"));
    msleep(50);
    n = drain(fan, "creat subdir/inner.txt", 250, evbuf, 4096, 1, 40, &st);
    e = find_ev(evbuf, n, FAN_CREATE, "inner.txt");
    check(e != NULL, "create in subdir -> FAN_CREATE name \"inner.txt\" arrived");
    if (e)
        check(e->info[0].resolve_marked_errno == 0 &&
              ends_with(e->info[0].resolved_marked, "/subdir"),
              "Q5: parent handle is the IMMEDIATE parent (\"%s\" ends with /subdir), not the marked root",
              e->info[0].resolved_marked);

    /* d) same-dir file rename */
    rename(jp(scratch, "file-a"), jp(scratch, "file-b"));
    msleep(50);
    n = drain(fan, "rename file-a -> file-b", 250, evbuf, 4096, 1, 40, &st);
    ev_t *efrom = find_ev(evbuf, n, FAN_MOVED_FROM, "file-a");
    ev_t *eto = find_ev(evbuf, n, FAN_MOVED_TO, "file-b");
    check(efrom != NULL, "Q5: FAN_MOVED_FROM carries OLD name \"file-a\"");
    check(eto != NULL, "Q5: FAN_MOVED_TO carries NEW name \"file-b\"");
    if (efrom && eto) {
        check(strcmp(efrom->info[0].handle_hex, eto->info[0].handle_hex) == 0,
              "same-dir rename: FROM and TO parent handles identical (%s)", efrom->info[0].handle_hex);
        printf("NOTE Q5 pairing: metadata fields are only {event_len,vers,metadata_len,mask,fd,pid};"
               " no cookie/id linking MOVED_FROM to MOVED_TO (unlike inotify)."
               " Only queue adjacency + FAN_RENAME (test 5b) pair them.\n");
    }

    /* e) directory rename */
    rename(jp(scratch, "subdir"), jp(scratch, "subdir2"));
    msleep(50);
    n = drain(fan, "rename subdir -> subdir2 (DIR)", 250, evbuf, 4096, 1, 40, &st);
    efrom = find_ev(evbuf, n, FAN_MOVED_FROM | FAN_ONDIR, "subdir");
    eto = find_ev(evbuf, n, FAN_MOVED_TO | FAN_ONDIR, "subdir2");
    check(efrom != NULL, "dir rename -> FAN_MOVED_FROM|FAN_ONDIR name \"subdir\"");
    check(eto != NULL, "dir rename -> FAN_MOVED_TO|FAN_ONDIR name \"subdir2\"");

    /* f) unlink file in renamed subdir -- post-rename handle resolution */
    unlink(jp(scratch, "subdir2/inner.txt"));
    msleep(50);
    n = drain(fan, "unlink subdir2/inner.txt", 250, evbuf, 4096, 1, 40, &st);
    e = find_ev(evbuf, n, FAN_DELETE, "inner.txt");
    check(e != NULL, "unlink -> FAN_DELETE name \"inner.txt\"");
    if (e)
        check(e->info[0].resolve_marked_errno == 0 &&
              ends_with(e->info[0].resolved_marked, "/subdir2"),
              "parent handle resolves to CURRENT path after dir rename (\"%s\")",
              e->info[0].resolved_marked);

    /* g) unlink file-b, rmdir subdir2 */
    unlink(jp(scratch, "file-b"));
    msleep(30);
    rmdir(jp(scratch, "subdir2"));
    msleep(50);
    n = drain(fan, "unlink file-b + rmdir subdir2", 250, evbuf, 4096, 1, 40, &st);
    check(find_ev(evbuf, n, FAN_DELETE, "file-b") != NULL, "unlink -> FAN_DELETE \"file-b\"");
    check(find_ev(evbuf, n, FAN_DELETE | FAN_ONDIR, "subdir2") != NULL,
          "Q5: rmdir -> FAN_DELETE|FAN_ONDIR \"subdir2\"");

    /* 4b) stale-handle probe: delete parent BEFORE draining its events */
    printf("\n--- TEST 4b: stale parent handle (dir deleted before drain) ---\n");
    mkdir(jp(scratch, "tmpd"), 0755);
    msleep(30);
    drain(fan, "setup tmpd", 250, evbuf, 4096, 0, 5, &st);
    creat_file(jp(scratch, "tmpd/f"));
    unlink(jp(scratch, "tmpd/f"));
    rmdir(jp(scratch, "tmpd"));
    msleep(50);
    n = drain(fan, "ops with parent gone", 250, evbuf, 4096, 1, 40, &st);
    e = find_ev(evbuf, n, FAN_CREATE, "f");
    check(e != NULL, "create event for tmpd/f still delivered after tmpd was rmdir'd");
    if (e)
        check(e->info[0].resolve_marked_errno == ESTALE,
              "resolving the DELETED parent's handle fails with ESTALE (got errno=%d %s)",
              e->info[0].resolve_marked_errno,
              e->info[0].resolve_marked_errno > 0 ? strerror(e->info[0].resolve_marked_errno) : "ok");

    /* ============== TEST 5: cross-dir rename, old/new parent handles */
    printf("\n=== TEST 5: cross-dir rename, old/new parent handles ===\n");
    mkdir(jp(scratch, "dstdir"), 0755);
    creat_file(jp(scratch, "xfile"));
    msleep(50);
    drain(fan, "setup dstdir+xfile", 250, evbuf, 4096, 0, 10, &st);
    rename(jp(scratch, "xfile"), jp(scratch, "dstdir/xfile2"));
    msleep(50);
    n = drain(fan, "rename xfile -> dstdir/xfile2", 250, evbuf, 4096, 1, 40, &st);
    efrom = find_ev(evbuf, n, FAN_MOVED_FROM, "xfile");
    eto = find_ev(evbuf, n, FAN_MOVED_TO, "xfile2");
    check(efrom != NULL, "Q5: cross-dir MOVED_FROM has OLD parent + OLD name (\"xfile\")");
    check(eto != NULL, "Q5: cross-dir MOVED_TO has NEW parent + NEW name (\"xfile2\")");
    if (efrom && eto) {
        check(strcmp(efrom->info[0].handle_hex, eto->info[0].handle_hex) != 0,
              "cross-dir rename: parent handles DIFFER (from=%s to=%s)",
              efrom->info[0].handle_hex, eto->info[0].handle_hex);
        check(efrom->info[0].resolve_marked_errno == 0 &&
              ends_with(efrom->info[0].resolved_marked, "/fanospike-scratch") &&
              eto->info[0].resolve_marked_errno == 0 &&
              ends_with(eto->info[0].resolved_marked, "/dstdir"),
              "resolved: FROM parent=\"%s\" TO parent=\"%s\"",
              efrom->info[0].resolved_marked, eto->info[0].resolved_marked);
    }

    /* 5b) FAN_RENAME: single event with OLD_DFID_NAME + NEW_DFID_NAME */
    printf("\n=== TEST 5b: FAN_RENAME (single-event pairing) ===\n");
    r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, FAN_RENAME | FAN_ONDIR,
                      AT_FDCWD, marked_path);
    printf("adding FAN_RENAME|FAN_ONDIR to the filesystem mark: %s%s\n",
           r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
    if (r == 0) {
        rename(jp(scratch, "dstdir/xfile2"), jp(scratch, "xfile3"));
        msleep(50);
        n = drain(fan, "rename dstdir/xfile2 -> xfile3 (RENAME armed)", 250, evbuf, 4096, 1, 40, &st);
        ev_t *er = find_ev(evbuf, n, FAN_RENAME, NULL);
        check(er != NULL, "FAN_RENAME event arrived (alongside MOVED_FROM/MOVED_TO)");
        if (er) {
            int has_old = 0, has_new = 0;
            for (int j = 0; j < er->n_info; j++) {
                if (er->info[j].type == FAN_EVENT_INFO_TYPE_OLD_DFID_NAME &&
                    strcmp(er->info[j].name, "xfile2") == 0) has_old = 1;
                if (er->info[j].type == FAN_EVENT_INFO_TYPE_NEW_DFID_NAME &&
                    strcmp(er->info[j].name, "xfile3") == 0) has_new = 1;
            }
            check(has_old && has_new,
                  "Q5 pairing answer: ONE FAN_RENAME event carries OLD_DFID_NAME(\"xfile2\") + NEW_DFID_NAME(\"xfile3\")");
        }
    }
    unlink(jp(scratch, "xfile3"));
    rmdir(jp(scratch, "dstdir"));
    msleep(50);
    drain(fan, "cleanup", 250, evbuf, 4096, 0, 5, &st);

    /* 5c) does mkdir/rmdir need FAN_ONDIR? separate group without it */
    printf("\n=== TEST 5c: is FAN_ONDIR required for mkdir/rmdir visibility? ===\n");
    int g2 = fanotify_init(FAN_CLASS_NOTIF | FAN_CLOEXEC | FAN_NONBLOCK | FAN_REPORT_DFID_NAME, O_RDONLY);
    if (g2 >= 0 && fanotify_mark(g2, FAN_MARK_ADD | FAN_MARK_FILESYSTEM,
                                 FAN_CREATE | FAN_DELETE, AT_FDCWD, marked_path) == 0) {
        printf("second group marked with CREATE|DELETE only (NO FAN_ONDIR)\n");
        mkdir(jp(scratch, "ondir-probe"), 0755);
        creat_file(jp(scratch, "ondir-probe-file"));
        msleep(50);
        rmdir(jp(scratch, "ondir-probe"));
        unlink(jp(scratch, "ondir-probe-file"));
        msleep(50);
        n = drain(g2, "no-ONDIR group", 250, evbuf, 4096, 0, 20, &st);
        int dir_seen = find_ev(evbuf, n, FAN_CREATE, "ondir-probe") != NULL ||
                       find_ev(evbuf, n, FAN_DELETE, "ondir-probe") != NULL;
        int file_seen = find_ev(evbuf, n, FAN_CREATE, "ondir-probe-file") != NULL;
        check(file_seen, "no-ONDIR group still sees FILE create");
        check(!dir_seen, "Q5: no-ONDIR group sees NO mkdir/rmdir events -> FAN_ONDIR IS required for dir events");
        drain(fan, "main group control (sees all 4)", 250, evbuf, 4096, 0, 8, &st);
        close(g2);
    } else {
        printf("could not set up second group: %s\n", strerror(errno));
        if (g2 >= 0) close(g2);
    }

    /* ============================ TEST 6: ignore marks */
    printf("\n=== TEST 6: ignore marks (FAN_MARK_IGNORED_MASK legacy form) ===\n");
    mkdir(jp(scratch, "noise"), 0755);
    mkdir(jp(scratch, "sibling"), 0755);
    msleep(50);
    drain(fan, "setup noise+sibling", 250, evbuf, 4096, 0, 5, &st);
    r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_IGNORED_MASK | FAN_MARK_IGNORED_SURV_MODIFY,
                      DIRENT_MASK, AT_FDCWD, jp(scratch, "noise"));
    printf("T6a ignore mark on noise/ (mask=CREATE|DELETE|MOVED_FROM|MOVED_TO|ONDIR, IGNORED_MASK|IGNORED_SURV_MODIFY): %s%s\n",
           r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
    creat_file(jp(scratch, "noise/nfile"));
    unlink(jp(scratch, "noise/nfile"));
    creat_file(jp(scratch, "sibling/sfile"));
    unlink(jp(scratch, "sibling/sfile"));
    mkdir(jp(scratch, "noise/deeper"), 0755);
    creat_file(jp(scratch, "noise/deeper/dfile"));
    unlink(jp(scratch, "noise/deeper/dfile"));
    msleep(80);
    n = drain(fan, "ignore-mark ops", 300, evbuf, 4096, 0, 30, &st);
    int nfile_seen = find_ev(evbuf, n, FAN_CREATE, "nfile") || find_ev(evbuf, n, FAN_DELETE, "nfile");
    int sfile_seen = find_ev(evbuf, n, FAN_CREATE, "sfile") && find_ev(evbuf, n, FAN_DELETE, "sfile");
    int deeper_mk_seen = find_ev(evbuf, n, FAN_CREATE | FAN_ONDIR, "deeper") != NULL;
    int dfile_seen = find_ev(evbuf, n, FAN_CREATE, "dfile") || find_ev(evbuf, n, FAN_DELETE, "dfile");
    check(!nfile_seen, "entries DIRECTLY inside noise/ are suppressed (no nfile events)");
    check(sfile_seen, "sibling/ events still arrive (sfile create+delete seen)");
    check(!deeper_mk_seen, "mkdir noise/deeper (an entry IN noise/) is suppressed");
    check(dfile_seen, "CRITICAL: events in NESTED dir noise/deeper/ are NOT suppressed (dfile seen) -> ignore mark is NOT recursive");
    if (nfile_seen)
        printf("NOTE: direct-child suppression FAILED with this mask form; retry variant needed\n");

    /* 6b: newer FAN_MARK_IGNORE_SURV form on a second dir */
    printf("\n--- TEST 6b: newer FAN_MARK_IGNORE_SURV form ---\n");
    mkdir(jp(scratch, "noise2"), 0755);
    msleep(50);
    drain(fan, "setup noise2", 250, evbuf, 4096, 0, 5, &st);
    r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_IGNORE_SURV, DIRENT_MASK, AT_FDCWD,
                      jp(scratch, "noise2"));
    printf("T6b ignore mark (FAN_MARK_IGNORE_SURV, mask=dirent|ONDIR): %s%s\n",
           r == 0 ? "PASS" : "FAIL ", r == 0 ? "" : strerror(errno));
    if (r != 0 && errno == EINVAL) {
        r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_IGNORE_SURV,
                          DIRENT_MASK | FAN_EVENT_ON_CHILD, AT_FDCWD, jp(scratch, "noise2"));
        printf("T6b retry with |FAN_EVENT_ON_CHILD: %s%s\n", r == 0 ? "PASS" : "FAIL ",
               r == 0 ? "" : strerror(errno));
    }
    if (r == 0) {
        creat_file(jp(scratch, "noise2/n2file"));
        unlink(jp(scratch, "noise2/n2file"));
        creat_file(jp(scratch, "sibling/s2file"));
        msleep(80);
        n = drain(fan, "IGNORE_SURV ops", 300, evbuf, 4096, 0, 20, &st);
        int n2_seen = find_ev(evbuf, n, FAN_CREATE, "n2file") || find_ev(evbuf, n, FAN_DELETE, "n2file");
        int s2_seen = find_ev(evbuf, n, FAN_CREATE, "s2file") != NULL;
        check(!n2_seen, "FAN_MARK_IGNORE_SURV suppresses direct children of noise2/");
        check(s2_seen, "sibling still reported under IGNORE_SURV form");
        unlink(jp(scratch, "sibling/s2file"));
    }

    /* ============================ TEST 7: coalescing, 1000 creates */
    printf("\n=== TEST 7: 1000 creates in one dir -- coalescing probe ===\n");
    mkdir(jp(scratch, "bulk"), 0755);
    msleep(50);
    drain(fan, "pre-bulk", 250, evbuf, 4096, 0, 5, &st);
    char bp[PATH_MAX];
    double t0 = now_ms();
    for (int i = 0; i < 1000; i++) {
        snprintf(bp, sizeof(bp) - 1, "%s/bulk/n%04d", scratch, i);
        int fd = open(bp, O_CREAT | O_WRONLY, 0644);
        if (fd >= 0) close(fd);
    }
    double t_create = now_ms() - t0;
    printf("created 1000 files in %.1f ms\n", t_create);
    msleep(100);
    n = drain(fan, "bulk-drain", 400, evbuf, 4096, 0, 3, &st);
    int n_create = 0;
    for (int i = 0; i < n; i++)
        if ((evbuf[i].mask & FAN_CREATE) && evbuf[i].n_info > 0 &&
            strncmp(evbuf[i].info[0].name, "n0", 2) == 0)
            n_create++;
    printf("FAN_CREATE events for the 1000 files: %d (collected %d total events)\n", n_create, n);
    printf("read() sizes:");
    for (int i = 0; i < st.reads && i < 64; i++) printf(" %zd", st.read_sizes[i]);
    printf("  (reads=%d, drain=%.1f ms incl. 400ms quiet tail, overflow=%s)\n", st.reads,
           st.drain_ms, st.overflow_seen ? "YES" : "no");
    check(n_create == 1000, "one distinct event per created name (%d/1000) -- no name-level coalescing", n_create);
    check(!st.overflow_seen, "no FAN_Q_OVERFLOW at 1000 queued events (max_queued_events=16384)");

    /* ==================== TEST 8: different filesystem not covered */
    const char *otherfs_dir = strcmp(marked_path, "/dev/shm") == 0 ? "/tmp" : "/dev/shm";
    printf("\n=== TEST 8: mark scope = one superblock (create on %s must be silent) ===\n",
           otherfs_dir);
    creat_file(jp(otherfs_dir, "fanospike-otherfs-probe"));
    msleep(80);
    n = drain(fan, "after other-fs create", 350, evbuf, 4096, 0, 10, &st);
    check(find_ev(evbuf, n, FAN_CREATE, "fanospike-otherfs-probe") == NULL,
          "create on %s (different superblock) produced NO event", otherfs_dir);
    unlink(jp(otherfs_dir, "fanospike-otherfs-probe"));
    creat_file(jp(scratch, "control-after-otherfs"));
    msleep(80);
    n = drain(fan, "control on marked fs", 350, evbuf, 4096, 0, 10, &st);
    check(find_ev(evbuf, n, FAN_CREATE, "control-after-otherfs") != NULL,
          "control: group still live, create on marked fs reported");
    unlink(jp(scratch, "control-after-otherfs"));

    /* ============================ TEST 9: registration cost */
    printf("\n=== TEST 9: re-mark / remove timing (target %s) ===\n", marked_path);
    for (int i = 0; i < 3; i++) {
        t0 = now_ms();
        r = fanotify_mark(fan, FAN_MARK_ADD | FAN_MARK_FILESYSTEM, DIRENT_MASK, AT_FDCWD,
                          marked_path);
        double dt = now_ms() - t0;
        printf("re-mark #%d (idempotent FAN_MARK_ADD|FAN_MARK_FILESYSTEM): %s, %.0f us\n",
               i + 1, r == 0 ? "OK" : strerror(errno), dt * 1000);
    }
    t0 = now_ms();
    r = fanotify_mark(fan, FAN_MARK_REMOVE | FAN_MARK_FILESYSTEM,
                      DIRENT_MASK | FAN_RENAME, AT_FDCWD, marked_path);
    printf("FAN_MARK_REMOVE|FAN_MARK_FILESYSTEM: %s, %.0f us\n",
           r == 0 ? "OK" : strerror(errno), (now_ms() - t0) * 1000);
    creat_file(jp(scratch, "after-remove"));
    msleep(80);
    n = drain(fan, "after mark removal", 300, evbuf, 4096, 0, 5, &st);
    check(find_ev(evbuf, n, FAN_CREATE, "after-remove") == NULL,
          "after FAN_MARK_REMOVE no further events arrive");
    unlink(jp(scratch, "after-remove"));

    printf("\nall in-process tests done\n");
    close(fan);
    return 0;
}
