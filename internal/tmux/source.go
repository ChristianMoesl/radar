package tmux

import (
	"context"
	"log/slog"

	"radar.nvim/internal/ingestion"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "tmux"
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := SourceStatus(ctx)
	return ingestion.StatusResult{Status: status, CanRun: status.Status == "ok"}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, status := FetchSessions(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("tmux session collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: sourceRefs}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: status.Status == "ok"}
}
