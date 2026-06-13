package ingestion

import (
	"context"
	"log/slog"

	"radar.nvim/internal/filters"
	"radar.nvim/internal/protocol"
)

type Request struct {
	Previous []protocol.Task
	Filters  filters.Config
	Logger   *slog.Logger
}

type Result struct {
	Tasks      []protocol.Task
	SourceRefs []protocol.SourceRef
	Complete   bool
}

type StatusResult struct {
	Status protocol.SourceStatus
	CanRun bool
}

type Source interface {
	Name() string
	Ingest(ctx context.Context, req Request) Result
}

type StatusReporter interface {
	Status(ctx context.Context, logger *slog.Logger) StatusResult
}

type ReconcileRequest struct {
	Previous []protocol.Task
	Active   []protocol.Task
	Result   Result
	Logger   *slog.Logger
}

type Reconciler interface {
	ReconcileDone(ctx context.Context, req ReconcileRequest) []protocol.Task
}
