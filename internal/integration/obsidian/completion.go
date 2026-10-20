package obsidian

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"radar/internal/integration"
	"radar/internal/protocol"
)

var validCompletionBaseline = regexp.MustCompile(`^[0-9a-f]{64}$`)

// The baseline records the set of already-completed work on explicit reopening.
// It survives restarts and cache resets. Observing active work arms completion
// again, even when the same issue or PR is reopened and subsequently completed.
func (s Source) ReconcileLifecycle(_ context.Context, ref protocol.SourceRef, request integration.TaskLifecycleRequest) (*integration.Observation, error) {
	workItems, activity := request.WorkItems, request.Activity
	reviewRequired := request.RequireAgentReview || (ref.Signal == "done" && activity != protocol.ActivityIdle)
	if len(workItems) == 0 && !reviewRequired {
		return nil, nil
	}
	ids := make([]string, 0, len(workItems))
	allDone := true
	for _, item := range workItems {
		if item.Signal == string(integration.SignalDone) {
			ids = append(ids, item.ID)
		} else {
			allDone = false
		}
	}
	sort.Strings(ids)
	baseline := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(ids, "\n"))))
	changed := false
	current, err := s.mutateNote(ref, func(current note) (map[string]string, error) {
		// Collection and mutation are separate. Never complete a note edited or
		// reopened in between, and never fabricate a successful write in cache.
		if ref.Metadata["content_hash"] != noteVersion(current) {
			return nil, fmt.Errorf("task note changed since collection")
		}
		if current.AgentReview {
			return nil, nil // Only explicit Done releases the hold, even for new contributors.
		}
		if reviewRequired {
			changed = true
			return s.agentReviewUpdates(current, ref, request)
		}
		if len(workItems) == 0 {
			return nil, nil
		}
		// Active remote work takes precedence over a locally completed note.
		// Recording the completed subset also arms the next completion cycle.
		if !allDone {
			updates := map[string]string{}
			if current.State == "done" {
				updates["state"] = "open"
				updates["completed_at"] = ""
			}
			if current.CompletionBaseline != baseline {
				updates["completion_baseline"] = baseline
			}
			changed = len(updates) > 0
			return updates, nil
		}
		if current.State == "done" {
			return nil, nil
		}
		if current.CompletionBaseline == "pending" {
			changed = true
			return map[string]string{"completion_baseline": baseline}, nil
		}
		if current.CompletionBaseline == baseline {
			return nil, nil
		}
		changed = true
		if activity != protocol.ActivityIdle {
			reviewRequired = true
			return s.agentReviewUpdates(current, ref, request)
		}
		return map[string]string{
			"state": "done", "completed_at": "__now_if_empty__",
			"completion_baseline": baseline,
		}, nil
	})
	if err != nil && (reviewRequired || (allDone && len(workItems) > 0 && activity != protocol.ActivityIdle)) {
		return nil, &integration.AgentReviewPersistenceError{Err: err}
	}
	if err != nil || !changed {
		return nil, err
	}
	vault, err := s.configuredVault()
	if err != nil {
		return nil, err
	}
	observation := observationsFor(vault, current)[0]
	return &observation, nil
}

var _ integration.TaskLifecycleProvider = Source{}

// Bind known source identities with the hold, not their statuses. Otherwise a
// cache reset could retain the review but lose the merged PR that caused it.
func (s Source) agentReviewUpdates(current note, ref protocol.SourceRef, request integration.TaskLifecycleRequest) (map[string]string, error) {
	task := request.Task
	if len(task.SourceRefs) == 0 {
		task.SourceRefs = request.WorkItems
	}
	task.SourceRefs = append(append([]protocol.SourceRef(nil), task.SourceRefs...), ref)
	bindings, err := taskBindings(task, false)
	if err != nil {
		return nil, err
	}
	vault, err := s.configuredVault()
	if err != nil {
		return nil, err
	}
	associated, err := associatedNote(vault, task, bindings)
	if err != nil {
		return nil, err
	}
	if associated == nil || associated.ID != current.ID {
		return nil, fmt.Errorf("agent review note association changed")
	}
	merged, _ := mergeBindings(current.Bindings, bindings)
	return map[string]string{
		"state": "open", "completed_at": "", "agent_review": "true",
		"bindings": encodeBindings(merged),
	}, nil
}
