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
	dir, name, err := location(path)
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
		return nil, fmt.Errorf("workspace is in use by another operation: %s", path)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}

// Acquisition and explicit probes must resolve exactly the same identity.
func location(path string) (string, string, error) {
	physical, err := CanonicalPath(path)
	if err != nil {
		return "", "", err
	}
	base, err := socket.Path()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(physical))
	return filepath.Join(filepath.Dir(base), "leases"), hex.EncodeToString(sum[:]) + ".lock", nil
}
