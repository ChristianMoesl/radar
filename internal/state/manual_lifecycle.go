package state

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"radar/internal/protocol"
	"radar/internal/taskdb"
)

// Source-only decisions belong to the durable task record, not to source
// snapshots. Keep exact provider identities (including resource lifetimes),
// so a recycled name or shared linking mark cannot inherit completion.
type manualLifecycle struct {
	DoneAt string                 `json:"done_at,omitempty"`
	Review bool                   `json:"review,omitempty"`
	Inputs map[string]manualInput `json:"inputs"`
}

type manualInput struct {
	Identity string `json:"identity"`
	Workflow string `json:"workflow,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

func manualInputFor(ref protocol.SourceRef) manualInput {
	return manualInput{Identity: ref.Binding().LinkingKey(), Workflow: ref.WorkflowState, Done: ref.Signal == "done"}
}

// SetManualLifecycle commits before publishing. A failed write must not show
// Done (or Reopen) in the dashboard or change automatic cleanup eligibility.
func (s *Store) SetManualLifecycle(taskID int, done bool) (protocol.Task, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	var task protocol.Task
	for _, item := range s.items {
		if item.ID == taskID {
			task = item
			break
		}
	}
	if task.ID == 0 {
		return protocol.Task{}, fmt.Errorf("task %d not found", taskID)
	}
	for i, record := range s.state.Records {
		if record.NumericID != taskID {
			continue
		}
		if len(recordAuthoredIdentities(s.state)[record.ID]) != 0 {
			return protocol.Task{}, fmt.Errorf("task %d has an authored note whose lifecycle could not be updated", taskID)
		}
		if done && task.ManualDone {
			return task, nil
		}
		if done && task.Attention == "done" {
			return protocol.Task{}, fmt.Errorf("task %d is already done", taskID)
		}
		if !done && !task.ManualDone {
			return protocol.Task{}, fmt.Errorf("task %d has no manual completion to reopen", taskID)
		}

		decision := &manualLifecycle{Inputs: map[string]manualInput{}}
		for _, ref := range s.state.SourceRefs {
			if ref.TaskRecordID == record.ID && authoritativeRef(ref.Snapshot) &&
				(ref.Active || (ref.Snapshot.RetainInactive && ref.Snapshot.Lifecycle == protocol.SourceRefLifecycleWorkItem)) {
				decision.Inputs[ref.ID] = manualInputFor(ref.Snapshot)
			}
		}
		if len(decision.Inputs) == 0 {
			return protocol.Task{}, fmt.Errorf("task %d has no stable source identity", taskID)
		}
		if done {
			decision.DoneAt = time.Now().UTC().Format(time.RFC3339Nano)
		}

		next := s.state
		next.Records = slices.Clone(next.Records)
		next.Records[i].ManualLifecycle = decision
		if err := validateAuthoredLinks(next); err != nil {
			return protocol.Task{}, err
		}
		items := projectTasks(next)
		index := slices.IndexFunc(items, func(item protocol.Task) bool { return item.ID == taskID })
		if index >= 0 {
			task = items[index]
		} else {
			// Reopen may restore acknowledgement filters and hide the row.
			hidden := projectTaskRecords(next, true, true)
			index := slices.IndexFunc(hidden, func(item protocol.Task) bool { return item.ID == taskID })
			if index < 0 {
				return protocol.Task{}, fmt.Errorf("task %d no longer has a usable source identity", taskID)
			}
			task = hidden[index]
		}
		data, err := json.Marshal(next)
		if err == nil {
			err = taskdb.WriteProjection(s.path, data)
		}
		if err != nil {
			return protocol.Task{}, fmt.Errorf("save local lifecycle for task %d: %w", taskID, err)
		}
		s.state, s.items, s.saveFailed = next, items, false
		s.bumpRevisionLocked()
		return task, nil
	}
	return protocol.Task{}, fmt.Errorf("task %d is no longer tracked", taskID)
}

// Only exact, still-associated authoritative refs can apply a local decision.
// A known resource ID reused with a different lifetime cannot authorize cleanup.
func (m *manualLifecycle) applies(refs []protocol.SourceRef) bool {
	matched := false
	for _, ref := range refs {
		if !authoritativeRef(ref) {
			continue
		}
		if input, ok := m.Inputs[ref.ID]; ok {
			if input.Identity != ref.Binding().LinkingKey() {
				return false
			}
			matched = true
		}
	}
	return matched
}

func effectiveLifecycle(record TaskRecord, active []protocol.SourceRef) TaskRecord {
	m := record.ManualLifecycle
	if m == nil || !m.applies(active) {
		record.ManualLifecycle = nil
		return record
	}
	if m.DoneAt != "" && !record.AgentReviewPending {
		record.State, record.DoneAt, record.Reason = "done", m.DoneAt, "Marked Done locally"
	} else {
		record.State, record.DoneAt, record.Reason = "active", "", ""
	}
	return record
}

func reconcileManualLifecycles(state persistedState, observed []protocol.Task, complete map[string]bool) {
	if !slices.ContainsFunc(state.Records, func(record TaskRecord) bool { return record.ManualLifecycle != nil }) {
		return
	}
	active := activeRefsByRecord(state.SourceRefs)
	fresh := map[string]bool{}
	for _, task := range observed {
		for _, ref := range task.SourceRefs {
			fresh[ref.ID] = complete[ref.Source]
		}
	}
	owners := recordAuthoredIdentities(state)
	for i := range state.Records {
		record := &state.Records[i]
		m := record.ManualLifecycle
		if m == nil {
			continue
		}
		if len(owners[record.ID]) > 0 {
			record.ManualLifecycle = nil // The authored lifecycle now owns this task.
			continue
		}
		if len(complete) == 0 {
			continue
		}
		refs := active[record.ID]
		applies := m.applies(refs)
		if applies && m.DoneAt != "" && slices.ContainsFunc(refs, func(ref protocol.SourceRef) bool {
			return authoritativeRef(ref) && fresh[ref.ID] && ref.Activity != protocol.ActivityIdle
		}) {
			record.ManualLifecycle = &manualLifecycle{Review: true, Inputs: m.Inputs}
			continue
		}
		if m.Review {
			continue // Only explicit Done releases an agent result-review hold.
		}
		next := *m
		next.Inputs = maps.Clone(m.Inputs)
		for _, ref := range refs {
			if !authoritativeRef(ref) || !fresh[ref.ID] {
				continue
			}
			prior, known := m.Inputs[ref.ID]
			input := manualInputFor(ref)
			if known && input.Identity != prior.Identity {
				record.ManualLifecycle = nil
				break
			}
			if ref.Lifecycle != protocol.SourceRefLifecycleWorkItem || ref.Authority == protocol.SourceRefAuthorityNone || ref.Signal == "" {
				continue
			}
			if m.DoneAt != "" {
				// Same rule as authored Done: missing workflow evidence does not
				// reopen work; confirmed terminal transitions advance independently.
				if input.Workflow == "" {
					continue
				}
				if !input.Done && (!known || (prior.Workflow != "" && prior.Workflow != input.Workflow)) {
					record.ManualLifecycle = nil
					break
				}
			} else if applies && input.Done && (!known || !prior.Done) {
				// An explicit Reopen disarms old terminal facts, not future ones.
				record.ManualLifecycle = nil
				break
			}
			if applies {
				next.Inputs[ref.ID] = input
			}
		}
		if record.ManualLifecycle != nil {
			record.ManualLifecycle = &next
		}
	}
}

func activeRefsByRecord(refs []SourceRefRecord) map[string][]protocol.SourceRef {
	active := map[string][]protocol.SourceRef{}
	for _, ref := range refs {
		if ref.Active && ref.ID != "" && ref.Snapshot.ID != "" {
			active[ref.TaskRecordID] = append(active[ref.TaskRecordID], ref.Snapshot)
		}
	}
	return active
}
