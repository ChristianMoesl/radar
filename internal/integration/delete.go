package integration

import (
	"context"
	"log/slog"

	"radar/internal/protocol"
)

type DeletePreviewRequest struct {
	Task    protocol.Task
	Current protocol.CurrentContext
	Logger  *slog.Logger
}

type DeleteProvider interface {
	PreviewDelete(ctx context.Context, req DeletePreviewRequest) (protocol.DeletePreview, bool, error)
	Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error)
}
