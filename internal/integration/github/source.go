package github

import (
	"context"
	"log/slog"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Name() string {
	return "github"
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status, allowed := GraphQLSourceStatus(ctx, logger)
	return integration.StatusResult{Status: status, CanRun: allowed}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := integration.CollectResult{}

	reviewItems, authoredItems, activityItems, err := FetchPullRequests(ctx, req.Previous, req.Logger)
	if err != nil {
		req.Logger.Warn("github pull request collection failed", "error", err)
		return result
	}

	observed := make([]protocol.Task, 0, len(reviewItems)+len(authoredItems)+len(activityItems))
	observed = append(observed, reviewItems...)
	observed = append(observed, authoredItems...)
	observed = append(observed, activityItems...)

	trackedItems, err := FetchRulePullRequests(ctx, req.Filters, req.Logger)
	if err != nil {
		req.Logger.Warn("github rule pull request collection failed", "error", err)
	} else {
		observed = appendMissingPullRequests(observed, trackedItems)
	}

	result.Observations = observationsFromTasks(observed)
	result.Complete = true
	return result
}

func (Source) ReconcileDone(ctx context.Context, req integration.ReconcileRequest) []protocol.Task {
	return ResolveDonePullRequests(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger)
}

func observationsFromTasks(tasks []protocol.Task) []integration.Observation {
	observations := make([]integration.Observation, 0, len(tasks))
	for _, task := range tasks {
		if len(task.SourceRefs) == 0 {
			continue
		}
		ref := task.SourceRefs[0]
		if task.Metadata != nil {
			if ref.Metadata == nil {
				ref.Metadata = map[string]string{}
			}
			for key, value := range task.Metadata {
				ref.Metadata[key] = value
			}
		}
		observations = append(observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(task.Attention), Reason: task.Reason})
	}
	return observations
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

var _ integration.Source = Source{}
var _ integration.StatusReporter = Source{}
var _ integration.Reconciler = Source{}
var _ integration.CodeReviewProvider = Source{}
