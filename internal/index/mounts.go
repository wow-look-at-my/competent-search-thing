package index

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Mount-aware walk skips. A whole-filesystem walk must not descend
// into virtual kernel filesystems (endless synthetic trees; /proc and
// /sys are excluded by config too, but mounts move around) or network
// filesystems (a walk over NFS/CIFS/sshfs hangs on server stalls and
// hammers the wire). Mountpoints of those types are computed fresh at
// every BuildFromDisk -- mounts change between rebuilds -- and fed to
// the walk as full-path exclude patterns, so the walker prunes the
// mountpoint directory entry exactly like a configured exclude.
//
// Escape hatch: a mountpoint that IS one of the configured roots is
// never skipped -- listing a network mount as an explicit root is the
// documented way to index it anyway (the walker dedupes nested roots,
// so the entry costs nothing; it only keeps the skip off).

// mountSkipCap bounds the skip list; a mount table bigger than this is
// pathological (containers with thousands of binds) and everything
// past the cap walks normally rather than growing the pattern list
// without bound.
const mountSkipCap = 256

// mountSkips is BuildFromDisk's seam over SystemMountSkips; tests
// inject fake skip lists through it.
var mountSkips = SystemMountSkips

// virtualFSTypes are kernel-synthetic filesystems: nothing in them is
// a user file. overlay is deliberately NOT here -- container roots are
// overlay mounts, and skipping them would skip everything.
var virtualFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
	"tmpfs": true, "cgroup": true, "cgroup2": true, "pstore": true,
	"securityfs": true, "debugfs": true, "tracefs": true,
	"fusectl": true, "configfs": true, "ramfs": true, "autofs": true,
	"mqueue": true, "hugetlbfs": true, "binfmt_misc": true,
	"rpc_pipefs": true,
}

// networkFSTypes are remote filesystems a local index must not walk.
// FUSE mounts ("fuse" and every "fuse.*", matched in skipFSType) are
// skipped wholesale too: the common FUSE mounts are network-backed
// (sshfs, rclone, gvfs), and a local-FUSE user can add the mountpoint
// as an explicit root (see the escape hatch above).
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true,
	"smbfs": true, "sshfs": true, "9p": true, "afs": true,
	"glusterfs": true, "ceph": true, "davfs": true,
}

// skipFSType reports whether a mount of this filesystem type must not
// be walked.
func skipFSType(fstype string) bool {
	if virtualFSTypes[fstype] || networkFSTypes[fstype] {
		return true
	}
	return fstype == "fuse" || strings.HasPrefix(fstype, "fuse.")
}

// ParseMountSkips reads /proc/self/mounts-format lines (fields: device
// mountpoint fstype options...) from r and returns the mountpoints a
// walk over roots must skip: virtual and network filesystem types (see
// the type sets above) that lie STRICTLY under one of the roots.
// Octal escapes in mountpoints (\040 for space etc.) are decoded. The
// root filesystem entry ("/") is never returned, a mountpoint equal to
// a configured root is never returned (the index-it-anyway escape
// hatch), and a mountpoint containing filepath.Match metacharacters is
// dropped -- the skips are consumed as match patterns, and a mount
// path with glob characters in it is not worth escaping machinery.
// The list is capped at mountSkipCap entries. Malformed lines are
// ignored; the function never fails.
func ParseMountSkips(r io.Reader, roots []string) []string {
	cleanRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if a, err := filepath.Abs(root); err == nil {
			cleanRoots = append(cleanRoots, filepath.Clean(a))
		}
	}
	var skips []string
	sc := bufio.NewScanner(r)
	for sc.Scan() && len(skips) < mountSkipCap {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || !skipFSType(fields[2]) {
			continue
		}
		mp := unescapeMountPath(fields[1])
		if mp == "/" || !filepath.IsAbs(mp) {
			continue
		}
		mp = filepath.Clean(mp)
		if strings.ContainsAny(mp, `*?[\`) {
			continue // never a valid filepath.Match literal; dropped
		}
		under := false
		for _, cr := range cleanRoots {
			if mp == cr {
				under = false // explicit root: the escape hatch wins
				break
			}
			if isWithin(mp, cr) {
				under = true
			}
		}
		if under {
			skips = append(skips, mp)
		}
	}
	return skips
}

// ParseMountpoints reads /proc/self/mounts-format lines (fields:
// device mountpoint fstype options...) from r and returns the
// mountpoints of REAL, walkable filesystems: everything skipFSType
// would prune (virtual kernel filesystems, network filesystems, FUSE)
// is dropped, mirroring the walk's own mount skipping, so a consumer
// diffing successive snapshots never drags a network mount into the
// index. Octal escapes are decoded, relative mountpoints and malformed
// lines are ignored, and the order is the table's.
func ParseMountpoints(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || skipFSType(fields[2]) {
			continue
		}
		mp := unescapeMountPath(fields[1])
		if !filepath.IsAbs(mp) {
			continue
		}
		out = append(out, filepath.Clean(mp))
	}
	return out
}

// RealMountpoints returns the current mount table's walkable
// mountpoints from /proc/self/mounts (see ParseMountpoints). Linux
// only; on other platforms, and on any read failure, it returns nil.
// The watch layer's sweeper diffs successive snapshots to
// force-reconcile mountpoints appearing or vanishing under the indexed
// roots.
func RealMountpoints() []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseMountpoints(f)
}

// SystemMountSkips returns the mountpoints under roots that
// BuildFromDisk must exclude, read from /proc/self/mounts. Linux only;
// on other platforms, and on any read failure, it returns nil -- mount
// skipping is a best-effort refinement, never an error source.
func SystemMountSkips(roots []string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	return ParseMountSkips(f, roots)
}

// unescapeMountPath decodes the 3-digit octal escapes /proc mount
// tables use for whitespace and backslashes (\040 = space).
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+3 < len(s) && isOctalDigit(s[i+1]) && isOctalDigit(s[i+2]) && isOctalDigit(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isOctalDigit(c byte) bool { return c >= '0' && c <= '7' }
