package taskdb

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// Lock coordinates the daemon and offline migration/restore operations.
func Lock(path string, exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	if err := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state is in use by another daemon or migration: %w", err)
	}
	return f, nil
}
