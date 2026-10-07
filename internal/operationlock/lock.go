// Package operationlock prevents upgrades from interrupting accepted mutations.
package operationlock

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"

	"radar/internal/socket"
	"radar/internal/version"
)

// Acquire is nonblocking: an updater declines during active work; clients get
// an actionable busy error during activation. Unix releases locks after crashes.
func Acquire(exclusive bool) (func(), error) {
	if runtime.GOOS != "darwin" {
		return func() {}, nil
	}
	path, err := socket.Path()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".operations.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	if err := unix.Flock(int(f.Fd()), op|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("Radar update/workspace mutation in progress; retry when it finishes")
	}
	if !exclusive {
		if err := version.CheckInstalled(); err != nil {
			f.Close()
			return nil, err
		}
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
