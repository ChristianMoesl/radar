package workspacegroup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const diagnosticRegistryBytes = 1 << 20
const diagnosticRegistryEntries = 1024

// DiagnosticLoad is a bounded observation, not native execution admission. It
// never creates roots/coordination files or acquires the native mutex/file lock.
// Native writers replace the registry atomically; observed replacement during
// capture fails unavailable. This is not a transaction or future reservation.
func DiagnosticLoad(root string) (Registry, error) {
	return diagnosticLoad(root, readDiagnosticRegistry)
}

func diagnosticLoad(root string, read func(diagnosticRegistryFile, string) ([]byte, error)) (Registry, error) {
	unavailable := func() (Registry, error) {
		return Registry{}, fmt.Errorf("workspace registry diagnostic evidence unavailable")
	}
	if !filepath.IsAbs(root) {
		return unavailable()
	}
	root = filepath.Clean(root)
	empty := Registry{Version: Version, Workspaces: []Workspace{}}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	rootFD, err := unix.Open(root, flags|unix.O_DIRECTORY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return unavailable()
	}
	directory := os.NewFile(uintptr(rootFD), root)
	defer directory.Close()
	before, err := directory.Stat()
	if err != nil {
		return unavailable()
	}
	stable := func() bool {
		current, err := os.Lstat(root)
		return err == nil && current.IsDir() && os.SameFile(before, current)
	}
	fileFD, err := unix.Openat(rootFD, FileName, flags, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && stable() {
			return empty, nil
		}
		return unavailable()
	}
	file := os.NewFile(uintptr(fileFD), Path(root))
	defer file.Close()
	data, err := read(file, Path(root))
	if err != nil || !stable() {
		return unavailable()
	}
	var registry Registry
	if json.Unmarshal(data, &registry) != nil || len(registry.Workspaces) > diagnosticRegistryEntries {
		return unavailable()
	}
	registry, err = validateRegistry(registry, Path(root))
	if err != nil {
		return unavailable() // Native validation errors can contain private recipe values.
	}
	return registry, nil
}

type diagnosticRegistryFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
}

func readDiagnosticRegistry(file diagnosticRegistryFile, path string) ([]byte, error) {
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > diagnosticRegistryBytes {
		return nil, fmt.Errorf("workspace registry diagnostic read unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, diagnosticRegistryBytes+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > diagnosticRegistryBytes || int64(len(data)) != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(before, current) || !current.Mode().IsRegular() || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
		return nil, fmt.Errorf("workspace registry diagnostic read changed or incomplete")
	}
	return data, nil
}
