package diagnostics

import (
	"strconv"
	"testing"
	"time"

	"radar/internal/protocol"
	"radar/internal/state"
	"radar/internal/workspacegc"
)

func TestRetentionGateEvidenceIsNotExecutionEligibilityOrDisplayLimited(t *testing.T) {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	record := state.TaskRecord{ID: "record", NumericID: 1, State: "done", DoneAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}
	refs := []state.SourceRefRecord{}
	for i := 0; i < 25; i++ {
		id := "ref-" + strconv.Itoa(i)
		refs = append(refs, state.SourceRefRecord{ID: id, TaskRecordID: record.ID, Active: true, Snapshot: protocol.SourceRef{ID: id, Role: protocol.SourceRefRoleAuthoritative}})
	}
	refs[24].Snapshot.Activity = protocol.ActivityBusy
	view := taskView(record, refs, state.LinkingEvidence{}, now)
	if !view.Truncated || len(view.Sources) != protocol.DiagnosticChildLimit || view.GCRetention.Decision != "agent_work_or_review_pending" || view.GCRetention.Evidence != "production_gc_retention_gate_only" {
		t.Fatalf("late active owner lost to truncation: %+v", view)
	}
	refs[24].Snapshot.Activity = protocol.ActivityIdle
	view = taskView(record, refs, state.LinkingEvidence{}, now)
	expected := workspacegc.InspectRetention(record, refs, now)
	if view.GCRetention.Decision != expected.Decision || view.GCRetention.Decision != "retention_gate_passed" || view.GCRetention.ReadyAt != expected.ReadyAt.Format(time.RFC3339Nano) {
		t.Fatalf("production retention evidence changed: %+v", view.GCRetention)
	}
}
