package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"radar/internal/config"
	"radar/internal/filters"
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
		started := time.Now()
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
		logger.Debug("source reconciliation finished", "source", name, "duration", time.Since(started), "observations", len(reconciled))
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
		Sources:      make([]protocol.SourceStatus, 0, len(sources)),
		SourceNames:  make([]string, 0, len(sources)),
		Results:      map[string]integration.CollectResult{},
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load config for collection", "error", err)
	}
	result.LinkingMarks = linking.NewMarkMatcher(cfg.LinkingMarkPrefixes)

	collections := make([]sourceCollection, len(sources))
	var wg sync.WaitGroup
	wg.Add(len(sources))
	for i, source := range sources {
		descriptor := source.Descriptor()
		collections[i].descriptor = descriptor
		go func(i int, source integration.Source, descriptor integration.Descriptor) {
			defer wg.Done()
			collections[i] = collectSource(ctx, source, descriptor, cloneTasks(previous), cloneFilterConfig(cfg.GitHub.Filters), result.LinkingMarks, logger)
		}(i, source, descriptor)
	}
	wg.Wait()

	// Aggregate in registration order so task projection, source statuses, and
	// presentation remain deterministic regardless of completion order.
	for _, collection := range collections {
		result.SourceNames = append(result.SourceNames, collection.descriptor.Name)
		result.Sources = append(result.Sources, collection.status.Status)
		if !collection.status.CanRun {
			continue
		}
		result.Results[collection.descriptor.Name] = collection.result
		result.Observations = append(result.Observations, collection.result.Observations...)
	}

	return result
}

type sourceCollection struct {
	descriptor integration.Descriptor
	status     integration.StatusResult
	result     integration.CollectResult
}

func collectSource(ctx context.Context, source integration.Source, descriptor integration.Descriptor, previous []protocol.Task, filterCfg filters.Config, marks linking.MarkMatcher, logger *slog.Logger) sourceCollection {
	started := time.Now()
	collection := sourceCollection{
		descriptor: descriptor,
		status: integration.StatusResult{
			Status: protocol.SourceStatus{Name: descriptor.Name, Status: "ok"},
			CanRun: true,
		},
	}
	statusStarted := time.Now()
	if reporter, ok := source.(integration.StatusReporter); ok {
		collection.status = reporter.Status(ctx, logger)
	}
	statusDuration := time.Since(statusStarted)
	if !collection.status.CanRun {
		logger.Debug("source collection skipped", "source", descriptor.Name, "duration", time.Since(started), "status_duration", statusDuration, "status", collection.status.Status.Status)
		return collection
	}

	collectStarted := time.Now()
	collection.result = source.Collect(ctx, integration.CollectRequest{
		Previous:     previous,
		Filters:      filterCfg,
		LinkingMarks: marks,
		Logger:       logger,
	})
	if collection.result.SourceStatus != nil {
		collection.status.Status = *collection.result.SourceStatus
		if collection.status.Status.Name == "" {
			collection.status.Status.Name = descriptor.Name
		}
	}
	for i := range collection.result.Observations {
		collection.result.Observations[i] = describeObservation(descriptor, collection.result.Observations[i])
	}
	collection.status.Status.SourceRefCount = sourceRefCount(descriptor.Name, collection.result)
	logger.Debug("source collection finished", "source", descriptor.Name, "duration", time.Since(started), "status_duration", statusDuration, "collect_duration", time.Since(collectStarted), "observations", len(collection.result.Observations), "complete", collection.result.Complete)
	return collection
}

func cloneTasks(tasks []protocol.Task) []protocol.Task {
	if tasks == nil {
		return nil
	}
	cloned := make([]protocol.Task, len(tasks))
	for i, task := range tasks {
		cloned[i] = task
		cloned[i].Metadata = cloneStringMap(task.Metadata)
		cloned[i].SourceRefs = make([]protocol.SourceRef, len(task.SourceRefs))
		for j, ref := range task.SourceRefs {
			cloned[i].SourceRefs[j] = ref
			cloned[i].SourceRefs[j].LinkingKeys = append([]string(nil), ref.LinkingKeys...)
			cloned[i].SourceRefs[j].Metadata = cloneStringMap(ref.Metadata)
			if ref.Presentation.TitleOrder != nil {
				titleOrder := *ref.Presentation.TitleOrder
				cloned[i].SourceRefs[j].Presentation.TitleOrder = &titleOrder
			}
		}
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneFilterConfig(cfg filters.Config) filters.Config {
	cloned := cfg
	cloned.MuteRepos = append([]string(nil), cfg.MuteRepos...)
	cloned.DeprioritizeRepos = append([]string(nil), cfg.DeprioritizeRepos...)
	cloned.MuteUsers = append([]string(nil), cfg.MuteUsers...)
	cloned.DeprioritizeUsers = append([]string(nil), cfg.DeprioritizeUsers...)
	cloned.Rules = make([]filters.Rule, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		cloned.Rules[i] = rule
		cloned.Rules[i].Repos = append([]string(nil), rule.Repos...)
		cloned.Rules[i].Users = append([]string(nil), rule.Users...)
	}
	return cloned
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
