package collector

import (
	"context"
	"log/slog"

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
	return Result{Tasks: tasks, Sources: ingested.Sources}
}

func CollectLocal(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Result {
	ingested := IngestSources(ctx, previous, logger, LocalSources())
	baseTasks := withoutSourceRefs(previous, map[string]bool{"git": true, "tmux": true})
	tasks := linker.Link(linker.Input{
		Tasks:      baseTasks,
		SourceRefs: ingested.SourceRefs,
	})
	return Result{Tasks: tasks, Sources: ingested.Sources}
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

	filterCfg, err := filters.Load()
	if err != nil {
		logger.Warn("could not load filters for ingestion", "error", err)
	}

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

func withoutSourceRefs(tasks []protocol.Task, sources map[string]bool) []protocol.Task {
	kept := make([]protocol.Task, 0, len(tasks))
	for _, task := range tasks {
		refs := make([]protocol.SourceRef, 0, len(task.SourceRefs))
		for _, sourceRef := range task.SourceRefs {
			if !sources[sourceRef.Source] {
				refs = append(refs, sourceRef)
			}
		}
		if len(task.SourceRefs) > 0 && len(refs) == 0 {
			continue
		}
		task.SourceRefs = refs
		kept = append(kept, task)
	}
	return kept
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
