package ingestion

import (
	"context"
	"log/slog"

	"radar/internal/filters"
	"radar/internal/protocol"
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

type LocalSource interface {
	Local() bool
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

type DeletePreviewRequest struct {
	Task    protocol.Task
	Current protocol.CurrentContext
	Logger  *slog.Logger
}

type Deleter interface {
	PreviewDelete(ctx context.Context, req DeletePreviewRequest) (protocol.DeletePreview, bool, error)
	Delete(ctx context.Context, preview protocol.DeletePreview) (protocol.DeleteResult, error)
}
