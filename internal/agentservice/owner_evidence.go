package agentservice

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// A missing PID record retains native discovery semantics. Unreadable, unsafe or
// invalid evidence is not a dead owner and cannot authorize replacement/removal.
// This is a point-in-time PID observation, not an incarnation or peer attestation.
func ownerAlive(reg Registration) (bool, error) {
	unavailable := func() (bool, error) {
		return false, fmt.Errorf("runner owner PID evidence unavailable; inspect retained agent data before recovery or removal")
	}
	dir, err := instanceDir(reg.ID)
	if err != nil {
		return unavailable()
	}
	path := filepath.Join(dir, "pid")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return unavailable()
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	const bound = 64
	if err != nil || !before.Mode().IsRegular() || before.Size() > bound {
		return unavailable()
	}
	data, err := io.ReadAll(io.LimitReader(file, bound+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > bound || int64(len(data)) != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, current) || !current.Mode().IsRegular() || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
		return unavailable()
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 32)
	if err != nil || pid <= 0 {
		return unavailable()
	}
	// EPERM establishes an existing process, not its absence. Other unexpected
	// liveness errors remain unknown; never wrap a large integer into pid_t.
	switch err := syscall.Kill(int(pid), 0); {
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return true, nil
	case err != nil:
		return unavailable()
	default:
		return runnerProcessAlive(int(pid)), nil
	}
}
