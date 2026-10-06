package git

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func gitFileIdentity(path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return "", err
	}
	if stat.Btim.Sec == 0 && stat.Btim.Nsec == 0 {
		return "", errors.New("filesystem does not provide birth time")
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)), stat.Ino, stat.Btim.Sec, stat.Btim.Nsec), nil
}
