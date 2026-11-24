package agentservice

import "os"

// Observe only the selected registration's fixed collection checkpoint. Unlike
// native outbox loading, diagnostic reads must not follow a leaf symlink, wait
// for a FIFO writer or read an unbounded file. Native decoding stays shared.
func diagnosticCollection(reg Registration) (collectionState, error) {
	data, _, err := diagnosticCheckpoint(collectionPath(reg))
	if os.IsNotExist(err) {
		// Exactly native missing-file semantics, without creating a checkpoint
		// or asserting that collection/delivery/work execution ever succeeded.
		return newCollectionState(reg), nil
	}
	if err != nil {
		return collectionState{}, err
	}
	return decodeCollection(reg, data)
}
