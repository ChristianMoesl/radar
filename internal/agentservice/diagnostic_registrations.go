package agentservice

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Diagnostic inventories require one complete bounded registration observation
// before scoped checkpoint reads. Native execution discovery remains separate.
func diagnosticRegistrations() ([]Registration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	regs := []Registration{}
	if _, err := diagnosticRegistrationScan(ctx, func(reg Registration) { regs = append(regs, reg) }); err != nil {
		return nil, err // Never publish a partial registry or call it empty.
	}
	return regs, nil
}

func diagnosticRegistrationExpired(ctx context.Context) bool {
	deadline, bounded := ctx.Deadline()
	return ctx.Err() != nil || (bounded && !time.Now().Before(deadline))
}

// Share the existing owner scan's acquisition, validation and fingerprint order.
// A visitor's accumulated data is usable ONLY if the whole scan succeeds. No
// runner, checkpoint, token or lease is read, and no state is created or repaired.
func diagnosticRegistrationScan(ctx context.Context, visit func(Registration)) ([32]byte, error) {
	var empty [32]byte
	unavailable := func() ([32]byte, error) {
		return empty, fmt.Errorf("registered ownership evidence unavailable")
	}
	expired := func() bool { return diagnosticRegistrationExpired(ctx) }
	if expired() {
		return unavailable()
	}
	root, err := Root()
	if err != nil {
		return unavailable()
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return sha256.Sum256([]byte("registration_root_absent")), nil
	}
	if err != nil {
		return unavailable()
	}
	dir := os.NewFile(uintptr(fd), root)
	defer dir.Close()
	before, err := dir.Stat()
	if err != nil {
		return unavailable()
	}
	const maxEntries = 1024
	entries, err := dir.ReadDir(maxEntries + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > maxEntries || expired() {
		return unavailable()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	hash := sha256.New()
	writeInfo := func(info os.FileInfo) {
		stat := info.Sys().(*syscall.Stat_t)
		fmt.Fprintf(hash, "%d/%d/%d/%d;", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())
	}
	writeInfo(before)
	bytes := int64(0)
	for _, entry := range entries {
		if expired() {
			return unavailable()
		}
		fmt.Fprintf(hash, "%q/%d;", entry.Name(), entry.Type())
		// Match production discovery: valid instance dirs may retain no registration.
		if !entry.IsDir() || !validID.MatchString(entry.Name()) {
			continue
		}
		instanceFD, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return unavailable()
		}
		instance := os.NewFile(uintptr(instanceFD), entry.Name())
		fileFD, openErr := unix.Openat(instanceFD, "registration.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			instance.Close()
			continue // Production retains unregistered durable-data directories.
		}
		if openErr != nil {
			instance.Close()
			return unavailable()
		}
		file := os.NewFile(uintptr(fileFD), "registration.json")
		info, statErr := file.Stat()
		const fileBound = 64 * 1024
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > fileBound || bytes+info.Size() > 1024*1024 {
			file.Close()
			instance.Close()
			return unavailable()
		}
		readBound := min(int64(fileBound), 1024*1024-bytes)
		data, readErr := io.ReadAll(io.LimitReader(file, readBound+1))
		after, afterErr := file.Stat()
		current, pathErr := os.Lstat(filepath.Join(root, entry.Name(), "registration.json"))
		file.Close()
		instance.Close()
		if readErr != nil || afterErr != nil || pathErr != nil || int64(len(data)) > readBound || int64(len(data)) != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !os.SameFile(info, current) || !current.Mode().IsRegular() || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
			return unavailable()
		}
		bytes += int64(len(data))
		reg, decodeErr := decodeRegistration(data, entry.Name())
		if decodeErr != nil || !filepath.IsAbs(reg.Workspace.Path) {
			return unavailable()
		}
		writeInfo(info)
		hash.Write(data)
		if visit != nil {
			visit(reg)
		}
	}
	after, statErr := dir.Stat()
	current, pathErr := os.Lstat(root)
	if statErr != nil || pathErr != nil || !os.SameFile(before, current) || !current.IsDir() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) || expired() {
		return unavailable()
	}
	var fingerprint [32]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint, nil
}
