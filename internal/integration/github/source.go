package github

import (
	"context"
	"log/slog"
	"sync"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/github/filters"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type Source struct{}

func NewSource() Source {
	return Source{}
}

func (Source) Descriptor() integration.Descriptor {
	return integration.Descriptor{Name: "github", Label: "GitHub", DisplayOrder: 1}
}

func (Source) Status(ctx context.Context, logger *slog.Logger) integration.StatusResult {
	status, allowed := GraphQLSourceStatus(ctx, logger)
	return integration.StatusResult{Status: status, CanRun: allowed}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := integration.CollectResult{}
	filterConfig := filters.Config{}
	if cfg, err := config.Load(); err != nil {
		req.Logger.Warn("could not load github filters", "error", err)
	} else {
		filterConfig = cfg.GitHub.Filters
	}

	var reviewItems, authoredItems, activityItems, trackedItems []protocol.Task
	var pullRequestErr, trackedErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		reviewItems, authoredItems, activityItems, pullRequestErr = FetchPullRequests(ctx, req.Previous, filterConfig, req.Logger)
	}()
	go func() {
		defer wg.Done()
		trackedItems, trackedErr = FetchRulePullRequests(ctx, filterConfig, req.Logger)
	}()
	wg.Wait()

	if pullRequestErr != nil {
		req.Logger.Warn("github pull request collection failed", "error", pullRequestErr)
		return result
	}

	observed := make([]protocol.Task, 0, len(reviewItems)+len(authoredItems)+len(activityItems)+len(trackedItems))
	observed = append(observed, reviewItems...)
	observed = append(observed, authoredItems...)
	observed = append(observed, activityItems...)

	if trackedErr != nil {
		req.Logger.Warn("github rule pull request collection failed", "error", trackedErr)
	} else {
		observed = appendMissingPullRequests(observed, trackedItems)
	}

	applyLinkingMarks(observed, req.LinkingMarks)
	result.Observations = observationsFromTasks(observed)
	result.Complete = trackedErr == nil
	return result
}

func (Source) RateLimitSummary(ctx context.Context, logger *slog.Logger) (string, error) {
	return RateLimitSummary(ctx, logger)
}

func (Source) FilterTasks(tasks []protocol.Task, logger *slog.Logger) []protocol.Task {
	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load github filters", "error", err)
		return tasks
	}
	return filters.Apply(tasks, cfg.GitHub.Filters)
}

func (Source) Reconcile(ctx context.Context, req integration.ReconcileRequest) []integration.Observation {
	tasks := ResolveDonePullRequests(ctx, req.Previous, req.Active, req.Result.Complete, req.Logger)
	applyLinkingMarks(tasks, req.LinkingMarks)
	return observationsFromTasks(tasks)
}

func applyLinkingMarks(tasks []protocol.Task, marks linking.MarkMatcher) {
	for i := range tasks {
		for j := range tasks[i].SourceRefs {
			ref := &tasks[i].SourceRefs[j]
			ref.LinkingKeys = linking.Keys(append(marks.Keys(ref.Title, ref.Branch, ref.Repo, ref.URL, ref.Path), ref.LinkingKeys...)...)
		}
	}
}

func observationsFromTasks(tasks []protocol.Task) []integration.Observation {
	observations := make([]integration.Observation, 0, len(tasks))
	for _, task := range tasks {
		if len(task.SourceRefs) == 0 {
			continue
		}
		ref := task.SourceRefs[0]
		ref.Signal = task.Attention
		if task.Metadata != nil {
			if ref.Metadata == nil {
				ref.Metadata = map[string]string{}
			}
			for key, value := range task.Metadata {
				ref.Metadata[key] = value
			}
		}
		observations = append(observations, integration.Observation{
			Ref: ref, Signal: integration.WorkSignal(task.Attention), Reason: task.Reason,
			TaskKind: task.Kind, TaskMetadata: cloneMetadata(task.Metadata),
		})
	}
	return observations
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
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
var _ integration.TaskFilterProvider = Source{}
var _ integration.RateLimitReporter = Source{}
var _ integration.CodeReviewProvider = Source{}
