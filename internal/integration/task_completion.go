package integration

import (
	"context"

	"radar/internal/protocol"
)

// TaskCompletionProvider persists remote completion in an authored task's source.
// Work items are authoritative contributors with confirmed signals. A provider
// may also record a reopening baseline while some work items are still active.
// A nil observation means no source mutation was needed.
type TaskCompletionProvider interface {
	Integration
	ReconcileCompletion(ctx context.Context, ref protocol.SourceRef, workItems []protocol.SourceRef) (*Observation, error)
}
