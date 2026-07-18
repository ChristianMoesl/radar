package integration

import (
	"context"

	"radar/internal/protocol"
)

type CleanupPreviewRequest struct {
	Task protocol.Task
}

type CleanupRequest struct {
	Target protocol.CleanupTarget
	Force  bool
}

type CleanupProvider interface {
	PreviewCleanup(ctx context.Context, req CleanupPreviewRequest) ([]protocol.CleanupTarget, error)
	Cleanup(ctx context.Context, req CleanupRequest) (protocol.CleanupTarget, error)
}
