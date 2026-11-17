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
	dir, name, err := location(abs)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
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

// location is shared by acquisition and explicit probes; abs is absolute.
func location(abs string) (string, string, error) {
	base, err := socket.Path()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(filepath.Dir(base), "leases"), hex.EncodeToString(sum[:]) + ".lock", nil
}
