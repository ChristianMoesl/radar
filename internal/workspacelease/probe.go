package workspacelease

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// ProbeExclusive explicitly tests the native exclusive deletion lease, without
// creating directories/files, opening for writes or reading lock contents. A
// successful test briefly holds then releases the existing inode's lock. It can
// transiently contend with a new operation; callers must obtain explicit probe
// consent. It never reserves execution eligibility or identifies a holder.
// Missing files are unknown: another process could still hold an unlinked inode.
// The private fingerprint detects observed inode/status drift, not ABA changes.
func ProbeExclusive(ctx context.Context, path string) (string, [32]byte) {
	var empty [32]byte
	unavailable := func() (string, [32]byte) { return "unavailable", empty }
	if ctx.Err() != nil || !filepath.IsAbs(path) || len(path) > 4096 {
		return unavailable()
	}
	dir, name, err := location(path)
	if err != nil {
		return unavailable()
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "unknown_no_lock_file", sha256.Sum256([]byte("lease_directory_absent"))
	}
	if err != nil {
		return unavailable()
	}
	directory := os.NewFile(uintptr(fd), dir)
	defer directory.Close()
	dirInfo, err := directory.Stat()
	if err != nil {
		return unavailable()
	}
	fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		current, currentErr := os.Lstat(dir)
		if currentErr != nil || !os.SameFile(dirInfo, current) || !current.IsDir() || ctx.Err() != nil {
			return unavailable()
		}
		return "unknown_no_lock_file", fingerprint("lease_file_absent", dirInfo)
	}
	if err != nil {
		return unavailable()
	}
	file := os.NewFile(uintptr(fileFD), name)
	defer file.Close() // Also releases any acquired lock, including error paths.
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 || ctx.Err() != nil {
		return unavailable()
	}
	evidence := "exclusive_available_at_probe"
	err = unix.Flock(fileFD, unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		evidence = "contended_at_probe"
	} else if err != nil {
		return unavailable()
	}
	current, currentErr := os.Lstat(filepath.Join(dir, name))
	currentDir, dirErr := os.Lstat(dir)
	after, statErr := file.Stat()
	if currentErr != nil || dirErr != nil || statErr != nil || !os.SameFile(dirInfo, currentDir) || !currentDir.IsDir() || !os.SameFile(info, current) || !current.Mode().IsRegular() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || ctx.Err() != nil {
		return unavailable()
	}
	return evidence, fingerprint(evidence, info)
}

func fingerprint(evidence string, info os.FileInfo) [32]byte {
	stat := info.Sys().(*syscall.Stat_t)
	return sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d/%d/%d", evidence, stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())))
}
