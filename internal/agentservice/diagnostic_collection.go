package agentservice

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"radar/internal/protocol"
)

// Observe only the selected registration's fixed collection checkpoint. Unlike
// native outbox loading, diagnostic reads must not follow a leaf symlink, wait
// for a FIFO writer or read an unbounded file. Native decoding stays shared.
func diagnosticCollection(reg Registration) (collectionState, error) {
	path := collectionPath(reg)
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		// Exactly native missing-file semantics, without creating a checkpoint
		// or asserting that collection/delivery/work execution ever succeeded.
		return newCollectionState(reg), nil
	}
	if err != nil {
		return collectionState{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocol.AgentMaxFrameBytes {
		return collectionState{}, fmt.Errorf("collection checkpoint unavailable or exceeds diagnostic read bound")
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.AgentMaxFrameBytes+1))
	after, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || statErr != nil || pathErr != nil || len(data) > protocol.AgentMaxFrameBytes || int64(len(data)) != info.Size() || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !os.SameFile(info, current) || !current.Mode().IsRegular() || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return collectionState{}, fmt.Errorf("collection checkpoint changed or read incomplete")
	}
	return decodeCollection(reg, data)
}
