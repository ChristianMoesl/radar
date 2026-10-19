package agentservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"radar/internal/integration"
	"radar/internal/protocol"
)

type collectionRevision struct {
	Hash     string `json:"hash"`
	Sequence uint64 `json:"sequence"`
	Baseline bool   `json:"baseline"`
}

type collectionEvent struct {
	Source  string          `json:"source"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// The source checkpoint and outbox move atomically. Only a matching durable
// receipt removes an entry; uncertain responses retain identical bytes/IDs.
type collectionState struct {
	Version     int                           `json:"version"`
	Source      string                        `json:"source"`
	Initialized bool                          `json:"initialized"`
	Seen        map[string]collectionRevision `json:"seen"`
	Pending     []collectionEvent             `json:"pending"`
	Problem     string                        `json:"problem,omitempty"`
}

func collectionPath(reg Registration) string {
	dir, _ := instanceDir(reg.ID)
	return filepath.Join(dir, "collection.json")
}

func loadCollection(reg Registration) (collectionState, error) {
	state := collectionState{Version: 1, Source: reg.Collection, Seen: map[string]collectionRevision{}}
	data, err := os.ReadFile(collectionPath(reg))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version != 1 || state.Source != reg.Collection || state.Seen == nil {
		return state, fmt.Errorf("unsupported collection checkpoint; preserve agent data")
	}
	return state, nil
}

func (m *Manager) ObserveCollection(ctx context.Context, source string, items []integration.CollectionItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	regs, err := registrations()
	if err != nil {
		return err
	}
	// Sort and normalize JSON once, so source ordering/object key ordering cannot
	// cause spurious changes. Reject a contradictory snapshot before writing.
	facts := map[string]json.RawMessage{}
	for _, item := range items {
		var value any
		if item.Key == "" || json.Unmarshal(item.Payload, &value) != nil {
			return fmt.Errorf("invalid collection item")
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if prior, ok := facts[item.Key]; ok && string(prior) != string(payload) {
			return fmt.Errorf("conflicting collection item %s", item.Key)
		}
		facts[item.Key] = payload
	}
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var failures []error
	for _, reg := range regs {
		if reg.Collection != source {
			continue
		}
		state, err := loadCollection(reg)
		if err != nil {
			failures = append(failures, fmt.Errorf("agent %s: %w", reg.ID, err))
			continue
		}
		changed := !state.Initialized
		for _, key := range keys {
			payload := facts[key]
			sum := sha256.Sum256(payload)
			hash := hex.EncodeToString(sum[:])
			prior := state.Seen[key]
			if prior.Hash == hash {
				continue
			}
			change := "changed"
			if !state.Initialized {
				change = "snapshot"
			} else if prior.Sequence == 0 {
				change = "discovered"
			}
			revision := collectionRevision{Hash: hash, Sequence: prior.Sequence + 1, Baseline: !state.Initialized || prior.Baseline}
			envelope, _ := json.Marshal(map[string]any{"change": change, "baseline": revision.Baseline, "key": key, "revision": hash, "data": payload})
			event := collectionEvent{Source: "radar.collection." + source, ID: fmt.Sprintf("%s:%d:%s", key, revision.Sequence, hash), Payload: envelope}
			wire, _ := json.Marshal(event)
			if len(wire) > protocol.AgentMaxFrameBytes-1024 {
				return fmt.Errorf("collection item %s exceeds admission limit", key)
			}
			state.Pending = append(state.Pending, event)
			state.Seen[key] = revision
			changed = true
		}
		state.Initialized = true
		if changed {
			if err := atomicJSON(collectionPath(reg), state); err != nil {
				failures = append(failures, fmt.Errorf("agent %s: %w", reg.ID, err))
			}
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) collectionStatus(reg Registration, status *protocol.AgentStatus) {
	if reg.Collection == "" {
		return
	}
	status.Collection = reg.Collection
	state, err := loadCollection(reg)
	if err != nil {
		status.CollectionError = err.Error()
		return
	}
	status.CollectionPending = len(state.Pending)
	status.CollectionError = state.Problem
}

func (m *Manager) flushCollection(ctx context.Context, reg Registration) error {
	if reg.Collection == "" {
		return nil
	}
	state, err := loadCollection(reg)
	if err != nil {
		return err
	}
	path, err := socketPath(reg.ID)
	if err != nil {
		return err
	}
	// Bound each supervision pass so one backlog cannot starve other agents.
	for delivered := 0; len(state.Pending) > 0 && delivered < 25; delivered++ {
		event := state.Pending[0]
		var receipt struct {
			TaskID  int    `json:"taskId"`
			Source  string `json:"source"`
			EventID string `json:"eventId"`
		}
		err := call(ctx, path, "deliver", event, &receipt)
		if err == nil && (receipt.TaskID <= 0 || receipt.Source != event.Source || receipt.EventID != event.ID) {
			err = fmt.Errorf("invalid collection admission receipt")
		}
		if err != nil {
			state.Problem = "collection delivery pending: " + err.Error()
			if saveErr := atomicJSON(collectionPath(reg), state); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			return err
		}
		state.Pending = state.Pending[1:]
		state.Problem = ""
		if err := atomicJSON(collectionPath(reg), state); err != nil {
			return err
		}
	}
	return nil
}
