package git

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func gitFileIdentity(path string) (string, error) {
	var stat unix.Statx_t
	const required = unix.STATX_INO | unix.STATX_BTIME
	if err := unix.Statx(unix.AT_FDCWD, path, 0, required, &stat); err != nil {
		return "", err
	}
	return gitStatxIdentity(stat)
}

func gitStatxIdentity(stat unix.Statx_t) (string, error) {
	const required = unix.STATX_INO | unix.STATX_BTIME
	if stat.Mask&required != required {
		return "", errors.New("filesystem does not provide inode and birth time")
	}
	// ctime and mtime are not lifetime identities: both change during normal
	// repository use. Inode alone can be reused after a checkout is removed.
	return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev_major, stat.Dev_minor, stat.Ino, stat.Btime.Sec, stat.Btime.Nsec), nil
}
