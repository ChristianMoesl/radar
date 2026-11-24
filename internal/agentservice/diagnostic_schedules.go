package agentservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	"radar/internal/protocol"
)

// DiagnosticAgentSchedules reads only scoped registrations and persisted schedule
// checkpoints. A failed checkpoint is unavailable, not a complete empty list.
func DiagnosticAgentSchedules(workspace, parentID, id string) ([]protocol.DiagnosticAgentScheduleEntry, error) {
	regs, err := diagnosticRegistrations()
	if err != nil {
		return nil, err
	}
	items := []protocol.DiagnosticAgentScheduleEntry{}
	now := time.Now()
	for _, reg := range regs {
		if (parentID != "" && reg.ID != parentID) || (workspace != "" && !registrationWithinWorkspace(reg, workspace)) {
			continue
		}
		schedules, err := diagnosticScheduleViews(reg, now)
		if err != nil {
			return nil, err
		}
		for _, schedule := range schedules {
			if id == "" || schedule.ID == id {
				items = append(items, schedule)
			}
		}
	}
	return items, nil
}

func diagnosticScheduleViews(reg Registration, now time.Time) ([]protocol.DiagnosticAgentScheduleEntry, error) {
	checkpoint, err := diagnosticSchedules(reg)
	if err != nil {
		return nil, err
	}
	items := []protocol.DiagnosticAgentScheduleEntry{}
	for _, schedule := range scheduleStateStatuses(checkpoint, now) {
		// Native checkpoint IDs may be long. Derive selectable opaque identity
		// before clipping, and never include payloads or changing due times.
		identity, _ := json.Marshal([]string{reg.ID, schedule.ID})
		sum := sha256.Sum256(identity)
		view := protocol.DiagnosticAgentSchedule{ID: protocol.DiagnosticText(schedule.ID), Name: protocol.DiagnosticText(schedule.Name), Enabled: schedule.Enabled, Time: schedule.Time, Timezone: protocol.DiagnosticText(schedule.Timezone), Weekdays: schedule.Weekdays, NextDue: diagnosticTime(schedule.NextDue), PendingAt: diagnosticTime(schedule.PendingAt), LastMissedAt: diagnosticTime(schedule.LastMissedAt), DeliveryProblem: schedule.Error != ""}
		if admission := schedule.LastAdmission; admission != nil {
			view.LastAdmission = &protocol.DiagnosticAgentAdmission{ScheduledAt: diagnosticTime(admission.ScheduledAt), AcceptedAt: diagnosticTime(admission.AcceptedAt), TaskID: admission.TaskID, CatchUp: admission.CatchUp}
		}
		items = append(items, protocol.DiagnosticAgentScheduleEntry{ID: "agent-schedule:" + hex.EncodeToString(sum[:]), AgentID: reg.ID, Schedule: view})
	}
	return items, nil
}

func diagnosticSchedules(reg Registration) (scheduleState, error) {
	data, _, err := diagnosticCheckpoint(schedulePath(reg))
	if os.IsNotExist(err) {
		// Exactly production's missing-checkpoint semantics, not a runner-state
		// claim: there is no configured schedule in the persisted checkpoint.
		return scheduleState{Version: 1}, nil
	}
	if err != nil {
		return scheduleState{}, err
	}
	return decodeSchedules(data)
}
