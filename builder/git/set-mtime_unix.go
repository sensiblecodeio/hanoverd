// +build !darwin

package git

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func Chtimes(path string, atime, mtime time.Time) error {
	// https://github.com/torvalds/linux/blob/2decb2682f80759f631c8332f9a2a34a02150a03/include/uapi/linux/fcntl.h#L56
	var utimes [2]unix.Timespec
	utimes[0] = unix.NsecToTimespec(atime.UnixNano())
	utimes[1] = unix.NsecToTimespec(mtime.UnixNano())

	if e := unix.UtimesNanoAt(unix.AT_FDCWD, path, utimes[0:], unix.AT_SYMLINK_NOFOLLOW); e != nil {
		return &os.PathError{"futimesat", path, e}
	}
	return nil
}
