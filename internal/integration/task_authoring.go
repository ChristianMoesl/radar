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
}
