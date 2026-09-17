package integration

import (
	"context"

	"radar/internal/protocol"
)

// TaskLifecycleProvider persists remote-driven lifecycle changes in an authored task's source.
// Work items are authoritative contributors with confirmed signals. A provider
// may reopen on active work or preserve an explicit reopening baseline.
// A nil observation means no source mutation was needed.
type TaskLifecycleProvider interface {
	Integration
	ReconcileLifecycle(ctx context.Context, ref protocol.SourceRef, workItems []protocol.SourceRef) (*Observation, error)
}
