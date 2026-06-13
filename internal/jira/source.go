package jira

import (
	"context"
	"log/slog"
	"strings"

	"radar.nvim/internal/ingestion"
	"radar.nvim/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "jira"
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}
	_, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return ingestion.StatusResult{Status: status, CanRun: false}
	}
	return ingestion.StatusResult{Status: status, CanRun: true}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	sourceRefs, _, err := FetchAssignedIssues(ctx, req.Logger)
	if err != nil {
		req.Logger.Warn("jira issue collection failed", "error", err)
		return ingestion.Result{}
	}
	return ingestion.Result{SourceRefs: sourceRefs, Complete: true}
}
