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

// DiagnosticWorkspaceRemovalOwners observes only registrations using the native
// decoder and removal-owner predicate. The private fingerprint detects registry
// drift, not execution eligibility. No runner, checkpoint, token or lease is read.
// The narrower diagnostic scan fails unavailable rather than returning a partial
// count when any registration is unsafe/invalid or its scan budget is exceeded.
// Up to 512 absolute target paths share one bounded registry pass, not one scan
// per resource. Counts are per path, not unique owners of the whole bundle.
func DiagnosticWorkspaceRemovalOwners(ctx context.Context, paths []string) (map[string]int, [32]byte, error) {
	return diagnosticWorkspaceRemovalOwners(ctx, paths, nil)
}

// DiagnosticWorkspaceOwnerIDs supplies the same complete bounded registration
// observation for inventories that already expose owner identities. Partial IDs
// are discarded on any failure; no second scan or runtime/private-file read.
func DiagnosticWorkspaceOwnerIDs(ctx context.Context, paths []string) (map[string][]string, error) {
	owners := make(map[string][]string)
	if _, _, err := diagnosticWorkspaceRemovalOwners(ctx, paths, owners); err != nil {
		return nil, err
	}
	return owners, nil
}

func diagnosticWorkspaceRemovalOwners(ctx context.Context, paths []string, owners map[string][]string) (map[string]int, [32]byte, error) {
	var empty [32]byte
	unavailable := func() (map[string]int, [32]byte, error) {
		return nil, empty, fmt.Errorf("registered ownership evidence unavailable")
	}
	expired := func() bool {
		deadline, bounded := ctx.Deadline()
		return ctx.Err() != nil || (bounded && !time.Now().Before(deadline))
	}
	if len(paths) < 1 || len(paths) > 512 || expired() {
		return unavailable()
	}
	counts := make(map[string]int, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) || len(path) > 4096 {
			return unavailable()
		}
		counts[path] = 0
		if owners != nil {
			owners[path] = []string{}
		}
	}
	root, err := Root()
	if err != nil {
		return unavailable()
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return counts, sha256.Sum256([]byte("registration_root_absent")), nil
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
		// Match production registration discovery: only valid instance dirs.
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
		for path := range counts {
			if registrationWithinWorkspace(reg, filepath.Clean(path)) {
				counts[path]++ // Desired=false still holds ownership, exactly as production.
				if owners != nil {
					owners[path] = append(owners[path], reg.ID)
				}
			}
		}
	}
	after, statErr := dir.Stat()
	current, pathErr := os.Lstat(root)
	if statErr != nil || pathErr != nil || !os.SameFile(before, current) || !current.IsDir() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) || expired() {
		return unavailable()
	}
	var fingerprint [32]byte
	copy(fingerprint[:], hash.Sum(nil))
	return counts, fingerprint, nil
}
