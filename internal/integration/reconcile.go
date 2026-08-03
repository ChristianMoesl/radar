package integration

import (
	"context"
	"log/slog"

	"radar/internal/linking"
	"radar/internal/protocol"
)

type ReconcileRequest struct {
	Previous     []protocol.Task
	Active       []protocol.Task
	Result       CollectResult
	LinkingMarks linking.MarkMatcher
	Logger       *slog.Logger
}

type Reconciler interface {
	Integration
	Reconcile(ctx context.Context, req ReconcileRequest) []Observation
}
