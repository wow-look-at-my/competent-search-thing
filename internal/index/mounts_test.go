package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMountSkips(t *testing.T) {
	mounts := strings.Join([]string{
		"/dev/sda1 / ext4 rw,relatime 0 0",
		"proc /proc proc rw,nosuid 0 0",
		"sysfs /sys sysfs rw,nosuid 0 0",
		"tmpfs /run tmpfs rw,nosuid 0 0",
		"server:/export /mnt/nfs nfs rw,vers=4.2 0 0",
		"//srv/share /mnt/cifs cifs rw,vers=3.0 0 0",
		"user@host:/ /mnt/ssh fuse.sshfs rw,nosuid 0 0",
		"gvfsd-fuse /home/me/.gvfs fuse.gvfsd-fuse rw,nosuid 0 0",
		"appimage /mnt/app fuse ro 0 0",
		"/dev/sdb1 /data ext4 rw 0 0",
		"/dev/sdc1 /pool xfs rw 0 0",
		"/dev/sdd1 /big btrfs rw 0 0",
		"server:/e /mnt/with\\040space nfs rw 0 0",
		"server:/f /mnt/glob[1] nfs rw 0 0",
		"server:/g /outside/tree nfs rw 0 0",
		"overlay / overlay rw,lowerdir=/a 0 0",
		"malformed-line-with-two f",
		"server:/h /mnt/explicit nfs rw 0 0",
	}, "\n")

	roots := []string{"/mnt", "/home/me", "/mnt/explicit"}
	got := ParseMountSkips(strings.NewReader(mounts), roots)
	require.Equal(t, []string{
		"/mnt/nfs",
		"/mnt/cifs",
		"/mnt/ssh",
		"/home/me/.gvfs",
		"/mnt/app",
		"/mnt/with space", // \040 decoded
	}, got)

	// The dropped cases, spelled out: local filesystems are kept
	// (ext4/xfs/btrfs never appear), overlay is deliberately not a
	// skip type (container roots are overlay mounts), "/" is never
	// returned, /proc//sys//run are outside the given roots, the
	// glob-metachar mountpoint is dropped, the mountpoint configured
	// as an explicit root is the escape hatch, and malformed lines are
	// ignored.
	for _, absent := range []string{"/", "/proc", "/sys", "/run", "/data", "/pool", "/big", "/mnt/glob[1]", "/outside/tree", "/mnt/explicit"} {
		require.NotContains(t, got, absent)
	}
}

func TestParseMountSkipsWholeFilesystemRoot(t *testing.T) {
	mounts := strings.Join([]string{
		"/dev/sda1 / ext4 rw 0 0",
		"proc /proc proc rw 0 0",
		"server:/export /mnt/nfs nfs4 rw 0 0",
		"none /sys/fs/cgroup cgroup2 rw 0 0",
	}, "\n")
	got := ParseMountSkips(strings.NewReader(mounts), []string{"/"})
	require.Equal(t, []string{"/proc", "/mnt/nfs", "/sys/fs/cgroup"}, got,
		"with the whole-filesystem root, every virtual/network mount is under it")
}

func TestParseMountSkipsCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < mountSkipCap+50; i++ {
		fmt.Fprintf(&b, "server:/x /mnt/nfs%04d nfs rw 0 0\n", i)
	}
	got := ParseMountSkips(strings.NewReader(b.String()), []string{"/"})
	require.Len(t, got, mountSkipCap, "the skip list is capped; everything past it walks normally")
}

