package collector

import (
	"context"
	"fmt"
	"log/slog"

	"radar/internal/integration"
	"radar/internal/protocol"
)

// ReconcileAuthoredTasks runs after remote reconciliation and state linking, on
// CollectionTasks rather than display tasks so missing work cannot imply done.
// Only full collections supply Complete. Local refreshes cannot mutate lifecycle.
// The returned observations replace their collected refs only after persistence.
func ReconcileAuthoredTasks(ctx context.Context, tracked []protocol.Task, result *Result, sources []integration.Source, logger *slog.Logger) bool {
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
	changed := false
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
			for i := range result.Tasks {
				for j := range result.Tasks[i].SourceRefs {
					if result.Tasks[i].SourceRefs[j].ID == ref.ID {
						result.Tasks[i].SourceRefs[j] = updated.SourceRefs[0]
						if len(result.Tasks[i].SourceRefs) == 1 {
							result.Tasks[i].Attention = updated.Attention
							result.Tasks[i].Reason = updated.Reason
						}
					}
				}
			}
			changed = true
		}
	}
	return changed
}
