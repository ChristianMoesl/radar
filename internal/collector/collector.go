package collector

import (
	"context"
	"log/slog"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type Collected struct {
	Observations []integration.Observation
	Sources      []protocol.SourceStatus
	SourceNames  []string
	Results      map[string]integration.CollectResult
}

type Result struct {
	Tasks       []protocol.Task
	Sources     []protocol.SourceStatus
	SourceNames []string
}

func LocalSources(sources []integration.Source) []integration.Source {
	locals := make([]integration.Source, 0)
	for _, source := range sources {
		local, ok := source.(integration.LocalSource)
		if ok && local.Local() {
			locals = append(locals, source)
		}
	}
	return locals
}

func Collect(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []integration.Source) Result {
	collected := CollectSources(ctx, previous, logger, sources)
	tasks := observedTasks(collected)
	for _, source := range sources {
		reconciler, ok := source.(integration.Reconciler)
		if !ok {
			continue
		}
		tasks = append(tasks, reconciler.ReconcileDone(ctx, integration.ReconcileRequest{
			Previous: previous,
			Active:   tasks,
			Result:   collected.Results[source.Name()],
			Logger:   logger,
		})...)
	}
	return Result{Tasks: deduplicateReconciledTasks(tasks), Sources: collected.Sources, SourceNames: collected.SourceNames}
}

func CollectLocal(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []integration.Source) Result {
	collected := CollectSources(ctx, previous, logger, LocalSources(sources))
	return Result{Tasks: observedTasks(collected), Sources: collected.Sources, SourceNames: collected.SourceNames}
}

func CollectSources(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []integration.Source) Collected {
	result := Collected{
		Observations: make([]integration.Observation, 0),
		Sources:      make([]protocol.SourceStatus, 0, 4),
		SourceNames:  make([]string, 0, len(sources)),
		Results:      map[string]integration.CollectResult{},
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load config for collection", "error", err)
	}
	filterCfg := cfg.Filters

	for _, source := range sources {
		result.SourceNames = append(result.SourceNames, source.Name())
		status := integration.StatusResult{
			Status: protocol.SourceStatus{Name: source.Name(), Status: "ok"},
			CanRun: true,
		}
		if reporter, ok := source.(integration.StatusReporter); ok {
			status = reporter.Status(ctx, logger)
		}
		if !status.CanRun {
			result.Sources = append(result.Sources, status.Status)
			continue
		}

		collected := source.Collect(ctx, integration.CollectRequest{
			Previous: previous,
			Filters:  filterCfg,
			Logger:   logger,
		})
		status.Status.SourceRefCount = sourceRefCount(source.Name(), collected)
		result.Sources = append(result.Sources, status.Status)
		result.Results[source.Name()] = collected
		result.Observations = append(result.Observations, collected.Observations...)
	}

	return result
}

func observedTasks(collected Collected) []protocol.Task {
	tasks := make([]protocol.Task, 0, len(collected.Observations))
	for _, observation := range collected.Observations {
		if ignoredStandaloneSourceRef(observation.Ref) {
			continue
		}
		tasks = append(tasks, taskFromObservation(observation))
	}
	return tasks
}

func taskFromObservation(observation integration.Observation) protocol.Task {
	sourceRef := observation.Ref
	reason := observation.Reason
	if reason == "" {
		reason = sourceRef.Source + " " + sourceRef.Kind
	}
	attention := string(observation.Signal)
	if attention == "" {
		attention = string(integration.SignalInProgress)
	}
	sourceRef.Signal = attention
	return protocol.Task{
		Kind:       taskKindFromObservation(observation),
		Title:      sourceRef.Title,
		Repo:       sourceRef.Repo,
		URL:        sourceRef.URL,
		Attention:  attention,
		Reason:     reason,
		SourceRefs: []protocol.SourceRef{sourceRef},
		Metadata:   taskMetadataFromObservation(observation),
	}
}

func taskKindFromObservation(observation integration.Observation) string {
	ref := observation.Ref
	if ref.Source == "github" && ref.Kind == "pull_request" {
		if observation.Signal == integration.SignalAttention && observation.Reason == "review requested" {
			return "github_review_request"
		}
		if observation.Signal == integration.SignalAttention {
			return "github_pr_activity"
		}
		return "github_own_pr"
	}
	return ref.Source + "_" + ref.Kind
}

func taskMetadataFromObservation(observation integration.Observation) map[string]string {
	if observation.Ref.Source != "github" || observation.Ref.Metadata == nil {
		return nil
	}
	if author := observation.Ref.Metadata["author"]; author != "" {
		return map[string]string{"author": author}
	}
	return nil
}

func ignoredStandaloneSourceRef(sourceRef protocol.SourceRef) bool {
	if sourceRef.Source != "git" || sourceRef.Kind != "worktree" {
		return false
	}
	switch sourceRef.Branch {
	case "", "main", "master", "develop", "dev":
		return true
	default:
		return false
	}
}

func sourceRefCount(sourceName string, result integration.CollectResult) int {
	seen := map[string]bool{}
	for _, observation := range result.Observations {
		if observation.Ref.Source == sourceName {
			seen[observation.Ref.ID] = true
		}
	}
	return len(seen)
}

func deduplicateReconciledTasks(tasks []protocol.Task) []protocol.Task {
	kept := make([]protocol.Task, 0, len(tasks))
	byIdentity := map[string]int{}
	for _, task := range tasks {
		identity := reconciliationIdentity(task)
		if identity != "" {
			if existing, ok := byIdentity[identity]; ok {
				kept[existing].SourceRefs = mergeSourceRefs(kept[existing].SourceRefs, task.SourceRefs)
				continue
			}
			byIdentity[identity] = len(kept)
		}
		kept = append(kept, task)
	}
	return kept
}

func reconciliationIdentity(task protocol.Task) string {
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "github" && sourceRef.Kind == "pull_request" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	for _, sourceRef := range task.SourceRefs {
		if sourceRef.Source == "jira" && sourceRef.Kind == "issue" && sourceRef.ID != "" {
			return sourceRef.ID
		}
	}
	if task.URL != "" {
		return "url:" + task.URL
	}
	return ""
}

func mergeSourceRefs(left []protocol.SourceRef, right []protocol.SourceRef) []protocol.SourceRef {
	seen := map[string]bool{}
	for _, sourceRef := range left {
		if sourceRef.ID != "" {
			seen[sourceRef.ID] = true
		}
	}
	for _, sourceRef := range right {
		if sourceRef.ID != "" && seen[sourceRef.ID] {
			continue
		}
		left = append(left, sourceRef)
		if sourceRef.ID != "" {
			seen[sourceRef.ID] = true
		}
	}
	return left
}
