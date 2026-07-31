package jira

import (
	"context"
	"log/slog"
	"strings"

	"radar/internal/config"
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
	if _, err := config.Load(); err != nil {
		logger.Debug("jira user configuration is invalid", "error", err)
		status.Status = "error"
		status.Detail = "could not load config"
		return integration.StatusResult{Status: status, CanRun: false}
	}
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
	userConfig, err := config.Load()
	if err != nil {
		req.Logger.Warn("jira user configuration is invalid", "error", err)
		status := protocol.SourceStatus{Name: "jira", Status: "error", Detail: "could not load config"}
		return integration.CollectResult{SourceStatus: &status}
	}
	sourceRefs, status, err := FetchAssignedIssues(ctx, userConfig.Jira.IssueTypes, req.Logger)
	if err != nil {
		req.Logger.Warn("jira issue collection failed", "error", err)
		return integration.CollectResult{SourceStatus: &status}
	}
	observations := make([]integration.Observation, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		signal := integration.WorkSignal(userConfig.Jira.SignalForStatus(ref.Status))
		if strings.EqualFold(ref.Metadata["status_category"], "done") {
			signal = integration.SignalDone
		}
		ref.Signal = string(signal)
		observations = append(observations, integration.Observation{Ref: ref, Signal: signal, Reason: ref.Status})
	}
	return integration.CollectResult{
		Observations: observations,
		Complete:     true,
		SourceStatus: &status,
	}
}

func (Source) ReconcileDone(ctx context.Context, req integration.ReconcileRequest) []protocol.Task {
	return ResolveDoneIssues(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger)
}

var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.Reconciler = Source{}
var _ integration.WorkTracker = Source{}