func TestSkipFSType(t *testing.T) {
	for _, f := range []string{"proc", "sysfs", "tmpfs", "cgroup2", "nfs", "nfs4", "cifs", "9p", "fuse", "fuse.sshfs", "fuse.rclone", "fuse.anything-else"} {
		require.True(t, skipFSType(f), f)
	}
	for _, f := range []string{"ext4", "xfs", "btrfs", "vfat", "zfs", "overlay", "squashfs", "fusectl2", "confused"} {
		require.False(t, skipFSType(f), f)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := map[string]string{
		`/plain`:                 "/plain",
		`/with\040space`:         "/with space",
		`/tab\011here`:           "/tab\there",
		`/back\134slash`:         `/back\slash`,
		`/trailing\04`:           `/trailing\04`, // incomplete escape kept verbatim
		`/not\089octal`:          `/not\089octal`,
		`/two\040\040spaces`:     "/two  spaces",
		`/mixed\040and\134both`:  `/mixed and\both`,
		`/ends-with-backslash\\`: `/ends-with-backslash\\`,
	}
	for in, want := range cases {
		require.Equal(t, want, unescapeMountPath(in), in)
	}
}

func TestParseMountpoints(t *testing.T) {
	mounts := strings.Join([]string{
		"/dev/sda1 / ext4 rw,relatime 0 0",
		"proc /proc proc rw,nosuid 0 0",
		"tmpfs /run tmpfs rw,nosuid 0 0",
		"server:/export /mnt/nfs nfs rw,vers=4.2 0 0",
		"user@host:/ /mnt/ssh fuse.sshfs rw,nosuid 0 0",
		"/dev/sdb1 /data ext4 rw 0 0",
		"/dev/loop3 /snap/tool/42 squashfs ro 0 0",
		"/dev/sdc1 /mnt/with\\040space btrfs rw 0 0",
		"overlay / overlay rw,lowerdir=/a 0 0",
		"weird relative-mountpoint ext4 rw 0 0",
		"malformed-line-with-two f",
	}, "\n")
	got := ParseMountpoints(strings.NewReader(mounts))
	require.Equal(t, []string{"/", "/data", "/snap/tool/42", "/mnt/with space", "/"}, got,
		"real filesystems only (virtual/network/FUSE dropped), escapes decoded, relative and malformed lines ignored")
}

func TestRealMountpointsRealTable(t *testing.T) {
	// Machine-dependent content, so only invariants are asserted: every
	// mountpoint absolute and of a walkable type ("/" itself is a
	// legitimate entry here, unlike the skip list). On non-linux this
	// returns nil, which the loop trivially satisfies.
	for _, mp := range RealMountpoints() {
		require.True(t, filepath.IsAbs(mp), mp)
	}
}

func TestSystemMountSkipsRealTable(t *testing.T) {
	// Machine-dependent content, so only invariants are asserted: never
	// "/", always absolute, always under the root. On non-linux this
	// returns nil, which the loop trivially satisfies.
	for _, mp := range SystemMountSkips([]string{"/"}) {
		require.True(t, filepath.IsAbs(mp))
		require.NotEqual(t, "/", mp)
	}
	require.Nil(t, SystemMountSkips(nil), "no roots means nothing is under them")
}

func TestBuildFromDiskPrunesMountSkips(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	mnt := filepath.Join(root, "mnt")
	require.NoError(t, os.MkdirAll(filepath.Join(keep, "inner"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(mnt, "remote"), 0o755))
	writeFile(t, filepath.Join(keep, "kept.txt"))
	writeFile(t, filepath.Join(mnt, "unreachable.txt"))

	orig := mountSkips
	mountSkips = func(roots []string) []string {
		require.Equal(t, []string{root}, roots, "the manager's configured roots feed the skip computation")
		return []string{mnt}
	}
	t.Cleanup(func() { mountSkips = orig })

	m := NewManager([]string{root}, []string{".git"}, 0)
	count, _, err := m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, 3, count, "keep/, keep/inner and keep/kept.txt; the mnt subtree is pruned entirely")

	require.Empty(t, m.Query("unreachable", 0), "files under the skipped mountpoint never enter the index")
	require.Empty(t, m.Query("remote", 0))
	require.Empty(t, m.Query("mnt", 0), "the mountpoint entry itself is pruned")
	require.NotEmpty(t, m.Query("kept", 0))
	require.Equal(t, []string{".git"}, m.Excludes(), "the configured excludes are never mutated by the merge")

	// The skips are recomputed on every rebuild: with the mount gone,
	// the same manager indexes the subtree.
	mountSkips = func([]string) []string { return nil }
	_, _, err = m.BuildFromDisk(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, m.Query("unreachable", 0), "a vanished mount is walked again on the next rebuild")
}
