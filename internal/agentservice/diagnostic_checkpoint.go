package agentservice

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"radar/internal/protocol"
)

// Diagnostic checkpoint capture is separate from unrestricted native loaders.
// Callers retain their own missing-file semantics and shared native decoders.
func diagnosticCheckpoint(path string) ([]byte, os.FileInfo, error) {
	return captureDiagnosticCheckpoint(path, readDiagnosticCheckpoint)
}

// Bind the immediate root and instance directories as well as the leaf. A
// symlink/replacement ancestor can retain the leaf's inode, bytes and metadata.
// The reader parameter permits deterministic capture-boundary tests without
// global hooks. Directory mtime is not identity: unrelated writes are allowed.
func captureDiagnosticCheckpoint(path string, read func(diagnosticCheckpointFile, string) ([]byte, os.FileInfo, error)) ([]byte, os.FileInfo, error) {
	dir := filepath.Dir(path)
	rootPath := filepath.Dir(dir)
	unavailable := func() ([]byte, os.FileInfo, error) {
		return nil, nil, fmt.Errorf("checkpoint directories changed or unavailable")
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	rootFD, err := unix.Open(rootPath, flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return unavailable()
	}
	root := os.NewFile(uintptr(rootFD), rootPath)
	defer root.Close()
	rootInfo, err := root.Stat()
	if err != nil {
		return unavailable()
	}
	instanceFD, err := unix.Openat(rootFD, filepath.Base(dir), flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return unavailable()
	}
	instance := os.NewFile(uintptr(instanceFD), dir)
	defer instance.Close()
	instanceInfo, err := instance.Stat()
	if err != nil {
		return unavailable()
	}
	stable := func() bool {
		currentRoot, rootErr := os.Lstat(rootPath)
		currentInstance, instanceErr := os.Lstat(dir)
		return rootErr == nil && instanceErr == nil && currentRoot.IsDir() && currentInstance.IsDir() && os.SameFile(rootInfo, currentRoot) && os.SameFile(instanceInfo, currentInstance)
	}
	fileFD, err := unix.Openat(instanceFD, filepath.Base(path), flags, 0)
	if err != nil {
		if !stable() {
			return unavailable()
		}
		return nil, nil, err // Preserve missing-leaf semantics only under stable directories.
	}
	file := os.NewFile(uintptr(fileFD), path)
	defer file.Close()
	data, info, err := read(file, path)
	if !stable() {
		return unavailable()
	}
	return data, info, err
}

type diagnosticCheckpointFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
}

// Observe descriptor and path stability around the bounded read. This detects
// observed replacement/incomplete reads, not an atomic snapshot or later drift.
func readDiagnosticCheckpoint(file diagnosticCheckpointFile, path string) ([]byte, os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocol.AgentMaxFrameBytes {
		return nil, nil, fmt.Errorf("checkpoint unavailable or exceeds diagnostic read bound")
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.AgentMaxFrameBytes+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > protocol.AgentMaxFrameBytes || int64(len(data)) != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !os.SameFile(info, current) || !current.Mode().IsRegular() || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return nil, nil, fmt.Errorf("checkpoint changed or read incomplete")
	}
	return data, info, nil
}
