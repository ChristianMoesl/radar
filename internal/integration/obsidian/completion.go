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
func (s Source) ReconcileCompletion(_ context.Context, ref protocol.SourceRef, workItems []protocol.SourceRef) (*integration.Observation, error) {
	if len(workItems) == 0 {
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
		if ref.Metadata["content_hash"] != fmt.Sprintf("%x", sha256.Sum256([]byte(current.content))) {
			return nil, fmt.Errorf("task note changed since collection")
		}
		if current.State == "done" {
			return nil, nil
		}
		if current.CompletionBaseline == "pending" {
			changed = true
			return map[string]string{"radar-completion-baseline": baseline}, nil
		}
		if !allDone {
			if current.CompletionBaseline != "" && current.CompletionBaseline != baseline {
				changed = true
				return map[string]string{"radar-completion-baseline": baseline}, nil
			}
			return nil, nil
		}
		if current.CompletionBaseline == baseline {
			return nil, nil
		}
		changed = true
		return map[string]string{
			"radar-state": "done", "radar-completed-at": "__now_if_empty__",
			"radar-completion-baseline": baseline,
		}, nil
	})
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

var _ integration.TaskCompletionProvider = Source{}
