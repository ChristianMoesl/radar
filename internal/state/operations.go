package state

import (
	"crypto/rand"
	"fmt"
	"time"

	"radar/internal/protocol"
)

const operationHistoryLimit = 64

// ReportResourceMutation records only what the client reported. Invalidation
// and authoritative collection are separate; do not publish daemon execution.
func (s *Store) ReportResourceMutation(target protocol.ResourceMutation) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextOperationIDLocked()
	s.appendOperationLocked(protocol.ManagedOperation{ID: id, Kind: "workspace", TaskID: target.TaskID, WorkspaceID: target.WorkspaceID, Status: "reported"})
	return id
}

// BeginOperation publishes execution accepted by this daemon. This bounded,
// in-memory status projection deliberately is not an event log or job queue.
func (s *Store) BeginOperation(kind string, taskID int, workspaceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextOperationIDLocked()
	now := time.Now()
	if s.operationStarts == nil {
		s.operationStarts = map[string]time.Time{}
	}
	s.operationStarts[id] = now
	s.appendOperationLocked(protocol.ManagedOperation{ID: id, Kind: kind, TaskID: taskID, WorkspaceID: workspaceID, Status: "running", StartedAt: now.UTC().Format(time.RFC3339Nano)})
	return id
}

func (s *Store) appendOperationLocked(operation protocol.ManagedOperation) {
	s.operations = append(s.operations, operation)
	s.trimOperationsLocked()
	s.bumpRevisionLocked()
}

func (s *Store) nextOperationIDLocked() string {
	if s.operationNamespace == "" {
		s.operationNamespace = rand.Text()
	}
	s.operationSequence++
	return fmt.Sprintf("%s:%d", s.operationNamespace, s.operationSequence)
}

func (s *Store) trimOperationsLocked() {
	terminal := 0
	for _, op := range s.operations {
		if op.Status != "running" {
			terminal++
		}
	}
	retained := s.operations[:0]
	for _, op := range s.operations {
		if op.Status != "running" && terminal > operationHistoryLimit {
			terminal--
			continue
		}
		retained = append(retained, op)
	}
	s.operations = retained
}

func (s *Store) FinishOperation(id string, result protocol.ManagedOperation) {
	if result.Status != "succeeded" && result.Status != "failed" && result.Status != "partial" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.operations {
		if s.operations[i].ID == id && s.operations[i].Status == "running" {
			op := s.operations[i]
			// Only the store's native lifecycle supplies timing. Ignore timestamps
			// in result, and keep first completion immutable on duplicate finishes.
			if start, ok := s.operationStarts[id]; ok {
				now := time.Now()
				op.FinishedAt = now.UTC().Format(time.RFC3339Nano)
				op.ElapsedMilliseconds = now.Sub(start).Milliseconds()
				delete(s.operationStarts, id)
			}
			op.Status = result.Status
			op.CompletedTargets, op.TotalTargets = result.CompletedTargets, result.TotalTargets
			if result.TaskID != 0 {
				op.TaskID = result.TaskID
			}
			s.operations = append(s.operations[:i], s.operations[i+1:]...)
			s.operations = append(s.operations, op)
			s.trimOperationsLocked()
			s.bumpRevisionLocked()
			return
		}
	}
}

// DashboardSnapshot pairs operations with the same projection revision. In
// particular a delayed snapshot cannot erase a newer operation completion.
func (s *Store) DashboardSnapshot() protocol.Response {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tasks := make([]protocol.Task, len(s.items))
	for i, task := range s.items {
		tasks[i] = cloneTask(task)
	}
	return protocol.Response{Tasks: tasks, Sources: append([]protocol.SourceStatus{}, s.state.Sources...), Operations: append([]protocol.ManagedOperation{}, s.operations...), Revision: s.revision}
}

// OperationInspectionSnapshot joins volatile operations to retained scope
// evidence at one revision, including hidden records. Callers must project an
// allowlist; client-reported target identities are not verified ownership.
func (s *Store) OperationInspectionSnapshot() ([]TaskRecord, []SourceRefRecord, []protocol.ManagedOperation, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inspectionRecordsLocked(), s.inspectionSourceRefsLocked(), append([]protocol.ManagedOperation{}, s.operations...), s.revision
}
