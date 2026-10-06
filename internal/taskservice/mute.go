package taskservice

import (
	"context"
	"fmt"

	"radar/internal/collector"
	"radar/internal/integration"
	"radar/internal/protocol"
)

func (s *Service) Mute(ctx context.Context, taskID int) (protocol.Task, error) {
	return s.SetMuted(ctx, taskID, true)
}

func (s *Service) Unmute(ctx context.Context, taskID int) (protocol.Task, error) {
	return s.SetMuted(ctx, taskID, false)
}

// SetMuted persists a whole-task preference, then publishes only the updated
// authoring source. Unrelated source observations remain cached; muting a task
// must neither wait for remote collection nor change its underlying lifecycle.
func (s *Service) SetMuted(ctx context.Context, taskID int, muted bool) (protocol.Task, error) {
	author, err := s.integrations.TaskAuthoring()
	if err != nil {
		return protocol.Task{}, err
	}
	provider, ok := author.(integration.TaskMuteProvider)
	if !ok {
		return protocol.Task{}, fmt.Errorf("%s task authoring integration does not support mute/unmute", author.Descriptor().Label)
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// The served projection can hide inactive contributing work. Pass retained
	// identities too, so the authored note can bind the entire aggregate and a
	// missing contributor cannot detach after a cache reset.
	task, ok := taskByID(s.store.CollectionTasks(), taskID)
	if !ok {
		return protocol.Task{}, fmt.Errorf("task %d not found", taskID)
	}
	identity, err := provider.SetMuted(ctx, task, muted)
	// An error can follow a successful note write. Fence in-flight refreshes on
	// both success and failure, just as lifecycle and deletion mutations do.
	s.authoringRevision++
	if err != nil {
		return protocol.Task{}, err
	}
	if muted && identity.SourceRefID == "" {
		return protocol.Task{}, fmt.Errorf("task %d mute mutation did not return an authored identity", taskID)
	}
	result := collector.Collect(ctx, s.store.CollectionTasks(), s.logger, []integration.Source{author})
	s.applyLocal(result)
	if !result.Complete[author.Descriptor().Name] {
		detail := "collection was incomplete"
		for _, status := range result.Sources {
			if status.Name == author.Descriptor().Name && status.Detail != "" {
				detail = status.Detail
				break
			}
		}
		return protocol.Task{}, fmt.Errorf("task mute preference could not be collected from %s: %s", author.Descriptor().Label, detail)
	}
	for _, current := range s.store.Tasks() {
		matches := identity.SourceRefID == "" && current.ID == taskID
		for _, ref := range current.SourceRefs {
			matches = matches || (identity.SourceRefID != "" && ref.ID == identity.SourceRefID)
		}
		if matches {
			if current.Muted != muted {
				return protocol.Task{}, fmt.Errorf("task %d mute preference was not collected after mutation", taskID)
			}
			return current, nil
		}
	}
	return protocol.Task{}, fmt.Errorf("task %d was not collected after mute preference mutation", taskID)
}
