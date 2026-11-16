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
	regs, err := registrations()
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
		item := protocol.DiagnosticAgent{ID: reg.ID, Registered: true, Desired: reg.Desired, Workspace: binding, OwnershipHold: "registered_owner_even_when_stopped", RunnerEvidence: "unknown_not_probed", LeaseEvidence: "unknown_not_inspected", CollectionEvidence: "not_configured", Schedules: []protocol.DiagnosticAgentSchedule{}}
		diagnosticSnapshot(reg, &item)
		if reg.Collection != "" {
			checkpoint, err := loadCollection(reg)
			item.CollectionEvidence = "unavailable"
			if err == nil {
				item.CollectionEvidence = "fresh_checkpoint"
				item.Collection = &protocol.DiagnosticAgentCollection{Source: reg.Collection, Pending: len(checkpoint.Pending), DeliveryProblem: checkpoint.Problem != ""}
			}
		}
		// Reuse production's validated schedule projection and due calculation.
		// In-memory supervision errors cannot be reconstructed from checkpoints.
		schedules, err := scheduleStatuses(reg, time.Now())
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
				view := protocol.DiagnosticAgentSchedule{ID: protocol.DiagnosticText(schedule.ID), Name: protocol.DiagnosticText(schedule.Name), Enabled: schedule.Enabled, Time: schedule.Time, Timezone: protocol.DiagnosticText(schedule.Timezone), Weekdays: schedule.Weekdays, NextDue: diagnosticTime(schedule.NextDue), PendingAt: diagnosticTime(schedule.PendingAt), LastMissedAt: diagnosticTime(schedule.LastMissedAt), DeliveryProblem: schedule.Error != ""}
				if admission := schedule.LastAdmission; admission != nil {
					view.LastAdmission = &protocol.DiagnosticAgentAdmission{ScheduledAt: diagnosticTime(admission.ScheduledAt), AcceptedAt: diagnosticTime(admission.AcceptedAt), TaskID: admission.TaskID, CatchUp: admission.CatchUp}
				}
				item.Schedules = append(item.Schedules, view)
			}
		}
		items = append(items, item)
	}
	return items, nil
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
	var snapshot struct {
		Protocol          int    `json:"protocol"`
		InstanceID        string `json:"instanceId"`
		DefinitionVersion int    `json:"definitionVersion"`
		Pending           int    `json:"pending"`
		Problem           string `json:"problem"`
		Presentation      struct {
			Attention bool `json:"attention"`
			Revision  int  `json:"revision"`
		} `json:"presentation"`
	}
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Protocol != protocol.AgentProtocolVersion || snapshot.InstanceID != reg.ID || snapshot.Pending < 0 {
		item.SnapshotEvidence = "invalid_or_incompatible"
		return
	}
	item.SnapshotEvidence = "cached_runner_snapshot"
	item.SnapshotSavedAt = diagnosticTime(info.ModTime())
	item.Snapshot = &protocol.DiagnosticAgentSnapshot{DefinitionVersion: snapshot.DefinitionVersion, Revision: snapshot.Presentation.Revision, Pending: snapshot.Pending, Attention: snapshot.Presentation.Attention, Problem: snapshot.Problem != ""}
}
