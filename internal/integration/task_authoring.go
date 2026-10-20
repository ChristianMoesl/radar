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
	SetLifecycle(ctx context.Context, ref protocol.SourceRef, state string) (AuthoredTaskIdentity, error)
	SetPriority(ctx context.Context, ref protocol.SourceRef, priority string) (AuthoredTaskIdentity, error)
	PreviewDelete(ctx context.Context, ref protocol.SourceRef) (protocol.TaskDeletionPreview, error)
	Delete(ctx context.Context, ref protocol.SourceRef, preview protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error)
}

// TaskDetailsProvider edits database-owned presentation and note associations.
type TaskDetailsProvider interface {
	SetTitle(context.Context, protocol.SourceRef, string) (AuthoredTaskIdentity, error)
	ReassociateNote(context.Context, protocol.SourceRef, string) (AuthoredTaskIdentity, error)
}
