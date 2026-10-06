package collector

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// ReconcileAuthoredBindings persists new associations after state linking. It
// requires a fresh, complete authoring snapshot but never changes lifecycle or
// completion baselines, so it can also run when remote facts predate a mutation.
// Returned observations replace collected refs only after successful persistence.
func ReconcileAuthoredBindings(ctx context.Context, tracked []protocol.Task, result *Result, sources []integration.Source, logger *slog.Logger) bool {
	binders := map[string]integration.TaskBindingProvider{}
	for _, source := range sources {
		if provider, ok := source.(integration.TaskBindingProvider); ok {
			binders[source.Descriptor().Name] = provider
		}
	}
	observed := map[string]protocol.SourceRef{}
	for _, task := range result.Tasks {
		for _, ref := range task.SourceRefs {
			observed[ref.ID] = ref
		}
	}
	changed := false
	for _, task := range tracked {
		for _, ref := range task.SourceRefs {
			binder := binders[ref.Source]
			fresh, seen := observed[ref.ID]
			if binder == nil || !seen || !result.Complete[ref.Source] || !fresh.Authored || !hasNewBindings(fresh, task) {
				continue
			}
			observation, err := binder.ReconcileBindings(ctx, fresh, task)
			if err != nil {
				logger.Warn("could not reconcile authored task bindings", "id", ref.ID, "error", err)
				result.Complete[ref.Source] = false
				for i := range result.Sources {
					if result.Sources[i].Name == ref.Source {
						result.Sources[i].Status = "error"
						result.Sources[i].Detail += fmt.Sprintf("; task binding reconciliation failed for %s: %v", ref.ID, err)
					}
				}
				continue
			}
			if observation != nil {
				updated := taskFromObservation(describeObservation(binder.Descriptor(), *observation))
				replaceAuthoredObservation(result, ref.ID, updated)
				observed[ref.ID] = updated.SourceRefs[0]
				changed = true
			}
		}
	}
	return changed
}

// ReconcileAuthoredTasks runs after remote reconciliation and state linking, on
// CollectionTasks rather than display tasks so missing work cannot imply done.
// Only full collections supply Complete. Local refreshes cannot mutate lifecycle.
// The returned observations replace their collected refs only after persistence.
func ReconcileAuthoredTasks(ctx context.Context, tracked []protocol.Task, result *Result, sources []integration.Source, logger *slog.Logger) bool {
	changed := ReconcileAuthoredBindings(ctx, tracked, result, sources, logger)
	providers := map[string]integration.TaskLifecycleProvider{}
	for _, source := range sources {
		if provider, ok := source.(integration.TaskLifecycleProvider); ok {
			providers[source.Descriptor().Name] = provider
		}
	}
	observed := map[string]protocol.SourceRef{}
	for _, task := range result.Tasks {
		for _, ref := range task.SourceRefs {
			observed[ref.ID] = ref
		}
	}
	for _, task := range tracked {
		workItems := make([]protocol.SourceRef, 0)
		confirmed := true
		active := false
		for _, ref := range task.SourceRefs {
			if ref.Role != protocol.SourceRefRoleAuthoritative || ref.Lifecycle != protocol.SourceRefLifecycleWorkItem || ref.Authority != protocol.SourceRefAuthorityContributing {
				continue
			}
			// Previously confirmed terminal facts remain valid, as in the remote
			// reconcilers. Missing non-terminal refs and failed sources never do.
			fresh, seen := observed[ref.ID]
			if !result.Complete[ref.Source] || (!seen && ref.Signal != string(integration.SignalDone)) {
				confirmed = false
				continue
			}
			if seen {
				ref = fresh
			}
			if ref.Role != protocol.SourceRefRoleAuthoritative || ref.Lifecycle != protocol.SourceRefLifecycleWorkItem || ref.Authority != protocol.SourceRefAuthorityContributing || ref.Signal == "" {
				confirmed = false
				continue
			}
			active = active || ref.Signal != string(integration.SignalDone)
			workItems = append(workItems, ref)
		}
		// Bound work may have no cached observation at all after reset. Its
		// absence must still prevent completion of other, successfully read work.
		known := map[string]bool{}
		for _, ref := range workItems {
			known[ref.Binding().LinkingKey()] = true
		}
		for _, ref := range task.SourceRefs {
			if fresh, ok := observed[ref.ID]; ok {
				ref = fresh
			}
			if !ref.Authored {
				continue
			}
			for _, binding := range ref.Bindings {
				if binding.WorkItem && !known[binding.LinkingKey()] {
					confirmed = false
				}
			}
		}
		// One confirmed active item is sufficient to reopen. Completion needs
		// every contributor, including missing previously-active work.
		if (!confirmed && !active) || len(workItems) == 0 {
			continue
		}
		for _, ref := range task.SourceRefs {
			provider := providers[ref.Source]
			if provider == nil || !result.Complete[ref.Source] || ref.Role != protocol.SourceRefRoleAuthoritative || ref.Lifecycle != protocol.SourceRefLifecycleWorkItem || ref.Authority != protocol.SourceRefAuthorityPrimary {
				continue
			}
			fresh, seen := observed[ref.ID]
			if !seen {
				continue
			}
			observation, err := provider.ReconcileLifecycle(ctx, fresh, workItems)
			if err != nil {
				logger.Warn("could not reconcile authored task lifecycle", "id", ref.ID, "error", err)
				for i := range result.Sources {
					if result.Sources[i].Name == ref.Source {
						result.Sources[i].Status = "error"
						result.Sources[i].Detail += fmt.Sprintf("; task lifecycle reconciliation failed for %s: %v", ref.ID, err)
					}
				}
				continue
			}
			if observation == nil {
				continue
			}
			updated := taskFromObservation(describeObservation(provider.Descriptor(), *observation))
			replaceAuthoredObservation(result, ref.ID, updated)
			changed = true
		}
	}
	return changed
}

func replaceAuthoredObservation(result *Result, id string, updated protocol.Task) {
	for i := range result.Tasks {
		for j := range result.Tasks[i].SourceRefs {
			if result.Tasks[i].SourceRefs[j].ID == id {
				result.Tasks[i].SourceRefs[j] = updated.SourceRefs[0]
				if len(result.Tasks[i].SourceRefs) == 1 {
					result.Tasks[i].Attention = updated.Attention
					result.Tasks[i].Reason = updated.Reason
					result.Tasks[i].Muted = updated.Muted
				}
			}
		}
	}
}
