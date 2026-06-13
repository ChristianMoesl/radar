package jira

import (
	"context"
	"log/slog"
	"strings"

	"radar.nvim/internal/ingestion"
	"radar.nvim/internal/protocol"
)

type Service struct{}

func NewService() Service {
	return Service{}
}

func (Service) Name() string {
	return "jira"
}

func (Service) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := protocol.ServiceStatus{Name: "jira", Status: "ok"}
	_, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return ingestion.StatusResult{Status: status, CanRun: false}
	}
	return ingestion.StatusResult{Status: status, CanRun: true}
}

func (Service) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	source_refs, _, err := FetchAssignedIssues(ctx, req.Logger)
	if err != nil {
		req.Logger.Warn("jira issue collection failed", "error", err)
		return ingestion.Result{}
	}
	return ingestion.Result{SourceRefs: source_refs, Complete: true}
}
