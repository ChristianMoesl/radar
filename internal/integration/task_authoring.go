package integration

import (
	"context"

	"radar/internal/protocol"
)

type AuthoredTaskIdentity struct {
	SourceRefID string
}

type TaskAuthoringProvider interface {
	Source
	Create(ctx context.Context, title string) (AuthoredTaskIdentity, error)
	// Explicit Done receives the latest known aggregate refs so its remote
	// baseline is saved with the mutation, without collecting remote sources.
	SetLifecycle(ctx context.Context, ref protocol.SourceRef, state string, contributors []protocol.SourceRef) (AuthoredTaskIdentity, error)
	// Priority captures the aggregate's current actions when selecting Low and
	// can adopt source-only work through the ordinary durable binding path.
	SetPriority(ctx context.Context, task protocol.Task, priority string) (AuthoredTaskIdentity, error)
	PreviewDelete(ctx context.Context, ref protocol.SourceRef) (protocol.TaskDeletionPreview, error)
	Delete(ctx context.Context, ref protocol.SourceRef, preview protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error)
}

// TaskDetailsProvider edits database-owned presentation and note associations.
type TaskDetailsProvider interface {
	SetTitle(context.Context, protocol.SourceRef, string) (AuthoredTaskIdentity, error)
	ReassociateNote(context.Context, protocol.SourceRef, string) (AuthoredTaskIdentity, error)
}
