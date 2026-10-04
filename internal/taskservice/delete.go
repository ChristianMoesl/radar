package taskservice

import (
	"context"
	"fmt"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func (s *Service) PreviewDeleteTask(ctx context.Context, taskID int) (protocol.TaskDeletionPreview, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	provider, ref, err := s.deletionTarget(taskID)
	if err != nil {
		return protocol.TaskDeletionPreview{}, err
	}
	preview, err := provider.PreviewDelete(ctx, ref)
	preview.TaskID = taskID
	return preview, err
}

func (s *Service) DeleteTask(ctx context.Context, preview *protocol.TaskDeletionPreview) (protocol.TaskDeletionResult, error) {
	if preview == nil {
		return protocol.TaskDeletionResult{}, fmt.Errorf("confirmed task deletion preview is required")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	provider, ref, err := s.deletionTarget(preview.TaskID)
	if err != nil {
		return protocol.TaskDeletionResult{}, err
	}
	// Numeric IDs are cache-local and can change after a reset or merge. Never
	// delete a different note than the one the user actually confirmed.
	if ref.ID != preview.SourceRefID {
		return protocol.TaskDeletionResult{}, fmt.Errorf("task deletion target changed; preview and confirm deletion again")
	}
	result, err := provider.Delete(ctx, ref, *preview)
	s.authoringRevision++
	if err != nil {
		return result, err
	}
	// A failed author collection may reuse previous observations. Exclude the
	// successfully deleted ref even from that fallback, so it cannot linger or
	// be revived by a background collection that started before the deletion.
	previous := withoutSourceRef(s.store.CollectionTasks(), ref.ID)
	collected := collector.Collect(ctx, previous, s.logger, []integration.Source{provider})
	s.applyLocal(collected)
	return result, nil
}

func (s *Service) deletionTarget(taskID int) (integration.TaskAuthoringProvider, protocol.SourceRef, error) {
	provider, err := s.integrations.TaskAuthoring()
	if err != nil {
		return nil, protocol.SourceRef{}, err
	}
	task, ok := taskByID(s.store.Tasks(), taskID)
	if !ok {
		return nil, protocol.SourceRef{}, fmt.Errorf("task %d not found", taskID)
	}
	var target protocol.SourceRef
	for _, ref := range task.SourceRefs {
		if ref.Source != provider.Descriptor().Name || !ref.Authored {
			continue
		}
		if target.ID != "" {
			return nil, protocol.SourceRef{}, fmt.Errorf("task %d has multiple authored notes; cannot choose a note to delete", taskID)
		}
		target = ref
	}
	if target.ID == "" {
		return nil, protocol.SourceRef{}, fmt.Errorf("task %d has no authored note to delete; source items are not deleted by Radar", taskID)
	}
	return provider, target, nil
}

func withoutSourceRef(tasks []protocol.Task, id string) []protocol.Task {
	filtered := make([]protocol.Task, 0, len(tasks))
	for _, task := range tasks {
		refs := make([]protocol.SourceRef, 0, len(task.SourceRefs))
		for _, ref := range task.SourceRefs {
			if ref.ID != id {
				refs = append(refs, ref)
			}
		}
		if len(refs) > 0 {
			task.SourceRefs = refs
			filtered = append(filtered, task)
		}
	}
	return filtered
}
