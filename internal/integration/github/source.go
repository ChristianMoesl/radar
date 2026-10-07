package github

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"radar/internal/command"
	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/integration/github/filters"
	githubsettings "radar/internal/integration/github/settings"
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
	cfg, err := config.Load()
	if err != nil {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "github", Status: "error", Detail: err.Error()}}
	}
	if status := integration.OptionalStatus("github", cfg.GitHub.Enabled, ""); !status.CanRun {
		return status
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return integration.OptionalStatus("github", cfg.GitHub.Enabled, "gh not found")
	}
	// Read local authentication only. Do not prompt, contact GitHub, or log tokens.
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token, err := command.CommandContext(authCtx, "gh", "auth", "token").Output()
	if authCtx.Err() != nil {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "github", Status: "error", Detail: "gh authentication check timed out or was cancelled"}}
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return integration.OptionalStatus("github", cfg.GitHub.Enabled, "gh authentication missing; run gh auth login")
		}
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "github", Status: "error", Detail: "gh authentication check failed: " + err.Error()}}
	}
	if len(token) == 0 {
		return integration.StatusResult{Status: protocol.SourceStatus{Name: "github", Status: "error", Detail: "gh authentication check returned no token"}}
	}
	status, allowed := GraphQLSourceStatus(ctx, logger)
	return integration.StatusResult{Status: status, CanRun: allowed}
}

func (Source) Collect(ctx context.Context, req integration.CollectRequest) integration.CollectResult {
	result := integration.CollectResult{}
	githubConfig := githubsettings.Default()
	if cfg, err := config.Load(); err != nil {
		status := protocol.SourceStatus{Name: "github", Status: "error", Detail: err.Error()}
		return integration.CollectResult{SourceStatus: &status}
	} else {
		if status := integration.OptionalStatus("github", cfg.GitHub.Enabled, ""); !status.CanRun {
			return integration.CollectResult{SourceStatus: &status.Status}
		}
		githubConfig = cfg.GitHub
	}

	var reviewItems, authoredItems, activityItems, trackedItems []protocol.Task
	var pullRequestErr, trackedErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		reviewItems, authoredItems, activityItems, pullRequestErr = FetchPullRequests(ctx, req.Previous, githubConfig.ActivityRules, req.Logger)
	}()
	go func() {
		defer wg.Done()
		trackedItems, trackedErr = FetchTrackedPullRequests(ctx, githubConfig.Track, req.Logger)
	}()
	wg.Wait()

	if pullRequestErr != nil {
		req.Logger.Warn("github pull request collection failed", "error", pullRequestErr)
		status := protocol.SourceStatus{Name: "github", Status: "error", Detail: "pull request collection failed"}
		result.SourceStatus = &status
	}

	observed := make([]protocol.Task, 0, len(reviewItems)+len(authoredItems)+len(activityItems)+len(trackedItems))
	observed = append(observed, reviewItems...)
	observed = append(observed, authoredItems...)
	observed = append(observed, activityItems...)

	if trackedErr != nil {
		req.Logger.Warn("github tracked pull request collection failed", "error", trackedErr)
		status := protocol.SourceStatus{Name: "github", Status: "partial", Detail: "tracked pull request collection incomplete: " + trackedErr.Error()}
		if result.SourceStatus == nil {
			result.SourceStatus = &status
		}
	}
	observed = appendMissingPullRequests(observed, trackedItems)

	applyLinkingMarks(observed, req.LinkingMarks)
	result.Observations = observationsFromTasks(observed)
	result.Complete = trackedErr == nil && pullRequestErr == nil
	if pullRequestErr != nil {
		// A tracked open-PR snapshot has no personal review/activity facts.
		// Do not let it erase known attention during a failed personal refresh.
		// Fresh open observations must still win over cached terminal facts.
		previous := map[string]protocol.SourceRef{}
		for _, task := range req.Previous {
			for _, ref := range task.SourceRefs {
				if ref.Source == "github" && ref.Signal != "done" {
					previous[ref.ID] = ref
				}
			}
		}
		for i, observation := range result.Observations {
			if ref, ok := previous[observation.Ref.ID]; ok {
				result.Observations[i] = integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status}
			}
		}
	}
	if !result.Complete {
		seen := map[string]bool{}
		for _, observation := range result.Observations {
			seen[observation.Ref.ID] = true
		}
		for _, task := range req.Previous {
			for _, ref := range task.SourceRefs {
				if ref.Source == "github" && !seen[ref.ID] {
					result.Observations = append(result.Observations, integration.Observation{Ref: ref, Signal: integration.WorkSignal(ref.Signal), Reason: ref.Status})
					seen[ref.ID] = true
				}
			}
		}
	}
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
	return filters.Apply(tasks, cfg.GitHub.PullRequestRules)
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
	seen := map[string]int{}
	for i, task := range tasks {
		if task.URL != "" {
			seen[task.URL] = i
		}
	}
	for _, task := range candidates {
		if i, found := seen[task.URL]; task.URL != "" && found {
			if len(tasks[i].SourceRefs) > 0 && tasks[i].SourceRefs[0].Presentation.Hidden {
				tasks[i] = task
			} else if tasks[i].Kind == "github_pr_activity" && len(tasks[i].SourceRefs) > 0 && tasks[i].SourceRefs[0].Acknowledgement != nil {
				ack := *tasks[i].SourceRefs[0].Acknowledgement
				ack.HideWhenAcknowledged = false
				ack.FallbackSignal, ack.FallbackReason = task.Attention, task.Reason
				tasks[i].SourceRefs[0].Acknowledgement = &ack
			}
			continue
		}
		tasks = append(tasks, task)
		if task.URL != "" {
			seen[task.URL] = len(tasks) - 1
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
