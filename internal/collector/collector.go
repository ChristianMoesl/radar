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
	Services   []protocol.ServiceStatus
	Results    map[string]ingestion.Result
}

type Result struct {
	Tasks    []protocol.Task
	Services []protocol.ServiceStatus
}

func Sources() []ingestion.Source {
	return []ingestion.Source{
		github.NewService(),
		jira.NewService(),
		gitcollector.NewService(),
		tmux.NewService(),
	}
}

func Collect(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Result {
	sources := Sources()
	ingested := Ingest(ctx, previous, logger)
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
	return Result{Tasks: tasks, Services: ingested.Services}
}

func Ingest(ctx context.Context, previous []protocol.Task, logger *slog.Logger) Ingested {
	result := Ingested{
		Tasks:      make([]protocol.Task, 0),
		SourceRefs: make([]protocol.SourceRef, 0),
		Services:   make([]protocol.ServiceStatus, 0, 4),
		Results:    map[string]ingestion.Result{},
	}

	filterCfg, err := filters.Load()
	if err != nil {
		logger.Warn("could not load filters for ingestion", "error", err)
	}

	for _, source := range Sources() {
		status := ingestion.StatusResult{
			Status: protocol.ServiceStatus{Name: source.Name(), Status: "ok"},
			CanRun: true,
		}
		if reporter, ok := source.(ingestion.StatusReporter); ok {
			status = reporter.Status(ctx, logger)
		}
		if !status.CanRun {
			result.Services = append(result.Services, status.Status)
			continue
		}

		ingested := source.Ingest(ctx, ingestion.Request{
			Previous: previous,
			Filters:  filterCfg,
			Logger:   logger,
		})
		status.Status.SourceRefCount = sourceRefCount(source.Name(), ingested)
		result.Services = append(result.Services, status.Status)
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
