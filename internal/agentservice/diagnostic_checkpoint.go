package agentservice

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"radar/internal/protocol"
)

// Diagnostic checkpoint capture is separate from unrestricted native loaders.
// Callers retain their own missing-file semantics and shared native decoders.
func diagnosticCheckpoint(path string) ([]byte, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	return readDiagnosticCheckpoint(file, path)
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
