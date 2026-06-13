package tmux

import (
	"context"
	"log/slog"

	"radar.nvim/internal/ingestion"
)

type Service struct{}

func NewService() Service {
	return Service{}
}

func (Service) Name() string {
	return "tmux"
}

func (Service) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := ServiceStatus(ctx)
	return ingestion.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Service) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, status := FetchPanes(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("tmux pane collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: sourceRefs}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: status.Status == "ok"}
}
