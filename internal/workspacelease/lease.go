// Package workspacelease coordinates active agent work with workspace deletion.
package workspacelease

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"radar/internal/socket"
)

func Acquire(path string, exclusive bool) (func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	base, err := socket.Path()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(base), "leases")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	name := filepath.Join(dir, hex.EncodeToString(sum[:])+".lock")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	if err := unix.Flock(int(f.Fd()), op|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("workspace is in use by another operation: %s", abs)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
