package agentservice

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"radar/internal/protocol"
)

// DiagnosticAgents observes only registrations and persisted checkpoints. It
// deliberately does not call status/Manager.status: those contact live runners
// and inspect workspace runtimes. Scope is applied before checkpoint reads.
func DiagnosticAgents(workspace, id string) ([]protocol.DiagnosticAgent, error) {
	regs, err := diagnosticRegistrations()
	if err != nil {
		return nil, err
	}
	items := []protocol.DiagnosticAgent{}
	for _, reg := range regs {
		if (id != "" && reg.ID != id) || (workspace != "" && !registrationWithinWorkspace(reg, workspace)) {
			continue
		}
		binding := reg.Workspace
		binding.ID, binding.Path = protocol.DiagnosticText(binding.ID), protocol.DiagnosticText(binding.Path)
		binding.Mode, binding.Sandbox = protocol.DiagnosticText(binding.Mode), protocol.DiagnosticText(binding.Sandbox)
		item := protocol.DiagnosticAgent{ID: reg.ID, Registered: true, Desired: reg.Desired, Workspace: binding, OwnershipHold: "registered_owner_even_when_stopped", RunnerEvidence: "unknown_not_probed", LeaseEvidence: "unknown_not_inspected", CollectionEvidence: "not_configured", Schedules: []protocol.DiagnosticAgentSchedule{}, SupervisionEvidence: "unavailable_not_observed"}
		diagnosticSnapshot(reg, &item)
		if reg.Collection != "" {
			checkpoint, err := diagnosticCollection(reg)
			item.CollectionEvidence = "unavailable"
			if err == nil {
				item.CollectionEvidence = "fresh_checkpoint"
				item.Collection = &protocol.DiagnosticAgentCollection{Source: reg.Collection, Pending: len(checkpoint.Pending), DeliveryProblem: checkpoint.Problem != ""}
			}
		}
		// Reuse production's validated schedule projection and due calculation.
		// In-memory supervision errors cannot be reconstructed from checkpoints.
		schedules, err := diagnosticScheduleViews(reg, time.Now())
		item.SchedulesEvidence = "unavailable"
		if err == nil {
			item.SchedulesEvidence = "fresh_checkpoint_runtime_errors_unknown"
			count := len(schedules)
			item.ScheduleCount = &count
			for _, schedule := range schedules {
				if len(item.Schedules) == protocol.DiagnosticChildLimit {
					item.Truncated = true
					break
				}
				item.Schedules = append(item.Schedules, schedule.Schedule)
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// DiagnosticAgentSupervision copies only already-held retry/error evidence for
// the scoped registration IDs. The supervisor holds mu across native I/O, so a
// busy mutex returns unknown immediately rather than delaying inspection.
// No registration/checkpoint reads, status calls or work are initiated here.
func (m *Manager) DiagnosticAgentSupervision(ids []string) map[string]protocol.DiagnosticAgentSupervision {
	if !m.mu.TryLock() {
		return nil
	}
	defer m.mu.Unlock()
	items := make(map[string]protocol.DiagnosticAgentSupervision, len(ids))
	for _, id := range ids {
		view := protocol.DiagnosticAgentSupervision{RetryRecordEvidence: "no_current_record", ScheduleDeliveryProblem: m.scheduleErrors[id] != ""}
		if retry, ok := m.retry[id]; ok {
			next := diagnosticTime(retry.next)
			if retry.attempts < 1 || uint64(retry.attempts) > 1<<53-1 || retry.next.IsZero() || retry.next.UTC().Year() < 1 || retry.next.UTC().Year() > 9999 || len(next) > 35 {
				continue // Invalid native evidence is unavailable, never zero/healthy.
			}
			view.RetryRecordEvidence = "current_record"
			view.Retry = &protocol.DiagnosticAgentRetry{FailedStartAttempts: retry.attempts, RetryNotBefore: next, StartProblem: retry.problem != ""}
		}
		items[id] = view
	}
	return items
}

func diagnosticTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func diagnosticSnapshot(reg Registration, item *protocol.DiagnosticAgent) {
	item.SnapshotEvidence = "unavailable"
	dir, _ := instanceDir(reg.ID)
	file, err := os.OpenFile(filepath.Join(dir, "snapshot.json"), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		item.SnapshotEvidence = "missing_no_runtime_conclusion"
	}
	if err != nil {
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > protocol.AgentMaxFrameBytes {
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.AgentMaxFrameBytes+1))
	if err != nil || len(data) > protocol.AgentMaxFrameBytes {
		return
	}
	// Decode only allowlisted evidence. Private presentation, tasks, recent
	// events, action payloads, titles and conversation references are discarded.
	var snapshot diagnosticRunnerSnapshot
	if json.Unmarshal(data, &snapshot) != nil {
		item.SnapshotEvidence = "invalid_or_incompatible"
		return
	}
	view := snapshot.projection(reg.ID)
	if view == nil {
		item.SnapshotEvidence = "invalid_or_incompatible"
		return
	}
	item.SnapshotEvidence = "cached_runner_snapshot"
	item.SnapshotSavedAt = diagnosticTime(info.ModTime())
	item.Snapshot = view
}
