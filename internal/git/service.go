package git

import (
	"context"
	"log/slog"

	"radar.nvim/internal/ingestion"
	"radar.nvim/internal/protocol"
)

type Service struct{}

func NewService() Service {
	return Service{}
}

func (Service) Name() string {
	return "git"
}

func (Service) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	return ingestion.StatusResult{
		Status: protocol.ServiceStatus{Name: "git", Status: "ok"},
		CanRun: true,
	}
}

func (Service) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	source_refs, status := FetchWorktrees(ctx, req.Logger)
	if status.Status == "error" {
		req.Logger.Warn("git worktree collection failed", "detail", status.Detail)
		return ingestion.Result{SourceRefs: source_refs}
	}
	return ingestion.Result{SourceRefs: source_refs, Complete: status.Status == "ok"}
}
