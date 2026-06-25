package collector

import (
	"context"
	"log/slog"

	"radar/internal/config"
	"radar/internal/filters"
	"radar/internal/integration"
	"radar/internal/protocol"
)

type Ingested struct {
	Tasks       []protocol.Task
	SourceRefs  []protocol.SourceRef
	Sources     []protocol.SourceStatus
	SourceNames []string
	Results     map[string]integration.CollectResult
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
	ingested := IngestSources(ctx, previous, logger, sources)
	tasks := observedTasks(ingested)
	for _, source := range sources {
		reconciler, ok := source.(integration.Reconciler)
		if !ok {
			continue
		}
		tasks = append(tasks, reconciler.ReconcileDone(ctx, integration.ReconcileRequest{
			Previous: previous,
			Active:   tasks,
			Result:   ingested.Results[source.Name()],
			Logger:   logger,
		})...)
	}
	return Result{Tasks: applyTaskFilters(deduplicateReconciledTasks(tasks), logger), Sources: ingested.Sources, SourceNames: ingested.SourceNames}
}

func CollectLocal(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []integration.Source) Result {
	ingested := IngestSources(ctx, previous, logger, LocalSources(sources))
	return Result{Tasks: applyTaskFilters(observedTasks(ingested), logger), Sources: ingested.Sources, SourceNames: ingested.SourceNames}
}

func IngestSources(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []integration.Source) Ingested {
	result := Ingested{
		Tasks:       make([]protocol.Task, 0),
		SourceRefs:  make([]protocol.SourceRef, 0),
		Sources:     make([]protocol.SourceStatus, 0, 4),
		SourceNames: make([]string, 0, len(sources)),
		Results:     map[string]integration.CollectResult{},
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load config for ingestion", "error", err)
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

		ingested := source.Collect(ctx, integration.CollectRequest{
			Previous: previous,
			Filters:  filterCfg,
			Logger:   logger,
		})
		status.Status.SourceRefCount = sourceRefCount(source.Name(), ingested)
		result.Sources = append(result.Sources, status.Status)
		result.Results[source.Name()] = ingested
		result.Tasks = append(result.Tasks, ingested.Tasks...)
		result.SourceRefs = append(result.SourceRefs, ingested.SourceRefs...)
	}

	return result
}

func observedTasks(ingested Ingested) []protocol.Task {
	tasks := make([]protocol.Task, 0, len(ingested.Tasks)+len(ingested.SourceRefs))
	tasks = append(tasks, ingested.Tasks...)
	for _, sourceRef := range ingested.SourceRefs {
		if ignoredStandaloneSourceRef(sourceRef) {
			continue
		}
		tasks = append(tasks, taskFromSourceRef(sourceRef))
	}
	return tasks
}

func taskFromSourceRef(sourceRef protocol.SourceRef) protocol.Task {
	reason := sourceRef.Source + " " + sourceRef.Kind
	return protocol.Task{
		Kind:       sourceRef.Source + "_" + sourceRef.Kind,
		Title:      sourceRef.Title,
		Repo:       sourceRef.Repo,
		URL:        sourceRef.URL,
		Attention:  "in_progress",
		Reason:     reason,
		SourceRefs: []protocol.SourceRef{sourceRef},
	}
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
	for _, sourceRef := range result.SourceRefs {
		if sourceRef.Source == sourceName {
			seen[sourceRef.ID] = true
		}
	}
	for _, task := range result.Tasks {
		for _, sourceRef := range task.SourceRefs {
			if sourceRef.Source == sourceName {
				seen[sourceRef.ID] = true
			}
		}
	}
	return len(seen)
}

func applyTaskFilters(tasks []protocol.Task, logger *slog.Logger) []protocol.Task {
	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load config for task filtering", "error", err)
		return tasks
	}
	return filters.Apply(tasks, cfg.Filters)
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
