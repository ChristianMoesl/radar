package collector

import (
	"context"
	"log/slog"

	"radar/internal/config"
	"radar/internal/integration"
	"radar/internal/linking"
	"radar/internal/protocol"
)

type Collected struct {
	Observations []integration.Observation
	Sources      []protocol.SourceStatus
	SourceNames  []string
	Results      map[string]integration.CollectResult
	LinkingMarks linking.MarkMatcher
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
	active := observedTasks(collected)
	for _, source := range sources {
		reconciler, ok := source.(integration.Reconciler)
		if !ok {
			continue
		}
		name := source.Descriptor().Name
		reconciled := reconciler.Reconcile(ctx, integration.ReconcileRequest{
			Previous:     previous,
			Active:       active,
			Result:       collected.Results[name],
			LinkingMarks: collected.LinkingMarks,
			Logger:       logger,
		})
		for i := range reconciled {
			reconciled[i] = describeObservation(source.Descriptor(), reconciled[i])
		}
		collected.Observations = append(collected.Observations, reconciled...)
	}
	return Result{Tasks: deduplicateReconciledTasks(observedTasks(collected)), Sources: collected.Sources, SourceNames: collected.SourceNames}
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
	filterCfg := cfg.GitHub.Filters
	result.LinkingMarks = linking.NewMarkMatcher(cfg.LinkingMarkPrefixes)

	for _, source := range sources {
		descriptor := source.Descriptor()
		result.SourceNames = append(result.SourceNames, descriptor.Name)
		status := integration.StatusResult{
			Status: protocol.SourceStatus{Name: descriptor.Name, Status: "ok"},
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
			Previous:     previous,
			Filters:      filterCfg,
			LinkingMarks: result.LinkingMarks,
			Logger:       logger,
		})
		if collected.SourceStatus != nil {
			status.Status = *collected.SourceStatus
			if status.Status.Name == "" {
				status.Status.Name = descriptor.Name
			}
		}
		for i := range collected.Observations {
			collected.Observations[i] = describeObservation(descriptor, collected.Observations[i])
		}
		status.Status.SourceRefCount = sourceRefCount(descriptor.Name, collected)
		result.Sources = append(result.Sources, status.Status)
		result.Results[descriptor.Name] = collected
		result.Observations = append(result.Observations, collected.Observations...)
	}

	return result
}

func describeObservation(descriptor integration.Descriptor, observation integration.Observation) integration.Observation {
	observation.Ref.Source = descriptor.Name
	observation.Ref.SourceLabel = descriptor.Label
	observation.Ref.DisplayOrder = descriptor.DisplayOrder
	return observation
}

func observedTasks(collected Collected) []protocol.Task {
	tasks := make([]protocol.Task, 0, len(collected.Observations))
	for _, observation := range collected.Observations {
		tasks = append(tasks, taskFromObservation(observation))
	}
	return tasks
}

func taskFromObservation(observation integration.Observation) protocol.Task {
	sourceRef := observation.Ref
	if sourceRef.Role == protocol.SourceRefRoleInformational {
		return protocol.Task{
			TargetTaskID: observation.TargetTaskID,
			Kind:         taskKindFromObservation(observation),
			SourceRefs:   []protocol.SourceRef{sourceRef},
		}
	}
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
		TargetTaskID: observation.TargetTaskID,
		Busy:         sourceRef.Busy,
		Kind:         taskKindFromObservation(observation),
		Title:        sourceRef.Title,
		Repo:         sourceRef.Repo,
		URL:          sourceRef.URL,
		Attention:    attention,
		Reason:       reason,
		SourceRefs:   []protocol.SourceRef{sourceRef},
		Metadata:     taskMetadataFromObservation(observation),
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
		if sourceRef.Role == protocol.SourceRefRoleAuthoritative && sourceRef.ID != "" {
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
