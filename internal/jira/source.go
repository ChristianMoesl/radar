package jira

import (
	"context"
	"log/slog"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "jira"
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status := protocol.SourceStatus{Name: "jira", Status: "ok"}
	_, ok, missing := configFromEnv()
	if !ok {
		logger.Debug("jira collector not configured", "missing", missing)
		status.Status = "disabled"
		status.Detail = "missing " + strings.Join(missing, ", ")
		return integration.StatusResult{Status: status, CanRun: false}
	}
	return integration.StatusResult{Status: status, CanRun: true}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	sourceRefs, _, err := FetchAssignedIssues(ctx, req.Logger)
	if err != nil {
		req.Logger.Warn("jira issue collection failed", "error", err)
		return integration.CollectResult{}
	}
	return integration.CollectResult{Observations: integration.ObserveRefs(sourceRefs, integration.SignalInProgress), Complete: true}
}

func (Source) ReconcileDone(ctx context.Context, req integration.ReconcileRequest) []protocol.Task {
	return ResolveDoneIssues(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger)
}

var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.Reconciler = Source{}
var _ integration.WorkTracker = Source{}
