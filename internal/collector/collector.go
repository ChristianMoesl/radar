package collector

import (
	"context"
	"log/slog"

	"radar.nvim/internal/config"
	"radar.nvim/internal/filters"
	gitcollector "radar.nvim/internal/git"
	"radar.nvim/internal/github"
	"radar.nvim/internal/ingestion"
	"radar.nvim/internal/jira"
	"radar.nvim/internal/linker"
	"radar.nvim/internal/protocol"
	"radar.nvim/internal/tmux"
)

type Ingested struct {
	Tasks      []protocol.Task
	SourceRefs []protocol.SourceRef
	Sources    []protocol.SourceStatus
	Results    map[string]ingestion.Result
}

type Result struct {
	Tasks   []protocol.Task
	Sources []protocol.SourceStatus
}

func Sources() []ingestion.Source {
	return []ingestion.Source{
		github.NewSource(),
		jira.NewSource(),
		gitcollector.NewSource(),
		tmux.NewSource(),
	}
}

func LocalSources() []ingestion.Source {
	return []ingestion.Source{
		gitcollector.NewSource(),
		tmux.NewSource(),
	}
}

func Collect(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Result {
	sources := Sources()
	ingested := IngestSources(ctx, previous, logger, sources)
	tasks := linker.Link(linker.Input{
		Tasks:      ingested.Tasks,
		SourceRefs: ingested.SourceRefs,
	})
	for _, source := range sources {
		reconciler, ok := source.(ingestion.Reconciler)
		if !ok {
			continue
		}
		tasks = append(tasks, reconciler.ReconcileDone(ctx, ingestion.ReconcileRequest{
			Previous: previous,
			Active:   tasks,
			Result:   ingested.Results[source.Name()],
			Logger:   logger,
		})...)
	}
	return Result{Tasks: applyTaskFilters(deduplicateReconciledTasks(tasks), logger), Sources: ingested.Sources}
}

func CollectLocal(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Result {
	ingested := IngestSources(ctx, previous, logger, LocalSources())
	tasks := linker.Link(linker.Input{SourceRefs: ingested.SourceRefs})
	return Result{Tasks: applyTaskFilters(tasks, logger), Sources: ingested.Sources}
}

func Ingest(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Ingested {
	return IngestSources(ctx, previous, logger, Sources())
}

func IngestSources(ctx context.Context, previous []protocol.Task, logger *slog.Logger, sources []ingestion.Source) Ingested {
	result := Ingested{
		Tasks:      make([]protocol.Task, 0),
		SourceRefs: make([]protocol.SourceRef, 0),
		Sources:    make([]protocol.SourceStatus, 0, 4),
		Results:    map[string]ingestion.Result{},
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Warn("could not load config for ingestion", "error", err)
	}
	filterCfg := cfg.Filters

	for _, source := range sources {
		status := ingestion.StatusResult{
			Status: protocol.SourceStatus{Name: source.Name(), Status: "ok"},
			CanRun: true,
		}
		if reporter, ok := source.(ingestion.StatusReporter); ok {
			status = reporter.Status(ctx, logger)
		}
		if !status.CanRun {
			result.Sources = append(result.Sources, status.Status)
			continue
		}

		ingested := source.Ingest(ctx, ingestion.Request{
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

func sourceRefCount(sourceName string, result ingestion.Result) int {
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
