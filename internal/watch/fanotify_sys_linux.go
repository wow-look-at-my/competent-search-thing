package watch

// The fanotify notifier's production syscall seam implementations,
// split from fanotify_linux.go (which keeps the notifier logic those
// seams plug into). The _linux filename suffix carries the same GOOS
// constraint as the sibling.

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// --- production seam implementations -----------------------------

// fanoInit creates the fanotify group: notification class, fid mode
// with directory handles + entry names (FAN_REPORT_DFID_NAME), and
// non-blocking reads so the loop can interleave a poll on the stop
// pipe.
func fanoInit() (int, error) {
	return unix.FanotifyInit(
		unix.FAN_CLASS_NOTIF|unix.FAN_CLOEXEC|unix.FAN_NONBLOCK|unix.FAN_REPORT_DFID_NAME,
		unix.O_RDONLY|unix.O_LARGEFILE|unix.O_CLOEXEC)
}

// fanoMark adds the whole-filesystem dirent mark for the superblock
// containing path. Needs CAP_SYS_ADMIN (EPERM otherwise) and a
// non-null fsid (ENODEV since 6.8).
func fanoMark(fd int, path string) error {
	return unix.FanotifyMark(fd, unix.FAN_MARK_ADD|unix.FAN_MARK_FILESYSTEM,
		fanoMarkMask, unix.AT_FDCWD, path)
}

// fanoReadFn builds the production readFn: poll on the fanotify fd
// plus the stop pipe, then a non-blocking read, retrying EINTR/EAGAIN.
// Close wakes the poll by closing the pipe's write end -- never by
// closing the fanotify fd under a concurrent poll, which races fd
// reuse.
func fanoReadFn(stopR *os.File) func(int, []byte) (int, error) {
	return func(fd int, buf []byte) (int, error) {
		for {
			fds := []unix.PollFd{
				{Fd: int32(fd), Events: unix.POLLIN},
				{Fd: int32(stopR.Fd()), Events: unix.POLLIN},
			}
			if _, err := unix.Poll(fds, -1); err != nil {
				if err == unix.EINTR {
					continue
				}
				return 0, err
			}
			if fds[1].Revents != 0 {
				return 0, errFanoClosed
			}
			nr, err := unix.Read(fd, buf)
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			return nr, err
		}
	}
}

// fanoResolve turns a directory file handle into the directory's
// CURRENT absolute path: open_by_handle_at against the superblock's
// mount fd (O_PATH: no dentry-open side effects, works on
// search-only permissions), then readlink of the /proc/self/fd
// entry -- the fanotify(7) example's flow. Needs CAP_DAC_READ_SEARCH;
// ESTALE means the directory is gone.
func fanoResolve(mountFD int, handleType int32, handle []byte) (string, error) {
	h := unix.NewFileHandle(handleType, handle)
	fd, err := unix.OpenByHandleAt(mountFD, h, unix.O_RDONLY|unix.O_PATH)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
}

// statfsFsid reads the filesystem id events will carry for path
// (statfs f_fsid == the fanotify record fsid, per fanotify(7)).
func statfsFsid(path string) (fanoFsid, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fanoFsid{}, err
	}
	return fanoFsid{st.Fsid.Val[0], st.Fsid.Val[1]}, nil
}
