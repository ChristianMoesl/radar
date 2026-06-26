package integration

import (
	"context"
	"log/slog"

	"radar/internal/protocol"
)

type ReconcileRequest struct {
	Previous []protocol.Task
	Active   []protocol.Task
	Result   CollectResult
	Logger   *slog.Logger
}

type Reconciler interface {
	ReconcileDone(ctx context.Context, req ReconcileRequest) []protocol.Task
}
