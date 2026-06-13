package github

import (
	"context"
	"log/slog"

	"radar.nvim/internal/ingestion"
	"radar.nvim/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "github"
}

func (Source) Status(ctx context.Context, logger *slog.Logger) ingestion.StatusResult {
	status, allowed := GraphQLSourceStatus(ctx, logger)
	return ingestion.StatusResult{Status: status, CanRun: allowed}
}

func (Source) Ingest(ctx context.Context, req ingestion.Request) ingestion.Result {
	result := ingestion.Result{
		Tasks: make([]protocol.Task, 0),
	}

	reviewItems, authoredItems, activityItems, err := FetchPullRequests(ctx, req.Previous, req.Logger)
	if err != nil {
		req.Logger.Warn("github pull request collection failed", "error", err)
		return result
	}

	result.Tasks = append(result.Tasks, reviewItems...)
	result.Tasks = append(result.Tasks, authoredItems...)
	result.Tasks = append(result.Tasks, activityItems...)

	trackedItems, err := FetchRulePullRequests(ctx, req.Filters, req.Logger)
	if err != nil {
		req.Logger.Warn("github rule pull request collection failed", "error", err)
	} else {
		result.Tasks = appendMissingPullRequests(result.Tasks, trackedItems)
	}

	result.Complete = true
	return result
}

func (Source) ReconcileDone(ctx context.Context, req ingestion.ReconcileRequest) []protocol.Task {
	return ResolveDonePullRequests(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger)
}

func appendMissingPullRequests(tasks []protocol.Task, candidates []protocol.Task) []protocol.Task {
	seen := map[string]bool{}
	for _, task := range tasks {
		if task.URL != "" {
			seen[task.URL] = true
		}
	}
	for _, task := range candidates {
		if task.URL != "" && seen[task.URL] {
			continue
		}
		tasks = append(tasks, task)
		if task.URL != "" {
			seen[task.URL] = true
		}
	}
	return tasks
}
